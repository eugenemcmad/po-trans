package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	apidocs "po-trans/docs"
	"po-trans/internal/app"
	"po-trans/internal/domain"
	"po-trans/internal/httpapi"
	"po-trans/tests/memstore"
)

// TestMain fails the package on leaked goroutines.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// env is a handler on top of an in-memory store plus the log it writes.
type env struct {
	store   *memstore.Store
	handler http.Handler
	log     *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	store := memstore.Seeded()
	logBuf := &bytes.Buffer{}
	h := httpapi.NewHandler(httpapi.Deps{
		App:    app.New(store, store),
		Pinger: store,
	}, httpapi.Options{
		RequestTimeout: 5 * time.Second,
		Logger:         slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	return &env{store: store, handler: h, log: logBuf}
}

type response struct {
	code   int
	header http.Header
	body   string
}

func (e *env) do(t *testing.T, method, target, contentType, body string, headers ...string) response {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	res := rec.Result()
	defer func() { require.NoError(t, res.Body.Close()) }()
	b, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return response{code: res.StatusCode, header: res.Header, body: string(b)}
}

func (e *env) post(t *testing.T, body string) response {
	t.Helper()
	return e.do(t, http.MethodPost, "/api/v1/transfers", "application/json", body)
}

func transferBody(key, from, to string, amount int) string {
	b, _ := json.Marshal(map[string]any{"idempotency_key": key, "from_account_id": from, "to_account_id": to, "amount": amount})
	return string(b)
}

// TestPostTransfer_Success: the 200 body has exactly the fields of the contract.
func TestPostTransfer_Success(t *testing.T) {
	e := newEnv(t)
	res := e.post(t, transferBody("k1", "acc_1", "acc_2", 100))

	require.Equal(t, http.StatusOK, res.code)
	require.Equal(t, "application/json", res.header.Get("Content-Type"))

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.body), &got))
	// exactly the contract fields, nothing else (no reason, no created_at)
	require.ElementsMatch(t, []string{"transfer_id", "status", "from_account_id", "to_account_id", "amount"}, keys(got))
	require.Equal(t, "completed", got["status"])
	require.Equal(t, "acc_1", got["from_account_id"])
	require.Equal(t, "acc_2", got["to_account_id"])
	require.EqualValues(t, 100, got["amount"])
	require.NotEmpty(t, got["transfer_id"])
	require.Equal(t, int64(900), e.store.Balance("acc_1"))
	require.Equal(t, int64(1100), e.store.Balance("acc_2"))
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestPostTransfer_Responses: every status code and reason text of POST /transfers (422, 409, 400) and the order of checks, one table row per case.
func TestPostTransfer_Responses(t *testing.T) {
	const used = `{"idempotency_key":"used","from_account_id":"acc_1","to_account_id":"acc_2","amount":100}`
	longKey := strings.Repeat("k", domain.MaxIdempotencyKeyLen+1)

	tests := []struct {
		name        string
		contentType string
		body        string
		prior       string // posted first with the same environment
		wantCode    int
		wantBody    string
	}{
		// 422: the three rejections
		{name: "insufficient funds", body: transferBody("k", "acc_1", "acc_2", 1001), wantCode: 422, wantBody: `{"status":"rejected","reason":"insufficient funds"}`},
		{name: "self transfer", body: transferBody("k", "acc_1", "acc_1", 1), wantCode: 422, wantBody: `{"status":"rejected","reason":"self-transfer not allowed"}`},
		{name: "self transfer of unknown account", body: transferBody("k", "acc_99", "acc_99", 1), wantCode: 422, wantBody: `{"status":"rejected","reason":"self-transfer not allowed"}`},
		{name: "unknown sender", body: transferBody("k", "acc_99", "acc_2", 1), wantCode: 422, wantBody: `{"status":"rejected","reason":"account not found"}`},
		{name: "unknown receiver", body: transferBody("k", "acc_1", "acc_99", 1), wantCode: 422, wantBody: `{"status":"rejected","reason":"account not found"}`},
		{name: "upper case account id", body: transferBody("k", "ACC_1", "acc_2", 1), wantCode: 422, wantBody: `{"status":"rejected","reason":"account not found"}`},
		{name: "account with surrounding space", body: transferBody("k", " acc_1", "acc_2", 1), wantCode: 422, wantBody: `{"status":"rejected","reason":"account not found"}`},

		// 409
		{name: "same key other amount", prior: used, body: transferBody("used", "acc_1", "acc_2", 101), wantCode: 409, wantBody: `{"status":"error","reason":"idempotency key reused with different parameters"}`},
		{name: "same key other receiver", prior: used, body: transferBody("used", "acc_1", "acc_3", 100), wantCode: 409, wantBody: `{"status":"error","reason":"idempotency key reused with different parameters"}`},
		{name: "same key negative amount", prior: used, body: `{"idempotency_key":"used","from_account_id":"acc_1","to_account_id":"acc_2","amount":-1}`, wantCode: 409, wantBody: `{"status":"error","reason":"idempotency key reused with different parameters"}`},
		{name: "same key amount 100.0", prior: used, body: `{"idempotency_key":"used","from_account_id":"acc_1","to_account_id":"acc_2","amount":100.0}`, wantCode: 409, wantBody: `{"status":"error","reason":"idempotency key reused with different parameters"}`},
		{name: "same key amount as string", prior: used, body: `{"idempotency_key":"used","from_account_id":"acc_1","to_account_id":"acc_2","amount":"100"}`, wantCode: 409, wantBody: `{"status":"error","reason":"idempotency key reused with different parameters"}`},
		{name: "same key from is a number", prior: used, body: `{"idempotency_key":"used","from_account_id":1,"to_account_id":"acc_2","amount":100}`, wantCode: 409, wantBody: `{"status":"error","reason":"idempotency key reused with different parameters"}`},
		{name: "same key empty from", prior: used, body: `{"idempotency_key":"used","from_account_id":"","to_account_id":"acc_2","amount":100}`, wantCode: 409, wantBody: `{"status":"error","reason":"idempotency key reused with different parameters"}`},
		{name: "same key missing amount", prior: used, body: `{"idempotency_key":"used","from_account_id":"acc_1","to_account_id":"acc_2"}`, wantCode: 409, wantBody: `{"status":"error","reason":"idempotency key reused with different parameters"}`},
		{name: "same key amount in other case is a missing amount", prior: used, body: `{"idempotency_key":"used","from_account_id":"acc_1","to_account_id":"acc_2","Amount":100}`, wantCode: 409, wantBody: `{"status":"error","reason":"idempotency key reused with different parameters"}`},
		{name: "same key different case", prior: used, body: transferBody("USED", "acc_1", "acc_2", 100), wantCode: 200},

		// 400: transport
		{name: "no content type", contentType: "-", body: transferBody("k", "acc_1", "acc_2", 1), wantCode: 400, wantBody: `{"status":"error","reason":"unsupported content type"}`},
		{name: "text content type", contentType: "text/plain", body: transferBody("k", "acc_1", "acc_2", 1), wantCode: 400, wantBody: `{"status":"error","reason":"unsupported content type"}`},
		{name: "json with charset", contentType: "application/json; charset=utf-8", body: transferBody("k", "acc_1", "acc_2", 1), wantCode: 200},
		{name: "json upper case", contentType: "Application/JSON", body: transferBody("k", "acc_1", "acc_2", 1), wantCode: 200},
		{name: "broken json", body: `{"idempotency_key":`, wantCode: 400, wantBody: `{"status":"error","reason":"invalid json"}`},
		{name: "empty body", body: ``, wantCode: 400, wantBody: `{"status":"error","reason":"invalid json"}`},
		{name: "json array", body: `[]`, wantCode: 400, wantBody: `{"status":"error","reason":"invalid json"}`},
		{name: "json null", body: `null`, wantCode: 400, wantBody: `{"status":"error","reason":"invalid json"}`},
		{name: "json string", body: `"x"`, wantCode: 400, wantBody: `{"status":"error","reason":"invalid json"}`},
		{name: "two objects", body: transferBody("k", "acc_1", "acc_2", 1) + transferBody("k", "acc_1", "acc_2", 1), wantCode: 400, wantBody: `{"status":"error","reason":"invalid json"}`},
		{name: "invalid utf8", body: "{\"idempotency_key\":\"a\xff\",\"from_account_id\":\"acc_1\",\"to_account_id\":\"acc_2\",\"amount\":1}", wantCode: 400, wantBody: `{"status":"error","reason":"invalid json"}`},
		{name: "body too large", body: `{"idempotency_key":"` + strings.Repeat("a", 70<<10) + `"}`, wantCode: 400, wantBody: `{"status":"error","reason":"request body too large"}`},

		// 400: fields, in contract order
		{name: "key missing", body: `{"from_account_id":"acc_1","to_account_id":"acc_2","amount":1}`, wantCode: 400, wantBody: `{"status":"error","reason":"idempotency_key is required"}`},
		{name: "key null", body: `{"idempotency_key":null,"from_account_id":"acc_1","to_account_id":"acc_2","amount":1}`, wantCode: 400, wantBody: `{"status":"error","reason":"idempotency_key is required"}`},
		{name: "key spaces", body: transferBody("   ", "acc_1", "acc_2", 1), wantCode: 400, wantBody: `{"status":"error","reason":"idempotency_key is required"}`},
		{name: "key tabs", body: transferBody("\t\t", "acc_1", "acc_2", 1), wantCode: 400, wantBody: `{"status":"error","reason":"idempotency_key is required"}`},
		{name: "key is a number", body: `{"idempotency_key":5,"from_account_id":"acc_1","to_account_id":"acc_2","amount":1}`, wantCode: 400, wantBody: `{"status":"error","reason":"idempotency_key must be a string"}`},
		{name: "key too long", body: transferBody(longKey, "acc_1", "acc_2", 1), wantCode: 400, wantBody: `{"status":"error","reason":"idempotency_key is too long"}`},
		{name: "key 255 is fine", body: transferBody(strings.Repeat("k", domain.MaxIdempotencyKeyLen), "acc_1", "acc_2", 1), wantCode: 200},
		{name: "key with control", body: transferBody("a\nb", "acc_1", "acc_2", 1), wantCode: 400, wantBody: `{"status":"error","reason":"idempotency_key contains invalid characters"}`},
		{name: "from missing", body: `{"idempotency_key":"k","to_account_id":"acc_2","amount":1}`, wantCode: 400, wantBody: `{"status":"error","reason":"from_account_id is required"}`},
		{name: "from is an object", body: `{"idempotency_key":"k","from_account_id":{},"to_account_id":"acc_2","amount":1}`, wantCode: 400, wantBody: `{"status":"error","reason":"from_account_id must be a string"}`},
		{name: "from too long", body: transferBody("k", strings.Repeat("a", domain.MaxAccountIDLen+1), "acc_2", 1), wantCode: 400, wantBody: `{"status":"error","reason":"from_account_id is too long"}`},
		{name: "to empty", body: transferBody("k", "acc_1", "", 1), wantCode: 400, wantBody: `{"status":"error","reason":"to_account_id is required"}`},
		{name: "to is true", body: `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":true,"amount":1}`, wantCode: 400, wantBody: `{"status":"error","reason":"to_account_id must be a string"}`},
		{name: "to with control", body: transferBody("k", "acc_1", "acc\x07", 1), wantCode: 400, wantBody: `{"status":"error","reason":"to_account_id contains invalid characters"}`},
		{name: "amount missing", body: `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2"}`, wantCode: 400, wantBody: `{"status":"error","reason":"amount must be a positive integer"}`},
		{name: "amount null", body: `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":null}`, wantCode: 400, wantBody: `{"status":"error","reason":"amount must be a positive integer"}`},
		{name: "amount zero", body: transferBody("k", "acc_1", "acc_2", 0), wantCode: 400, wantBody: `{"status":"error","reason":"amount must be a positive integer"}`},
		{name: "amount negative", body: transferBody("k", "acc_1", "acc_2", -1), wantCode: 400, wantBody: `{"status":"error","reason":"amount must be a positive integer"}`},
		{name: "amount fractional", body: `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":100.5}`, wantCode: 400, wantBody: `{"status":"error","reason":"amount must be a positive integer"}`},
		{name: "amount 100.0", body: `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":100.0}`, wantCode: 400, wantBody: `{"status":"error","reason":"amount must be a positive integer"}`},
		{name: "amount exponent", body: `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":1e2}`, wantCode: 400, wantBody: `{"status":"error","reason":"amount must be a positive integer"}`},
		{name: "amount string", body: `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":"100"}`, wantCode: 400, wantBody: `{"status":"error","reason":"amount must be a positive integer"}`},
		{name: "amount negative string", body: `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":"-5"}`, wantCode: 400, wantBody: `{"status":"error","reason":"amount must be a positive integer"}`},
		{name: "amount true", body: `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":true}`, wantCode: 400, wantBody: `{"status":"error","reason":"amount must be a positive integer"}`},
		{name: "amount overflows int64", body: `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":9223372036854775808}`, wantCode: 400, wantBody: `{"status":"error","reason":"amount must be a positive integer"}`},
		{name: "amount max int64 is valid, then insufficient", body: `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":9223372036854775807}`, wantCode: 422, wantBody: `{"status":"rejected","reason":"insufficient funds"}`},
		{name: "first error wins", body: `{"idempotency_key":"k","from_account_id":"","to_account_id":"","amount":-1}`, wantCode: 400, wantBody: `{"status":"error","reason":"from_account_id is required"}`},
		{name: "field names are case sensitive", body: `{"IDEMPOTENCY_KEY":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":1}`, wantCode: 400, wantBody: `{"status":"error","reason":"idempotency_key is required"}`},
		{name: "unknown fields are ignored", body: `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":1,"note":"hi"}`, wantCode: 200},
		{name: "duplicate field: last wins", body: `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":5,"amount":7}`, wantCode: 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			if tt.prior != "" {
				require.Equal(t, 200, e.post(t, tt.prior).code)
			}
			contentType := tt.contentType
			switch contentType {
			case "":
				contentType = "application/json"
			case "-":
				contentType = ""
			}
			res := e.do(t, http.MethodPost, "/api/v1/transfers", contentType, tt.body)

			require.Equal(t, tt.wantCode, res.code, res.body)
			require.Equal(t, "application/json", res.header.Get("Content-Type"))
			if tt.wantBody != "" {
				require.JSONEq(t, tt.wantBody, res.body)
			}
		})
	}
}

// TestPostTransfer_RepeatGivesIdenticalAnswer: rule 2 — a repeat returns the same status and body, for completed and rejected transfers alike.
func TestPostTransfer_RepeatGivesIdenticalAnswer(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{name: "completed", body: transferBody("k", "acc_1", "acc_2", 100), wantCode: 200},
		{name: "rejected", body: transferBody("k", "acc_1", "acc_2", 5000), wantCode: 422},
		{name: "self transfer", body: transferBody("k", "acc_1", "acc_1", 5), wantCode: 422},
		{name: "unknown account", body: transferBody("k", "acc_1", "acc_99", 5), wantCode: 422},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			first := e.post(t, tt.body)
			second := e.post(t, tt.body)

			require.Equal(t, tt.wantCode, first.code)
			require.Equal(t, first.code, second.code)
			require.Equal(t, first.body, second.body)
			if tt.wantCode == 200 {
				require.Equal(t, int64(900), e.store.Balance("acc_1"), "applied once")
			}
		})
	}
}

// TestGetTransfer: the GET format, reason only for rejected transfers, keys as encoded path segments, and nothing stored for a 400.
func TestGetTransfer(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, 200, e.post(t, transferBody("done", "acc_1", "acc_2", 100)).code)
	require.Equal(t, 422, e.post(t, transferBody("nope", "acc_1", "acc_2", 5000)).code)
	require.Equal(t, 200, e.post(t, transferBody("a/b c%d", "acc_1", "acc_2", 1)).code)
	require.Equal(t, 200, e.post(t, transferBody("..", "acc_1", "acc_2", 1)).code)
	require.Equal(t, 200, e.post(t, transferBody("ключ", "acc_1", "acc_2", 1)).code)

	t.Run("completed", func(t *testing.T) {
		res := e.do(t, http.MethodGet, "/api/v1/transfers/done", "", "")
		require.Equal(t, 200, res.code)
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(res.body), &got))
		require.ElementsMatch(t, []string{"transfer_id", "idempotency_key", "from_account_id", "to_account_id", "amount", "status", "created_at"}, keys(got))
		require.Equal(t, "completed", got["status"])
		require.Equal(t, "done", got["idempotency_key"])
		require.EqualValues(t, 100, got["amount"])
		created, err := time.Parse("2006-01-02T15:04:05Z", got["created_at"].(string))
		require.NoError(t, err, "second precision, UTC, Z suffix")
		require.WithinDuration(t, time.Now(), created, time.Minute)
	})

	t.Run("rejected carries its reason", func(t *testing.T) {
		res := e.do(t, http.MethodGet, "/api/v1/transfers/nope", "", "")
		require.Equal(t, 200, res.code)
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(res.body), &got))
		require.Equal(t, "rejected", got["status"])
		require.Equal(t, "insufficient funds", got["reason"])
	})

	t.Run("keys are path segments", func(t *testing.T) {
		for _, tc := range []struct{ name, path, key string }{
			{"slash space percent", "/api/v1/transfers/a%2Fb%20c%25d", "a/b c%d"},
			{"dot dot encoded", "/api/v1/transfers/%2E%2E", ".."},
			{"non-ascii", "/api/v1/transfers/%D0%BA%D0%BB%D1%8E%D1%87", "ключ"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				res := e.do(t, http.MethodGet, tc.path, "", "")
				require.Equal(t, 200, res.code, res.body)
				require.Contains(t, res.body, `"idempotency_key":`+mustJSON(t, tc.key))
			})
		}
	})

	notFound := `{"status":"error","reason":"transfer not found"}`
	for _, tc := range []struct{ name, path string }{
		{"unknown key", "/api/v1/transfers/missing"},
		{"key with control char", "/api/v1/transfers/a%0Ab"},
		{"key with invalid utf8", "/api/v1/transfers/a%FFb"},
		{"key of 256 chars", "/api/v1/transfers/" + strings.Repeat("k", domain.MaxIdempotencyKeyLen+1)},
		{"key of spaces", "/api/v1/transfers/%20%20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := e.do(t, http.MethodGet, tc.path, "", "")
			require.Equal(t, 404, res.code)
			require.JSONEq(t, notFound, res.body)
		})
	}

	t.Run("a rejected-by-validation request leaves no record", func(t *testing.T) {
		require.Equal(t, 400, e.post(t, `{"idempotency_key":"invalid","from_account_id":"","to_account_id":"acc_2","amount":1}`).code)
		require.Equal(t, 404, e.do(t, http.MethodGet, "/api/v1/transfers/invalid", "", "").code)
	})
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return string(b)
}

// TestGetAccount: balances, and 404 (not 422) for an unknown account.
func TestGetAccount(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, 200, e.post(t, transferBody("k", "acc_1", "acc_2", 100)).code)

	tests := []struct {
		name     string
		path     string
		wantCode int
		wantBody string
	}{
		{name: "after transfer", path: "/api/v1/accounts/acc_1", wantCode: 200, wantBody: `{"account_id":"acc_1","balance":900}`},
		{name: "receiver", path: "/api/v1/accounts/acc_2", wantCode: 200, wantBody: `{"account_id":"acc_2","balance":1100}`},
		{name: "unknown is 404, not 422", path: "/api/v1/accounts/acc_99", wantCode: 404, wantBody: `{"status":"error","reason":"account not found"}`},
		{name: "case matters", path: "/api/v1/accounts/ACC_1", wantCode: 404, wantBody: `{"status":"error","reason":"account not found"}`},
		{name: "too long", path: "/api/v1/accounts/" + strings.Repeat("a", domain.MaxAccountIDLen+1), wantCode: 404, wantBody: `{"status":"error","reason":"account not found"}`},
		{name: "control char", path: "/api/v1/accounts/a%00b", wantCode: 404, wantBody: `{"status":"error","reason":"account not found"}`},
		{name: "invalid utf8", path: "/api/v1/accounts/a%FF", wantCode: 404, wantBody: `{"status":"error","reason":"account not found"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := e.do(t, http.MethodGet, tt.path, "", "")
			require.Equal(t, tt.wantCode, res.code)
			require.JSONEq(t, tt.wantBody, res.body)
		})
	}
}

// TestRouting: service endpoints, JSON 404 for unknown paths and 405 with Allow for wrong methods.
func TestRouting(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		path      string
		wantCode  int
		wantBody  string
		wantAllow string
	}{
		{name: "root", method: "GET", path: "/", wantCode: 200, wantBody: `{"status":"ok"}`},
		{name: "healthz", method: "GET", path: "/healthz", wantCode: 200, wantBody: `{"status":"ok"}`},
		{name: "readyz", method: "GET", path: "/readyz", wantCode: 200, wantBody: `{"status":"ok"}`},

		{name: "unknown route is not served by the root handler", method: "GET", path: "/api/v1/foo", wantCode: 404, wantBody: `{"status":"error","reason":"not found"}`},
		{name: "unknown top-level route", method: "GET", path: "/foo", wantCode: 404, wantBody: `{"status":"error","reason":"not found"}`},
		{name: "post to unknown route is 404 too", method: "POST", path: "/foo", wantCode: 404, wantBody: `{"status":"error","reason":"not found"}`},
		{name: "transfers collection has no GET", method: "GET", path: "/api/v1/transfers", wantCode: 405, wantBody: `{"status":"error","reason":"method not allowed"}`, wantAllow: "POST"},
		{name: "transfers collection has no PUT", method: "PUT", path: "/api/v1/transfers", wantCode: 405, wantBody: `{"status":"error","reason":"method not allowed"}`, wantAllow: "POST"},
		{name: "transfer by key has no POST", method: "POST", path: "/api/v1/transfers/k", wantCode: 405, wantBody: `{"status":"error","reason":"method not allowed"}`, wantAllow: "GET, HEAD"},
		{name: "account has no DELETE", method: "DELETE", path: "/api/v1/accounts/acc_1", wantCode: 405, wantBody: `{"status":"error","reason":"method not allowed"}`, wantAllow: "GET, HEAD"},
		{name: "root has no POST", method: "POST", path: "/", wantCode: 405, wantBody: `{"status":"error","reason":"method not allowed"}`, wantAllow: "GET, HEAD"},
		{name: "healthz has no POST", method: "POST", path: "/healthz", wantCode: 405, wantBody: `{"status":"error","reason":"method not allowed"}`, wantAllow: "GET, HEAD"},
		{name: "readyz has no POST", method: "POST", path: "/readyz", wantCode: 405, wantBody: `{"status":"error","reason":"method not allowed"}`, wantAllow: "GET, HEAD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := newEnv(t).do(t, tt.method, tt.path, "", "")
			require.Equal(t, tt.wantCode, res.code)
			require.Equal(t, "application/json", res.header.Get("Content-Type"))
			require.JSONEq(t, tt.wantBody, res.body)
			require.Equal(t, tt.wantAllow, res.header.Get("Allow"))
			require.Equal(t, "nosniff", res.header.Get("X-Content-Type-Options"))
		})
	}

	t.Run("HEAD is served by GET routes, without a body", func(t *testing.T) {
		srv := httptest.NewServer(newEnv(t).handler)
		defer srv.Close()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodHead, srv.URL+"/healthz", http.NoBody)
		require.NoError(t, err)
		res, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer func() { require.NoError(t, res.Body.Close()) }()
		body, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		require.Equal(t, 200, res.StatusCode)
		require.Empty(t, body)
	})
}

// TestReadyz_DatabaseDown: /readyz is 503 when the database is down, with the cause only in the log.
func TestReadyz_DatabaseDown(t *testing.T) {
	e := newEnv(t)
	e.store.PingErr = errors.New("connection refused")

	res := e.do(t, http.MethodGet, "/readyz", "", "")
	require.Equal(t, 503, res.code)
	require.JSONEq(t, `{"status":"error","reason":"database unavailable"}`, res.body)
	require.NotContains(t, res.body, "connection refused")

	rec := lastLog(t, e)
	require.Equal(t, "ERROR", rec["level"])
	require.Equal(t, "database unavailable: connection refused", rec["error"])

	// liveness does not depend on the database
	require.Equal(t, 200, e.do(t, http.MethodGet, "/healthz", "", "").code)
}

// TestInternalErrorsAreHidden: a storage failure is 500 "internal error"; its details never reach the client.
func TestInternalErrorsAreHidden(t *testing.T) {
	e := newEnv(t)
	e.store.Err = errors.New("pq: relation \"transfers\" does not exist")

	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"post", "POST", "/api/v1/transfers", transferBody("k", "acc_1", "acc_2", 1)},
		{"get transfer", "GET", "/api/v1/transfers/k", ""},
		{"get account", "GET", "/api/v1/accounts/acc_1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := e.do(t, tc.method, tc.path, "application/json", tc.body)
			require.Equal(t, 500, res.code)
			require.JSONEq(t, `{"status":"error","reason":"internal error"}`, res.body)

			rec := lastLog(t, e)
			require.Equal(t, "ERROR", rec["level"])
			require.Contains(t, rec["error"], "relation", "details stay in the log")
		})
	}
}

// TestPanicIsRecovered: a panic in a handler becomes a 500 and a logged stack, not a dropped connection.
func TestPanicIsRecovered(t *testing.T) {
	e := newEnv(t)
	e.store.BeforeTransfer = func(context.Context) { panic("boom") }

	res := e.post(t, transferBody("k", "acc_1", "acc_2", 1))
	require.Equal(t, 500, res.code)
	require.JSONEq(t, `{"status":"error","reason":"internal error"}`, res.body)

	rec := lastLog(t, e)
	require.Equal(t, "ERROR", rec["level"])
	require.Equal(t, "panic: boom", rec["error"])
	require.Contains(t, rec["stack"], "goroutine")

	// the service keeps working
	e.store.BeforeTransfer = nil
	require.Equal(t, 200, e.post(t, transferBody("k2", "acc_1", "acc_2", 1)).code)
}

// TestRequestID: a sane client X-Request-ID is kept; anything else is replaced by a generated one.
func TestRequestID(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		wantSame bool
	}{
		{name: "client id is kept", header: "trace-123", wantSame: true},
		{name: "128 chars is kept", header: strings.Repeat("a", 128), wantSame: true},
		{name: "129 chars is replaced", header: strings.Repeat("a", 129)},
		{name: "control char is replaced", header: "a\tb"},
		{name: "invalid utf8 is replaced", header: "a\xffb"},
		{name: "absent is generated", header: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			var res response
			if tt.header == "" {
				res = e.do(t, http.MethodGet, "/healthz", "", "")
			} else {
				res = e.do(t, http.MethodGet, "/healthz", "", "", "X-Request-ID", tt.header)
			}
			got := res.header.Get("X-Request-ID")
			require.NotEmpty(t, got)
			if tt.wantSame {
				require.Equal(t, tt.header, got)
			} else {
				require.NotEqual(t, tt.header, got)
			}
			require.Equal(t, got, lastLog(t, e)["request_id"])
		})
	}
}

// TestAccessLog: one log record per request, with the transfer fields and the reason of a refusal.
func TestAccessLog(t *testing.T) {
	t.Run("one info record per request with transfer fields", func(t *testing.T) {
		e := newEnv(t)
		e.post(t, transferBody("log-1", "acc_1", "acc_2", 100))

		require.Equal(t, 1, strings.Count(strings.TrimSpace(e.log.String()), "\n")+1)
		rec := lastLog(t, e)
		require.Equal(t, "INFO", rec["level"])
		require.Equal(t, "POST", rec["method"])
		require.Equal(t, "/api/v1/transfers", rec["path"])
		require.EqualValues(t, 200, rec["status"])
		require.Contains(t, rec, "duration_ms")
		require.Equal(t, "log-1", rec["idempotency_key"])
		require.Equal(t, "acc_1", rec["from_account_id"])
		require.Equal(t, "acc_2", rec["to_account_id"])
		require.EqualValues(t, 100, rec["amount"])
		require.NotContains(t, rec, "body", "raw body is not logged")
	})

	t.Run("business rejection is info with a reason", func(t *testing.T) {
		e := newEnv(t)
		e.post(t, transferBody("log-2", "acc_1", "acc_2", 9999))
		rec := lastLog(t, e)
		require.Equal(t, "INFO", rec["level"])
		require.EqualValues(t, 422, rec["status"])
		require.Equal(t, "insufficient funds", rec["reason"])
	})

	t.Run("client errors are info", func(t *testing.T) {
		e := newEnv(t)
		e.post(t, `{`)
		rec := lastLog(t, e)
		require.Equal(t, "INFO", rec["level"])
		require.EqualValues(t, 400, rec["status"])
		require.Equal(t, "invalid json", rec["reason"])
	})
}

// TestRequestTimeoutReachesTheStore: the request timeout bounds the storage call.
func TestRequestTimeoutReachesTheStore(t *testing.T) {
	store := memstore.Seeded()
	var deadline time.Time
	var hasDeadline bool
	store.BeforeTransfer = func(ctx context.Context) { deadline, hasDeadline = ctx.Deadline() }
	h := httpapi.NewHandler(httpapi.Deps{
		App:    app.New(store, store),
		Pinger: store,
	}, httpapi.Options{RequestTimeout: 3 * time.Second, Logger: slog.New(slog.DiscardHandler)})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/transfers", strings.NewReader(transferBody("k", "acc_1", "acc_2", 1)))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.True(t, hasDeadline)
	require.WithinDuration(t, time.Now().Add(3*time.Second), deadline, time.Second)
}

func lastLog(t *testing.T, e *env) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(e.log.String()), "\n")
	require.NotEmpty(t, lines)
	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &rec))
	return rec
}

// Probes are logged at debug so that they do not bury real requests; the
// default (info) level hides them, and a failing probe is still an error.
func TestProbeLogLevel(t *testing.T) {
	e := newEnv(t)
	e.do(t, http.MethodGet, "/healthz", "", "")
	require.Equal(t, "DEBUG", lastLog(t, e)["level"])
	e.do(t, http.MethodGet, "/readyz", "", "")
	require.Equal(t, "DEBUG", lastLog(t, e)["level"])

	e.store.PingErr = errors.New("down")
	e.do(t, http.MethodGet, "/readyz", "", "")
	require.Equal(t, "ERROR", lastLog(t, e)["level"])

	e.do(t, http.MethodGet, "/api/v1/accounts/acc_1", "", "")
	require.Equal(t, "INFO", lastLog(t, e)["level"], "ordinary requests stay at info")
}

// TestDocs: Swagger UI and the embedded OpenAPI file at /docs, errors in the common format, no routes without a spec.
func TestDocs(t *testing.T) {
	store := memstore.Seeded()
	h := httpapi.NewHandler(httpapi.Deps{App: app.New(store, store), Pinger: store}, httpapi.Options{
		RequestTimeout: 5 * time.Second,
		Logger:         slog.New(slog.DiscardHandler),
		OpenAPISpec:    apidocs.OpenAPI,
	})
	e := &env{store: store, handler: h, log: &bytes.Buffer{}}

	tests := []struct {
		name            string
		method, path    string
		wantCode        int
		wantContentType string
		wantContains    []string
		wantAllow       string
	}{
		{name: "swagger ui", method: http.MethodGet, path: "/docs", wantCode: 200, wantContentType: "text/html; charset=utf-8",
			wantContains: []string{"swagger-ui-dist@", "SwaggerUIBundle", `"/docs/openapi.yaml"`}},
		{name: "openapi file", method: http.MethodGet, path: "/docs/openapi.yaml", wantCode: 200, wantContentType: "application/yaml",
			wantContains: []string{"openapi: 3.0.3", "/api/v1/transfers"}},
		{name: "no POST", method: http.MethodPost, path: "/docs", wantCode: 405, wantContentType: "application/json",
			wantContains: []string{`"reason":"method not allowed"`}, wantAllow: "GET, HEAD"},
		{name: "nothing below /docs", method: http.MethodGet, path: "/docs/other", wantCode: 404, wantContentType: "application/json",
			wantContains: []string{`"reason":"not found"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := e.do(t, tt.method, tt.path, "", "")
			require.Equal(t, tt.wantCode, res.code, res.body)
			require.Equal(t, tt.wantContentType, res.header.Get("Content-Type"))
			require.Equal(t, "nosniff", res.header.Get("X-Content-Type-Options"))
			for _, s := range tt.wantContains {
				require.Contains(t, res.body, s)
			}
			if tt.wantAllow != "" {
				require.Equal(t, tt.wantAllow, res.header.Get("Allow"))
			}
		})
	}

	t.Run("the file is served as embedded", func(t *testing.T) {
		require.Equal(t, string(apidocs.OpenAPI), e.do(t, http.MethodGet, "/docs/openapi.yaml", "", "").body)
	})
	t.Run("no spec, no docs routes", func(t *testing.T) {
		require.Equal(t, http.StatusNotFound, newEnv(t).do(t, http.MethodGet, "/docs", "", "").code)
	})
}
