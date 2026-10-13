// Package integration holds the tests that run the real HTTP handlers on the
// real PostgreSQL repositories (the db-test service). All tests live in this
// one package and run one after another, so they can share the database.
package integration

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"po-trans/internal/app"
	"po-trans/internal/httpapi"
	"po-trans/internal/repository/postgres"
)

// ctxTimeout bounds every database call made by helpers.
const ctxTimeout = 30 * time.Second

func databaseURL() string { return os.Getenv("DATABASE_URL") }

// TestMain requires the db-test database and fails the package on leaked goroutines.
func TestMain(m *testing.M) {
	if databaseURL() == "" {
		// Not a skip: a run without a database must not look green.
		_, _ = os.Stderr.WriteString("DATABASE_URL is not set: integration tests need the db-test database (make test-ci)\n")
		os.Exit(1)
	}
	goleak.VerifyTestMain(m)
}

// newPool opens a pool for the test and closes it when the test ends.
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := postgres.NewPool(t.Context(), databaseURL(), 20)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// reset empties both tables.
func reset(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `TRUNCATE transfers, accounts`)
	require.NoError(t, err)
}

// seed loads the demo accounts (acc_1 … acc_10, 1000 each) from the same file
// the demo environment uses.
func seed(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	sql, err := os.ReadFile("../../deploy/local/seed.sql")
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), string(sql))
	require.NoError(t, err)
}

// freshSeeded gives a pool on an empty database holding the demo accounts.
func freshSeeded(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := newPool(t)
	reset(t, pool)
	seed(t, pool)
	return pool
}

func setBalance(t *testing.T, pool *pgxpool.Pool, id string, balance int64) {
	t.Helper()
	tag, err := pool.Exec(t.Context(), `UPDATE accounts SET balance = $2 WHERE id = $1`, id, balance)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())
}

func balanceOf(t *testing.T, pool *pgxpool.Pool, id string) int64 {
	t.Helper()
	var b int64
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT balance FROM accounts WHERE id = $1`, id).Scan(&b))
	return b
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(t.Context(), sql, args...).Scan(&n))
	return n
}

// requireInvariants checks that nothing was created or destroyed and nothing
// went negative, and that the balances match the completed transfers.
func requireInvariants(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var total, lowest int64
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT COALESCE(SUM(balance), 0), COALESCE(MIN(balance), 0) FROM accounts`).Scan(&total, &lowest))
	require.EqualValues(t, 10000, total, "total balance")
	require.GreaterOrEqual(t, lowest, int64(0), "no negative balance")

	rows, err := pool.Query(t.Context(), `
SELECT a.id, a.balance - 1000 - COALESCE(i.amount, 0) + COALESCE(o.amount, 0)
FROM accounts a
LEFT JOIN (SELECT to_account_id id, SUM(amount) amount FROM transfers WHERE status = 'completed' GROUP BY 1) i ON i.id = a.id
LEFT JOIN (SELECT from_account_id id, SUM(amount) amount FROM transfers WHERE status = 'completed' GROUP BY 1) o ON o.id = a.id`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var id string
		var diff int64
		require.NoError(t, rows.Scan(&id, &diff))
		require.Zero(t, diff, "balance of %s must equal 1000 plus completed transfers", id)
	}
	require.NoError(t, rows.Err())
}

// newServer serves the real handlers on the given pool.
var discard = slog.New(slog.DiscardHandler)

func newServer(t *testing.T, pool *pgxpool.Pool, retry postgres.RetryPolicy) *httptest.Server {
	t.Helper()
	queries := postgres.NewQueryRepository(pool)
	writer := postgres.NewTransferRepository(pool, retry, discard)
	h := httpapi.NewHandler(httpapi.Deps{
		App:    app.New(writer, queries),
		Pinger: postgres.NewHealthCheck(pool),
	}, httpapi.Options{RequestTimeout: ctxTimeout, Logger: discard})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// api is a tiny client for one test server.
type api struct {
	t   *testing.T
	srv *httptest.Server
}

type reply struct {
	Code int
	Body string
}

// try sends a request and returns what happened. It never touches *testing.T,
// so it is safe to call from any goroutine.
func (a api) try(method, path, body string) (reply, error) {
	req, err := http.NewRequestWithContext(a.t.Context(), method, a.srv.URL+path, strings.NewReader(body))
	if err != nil {
		return reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := a.srv.Client().Do(req)
	if err != nil {
		return reply{}, err
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return reply{}, err
	}
	return reply{Code: res.StatusCode, Body: strings.TrimSpace(string(b))}, nil
}

// tryPost is try for POST /api/v1/transfers.
func (a api) tryPost(body string) (reply, error) {
	return a.try(http.MethodPost, "/api/v1/transfers", body)
}

// do is try for the test goroutine: a failure ends the test.
func (a api) do(method, path, body string) reply {
	a.t.Helper()
	r, err := a.try(method, path, body)
	require.NoError(a.t, err)
	return r
}

func (a api) post(body string) reply { return a.do(http.MethodPost, "/api/v1/transfers", body) }

func (a api) get(path string) reply { return a.do(http.MethodGet, path, "") }

func body(key, from, to string, amount int64) string {
	b, _ := json.Marshal(map[string]any{"idempotency_key": key, "from_account_id": from, "to_account_id": to, "amount": amount})
	return string(b)
}

func (r reply) field(t *testing.T, name string) any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.Body), &m), r.Body)
	return m[name]
}

// runParallel runs fn(0..n-1) with at most limit calls in flight and returns
// the joined errors. fn must not use *testing.T: assertions belong to the test
// goroutine, after runParallel returns.
func runParallel(n, limit int, fn func(i int) error) error {
	sem := make(chan struct{}, limit)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			errs[i] = fn(i)
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// postAll posts bodyOf(0..n-1) with at most limit requests in flight and
// returns the replies in order.
func (a api) postAll(n, limit int, bodyOf func(i int) string) ([]reply, error) {
	replies := make([]reply, n)
	err := runParallel(n, limit, func(i int) error {
		r, err := a.tryPost(bodyOf(i))
		replies[i] = r
		return err
	})
	return replies, err
}
