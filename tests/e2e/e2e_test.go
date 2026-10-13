//go:build e2e

package e2e

import (
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Tests are sequential and compare balances before and after, so they do not
// depend on the order in which they run.

// E1: the service answers at the root and on both probes.
func TestE1_ServiceEndpoints(t *testing.T) {
	for _, path := range []string{"/", "/healthz", "/readyz"} {
		t.Run(path, func(t *testing.T) {
			r := call(t, http.MethodGet, path, "", "")
			require.Equal(t, http.StatusOK, r.Code)
			require.JSONEq(t, `{"status":"ok"}`, r.Body)
		})
	}
}

// E3: a transfer moves exactly its amount, and the answer has exactly the contract fields.
func TestE3_Transfer(t *testing.T) {
	from0, to0 := balance(t, "acc_3"), balance(t, "acc_4")
	key := uuid.NewString()

	r := callJSON(t, http.MethodPost, "/api/v1/transfers", transferJSON(key, "acc_3", "acc_4", 100))

	require.Equal(t, http.StatusOK, r.Code, r.Body)
	m := r.json(t)
	require.ElementsMatch(t, []string{"transfer_id", "status", "from_account_id", "to_account_id", "amount"}, mapKeys(m))
	require.Equal(t, "completed", m["status"])
	require.Equal(t, "acc_3", m["from_account_id"])
	require.Equal(t, "acc_4", m["to_account_id"])
	require.EqualValues(t, 100, m["amount"])
	_, err := uuid.Parse(m["transfer_id"].(string))
	require.NoError(t, err)
	require.Equal(t, from0-100, balance(t, "acc_3"))
	require.Equal(t, to0+100, balance(t, "acc_4"))
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// E4: rule 2 over HTTP — a repeat gives the same answer and moves no money; GET shows the transfer.
func TestE4_RepeatAndGet(t *testing.T) {
	from0 := balance(t, "acc_5")
	key := uuid.NewString()
	body := transferJSON(key, "acc_5", "acc_6", 50)

	first := callJSON(t, http.MethodPost, "/api/v1/transfers", body)
	second := callJSON(t, http.MethodPost, "/api/v1/transfers", body)

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, first.Code, second.Code)
	require.Equal(t, first.Body, second.Body)
	require.Equal(t, from0-50, balance(t, "acc_5"), "applied once")

	got := callJSON(t, http.MethodGet, "/api/v1/transfers/"+url.PathEscape(key), "")
	require.Equal(t, http.StatusOK, got.Code, got.Body)
	m := got.json(t)
	require.Equal(t, "completed", m["status"])
	require.Equal(t, key, m["idempotency_key"])
	require.Equal(t, first.json(t)["transfer_id"], m["transfer_id"])
	require.NotContains(t, m, "reason")
	require.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`, m["created_at"])
}

// E5: the three business rejections (rules 1, 3, 4) with their exact bodies.
func TestE5_Rejections(t *testing.T) {
	tests := []struct {
		name       string
		from, to   string
		amount     int64
		wantReason string
	}{
		{"insufficient funds", "acc_7", "acc_8", 1_000_000, "insufficient funds"},
		{"self transfer", "acc_7", "acc_7", 1, "self-transfer not allowed"},
		{"unknown account", "acc_7", "acc_99", 1, "account not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := uuid.NewString()
			body := transferJSON(key, tt.from, tt.to, tt.amount)
			want := fmt.Sprintf(`{"status":"rejected","reason":%q}`, tt.wantReason)

			first := callJSON(t, http.MethodPost, "/api/v1/transfers", body)
			require.Equal(t, http.StatusUnprocessableEntity, first.Code)
			require.JSONEq(t, want, first.Body)

			repeat := callJSON(t, http.MethodPost, "/api/v1/transfers", body)
			require.Equal(t, first.Code, repeat.Code)
			require.JSONEq(t, want, repeat.Body)

			stored := callJSON(t, http.MethodGet, "/api/v1/transfers/"+url.PathEscape(key), "")
			require.Equal(t, http.StatusOK, stored.Code, stored.Body)
			require.Equal(t, "rejected", stored.json(t)["status"])
			require.Equal(t, tt.wantReason, stored.json(t)["reason"])
		})
	}
}

// E6: the same key with other data is 409 (rule 2).
func TestE6_Conflict(t *testing.T) {
	key := uuid.NewString()
	require.Equal(t, http.StatusOK, callJSON(t, http.MethodPost, "/api/v1/transfers", transferJSON(key, "acc_9", "acc_10", 10)).Code)

	r := callJSON(t, http.MethodPost, "/api/v1/transfers", transferJSON(key, "acc_9", "acc_10", 11))

	require.Equal(t, http.StatusConflict, r.Code)
	require.JSONEq(t, `{"status":"error","reason":"idempotency key reused with different parameters"}`, r.Body)
}

// E7: validation errors are 400 with exact reasons (rule 5).
func TestE7_BadRequests(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantReason  string
	}{
		{"broken json", "application/json", `{"idempotency_key":`, "invalid json"},
		{"missing field", "application/json", `{"idempotency_key":"k","to_account_id":"acc_2","amount":1}`, "from_account_id is required"},
		{"negative amount", "application/json", `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":-1}`, "amount must be a positive integer"},
		{"wrong content type", "text/plain", `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":1}`, "unsupported content type"},
		{"no content type", "", `{"idempotency_key":"k","from_account_id":"acc_1","to_account_id":"acc_2","amount":1}`, "unsupported content type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := call(t, http.MethodPost, "/api/v1/transfers", tt.contentType, tt.body)
			require.Equal(t, http.StatusBadRequest, r.Code)
			require.JSONEq(t, fmt.Sprintf(`{"status":"error","reason":%q}`, tt.wantReason), r.Body)
		})
	}
}

// E8: 404 for unknown accounts, transfers and paths; 405 with Allow.
func TestE8_NotFoundAndMethods(t *testing.T) {
	t.Run("unknown account", func(t *testing.T) {
		r := call(t, http.MethodGet, "/api/v1/accounts/acc_99", "", "")
		require.Equal(t, http.StatusNotFound, r.Code)
		require.JSONEq(t, `{"status":"error","reason":"account not found"}`, r.Body)
	})
	t.Run("unknown transfer", func(t *testing.T) {
		r := call(t, http.MethodGet, "/api/v1/transfers/"+uuid.NewString(), "", "")
		require.Equal(t, http.StatusNotFound, r.Code)
		require.JSONEq(t, `{"status":"error","reason":"transfer not found"}`, r.Body)
	})
	t.Run("unknown route is not answered by the root handler", func(t *testing.T) {
		r := call(t, http.MethodGet, "/api/v1/foo", "", "")
		require.Equal(t, http.StatusNotFound, r.Code)
		require.JSONEq(t, `{"status":"error","reason":"not found"}`, r.Body)
	})
	for _, tt := range []struct{ method, path, allow string }{
		{http.MethodGet, "/api/v1/transfers", "POST"},
		{http.MethodPost, "/api/v1/transfers/some-key", "GET, HEAD"},
		{http.MethodDelete, "/api/v1/accounts/acc_1", "GET, HEAD"},
		{http.MethodPost, "/readyz", "GET, HEAD"},
	} {
		t.Run("405 "+tt.method+" "+tt.path, func(t *testing.T) {
			r := call(t, tt.method, tt.path, "", "")
			require.Equal(t, http.StatusMethodNotAllowed, r.Code)
			require.JSONEq(t, `{"status":"error","reason":"method not allowed"}`, r.Body)
			require.Equal(t, tt.allow, r.Header.Get("Allow"))
		})
	}
}

// E9: parallel load keeps the books balanced.
func TestE9_ParallelLoad(t *testing.T) {
	const total, inFlight, seed = 200, 50, 20261012
	rnd := rand.New(rand.NewSource(seed)) //nolint:gosec // fixed seed: the run must be reproducible
	bodies := make([]string, total)
	for i := range bodies {
		bodies[i] = transferJSON(uuid.NewString(),
			fmt.Sprintf("acc_%d", rnd.Intn(10)+1), fmt.Sprintf("acc_%d", rnd.Intn(10)+1), int64(rnd.Intn(500)+1))
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("random seed: %d", seed)
		}
	})

	replies := make([]reply, total)
	errs := make([]error, total)
	sem := make(chan struct{}, inFlight)
	var wg sync.WaitGroup
	for i := range bodies {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			replies[i], errs[i] = send(t.Context(), http.MethodPost, "/api/v1/transfers", "application/json", bodies[i])
		})
	}
	wg.Wait()
	require.NoError(t, errors.Join(errs...))

	for i, r := range replies {
		checkReply(t, http.MethodPost, "/api/v1/transfers", r)
		require.Contains(t, []int{http.StatusOK, http.StatusUnprocessableEntity}, r.Code, "request %d: %s", i, bodies[i])
	}
	var sum int64
	for i := 1; i <= 10; i++ {
		b := balance(t, fmt.Sprintf("acc_%d", i))
		require.GreaterOrEqual(t, b, int64(0))
		sum += b
	}
	require.EqualValues(t, 10000, sum)
}

// E10: any allowed key can be fetched back, encoded as one path segment.
func TestE10_AwkwardKeys(t *testing.T) {
	tests := []struct {
		name, key, escaped string
	}{
		{"slash", "a/b", "a%2Fb"},
		{"slash and percent and space", "x/y z%", "x%2Fy%20z%25"},
		{"dot dot", "..", "%2E%2E"},
		{"dot", ".", "%2E"},
		{"non-ascii", "ключ-1", "%D0%BA%D0%BB%D1%8E%D1%87-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The same body again under the same key is a repeat, so the test can run twice.
			post := callJSON(t, http.MethodPost, "/api/v1/transfers", transferJSON(tt.key, "acc_1", "acc_2", 1))
			require.Equal(t, http.StatusOK, post.Code, post.Body)

			got := callJSON(t, http.MethodGet, "/api/v1/transfers/"+tt.escaped, "")
			require.Equal(t, http.StatusOK, got.Code, got.Body)
			require.Equal(t, tt.key, got.json(t)["idempotency_key"])
			require.Equal(t, post.json(t)["transfer_id"], got.json(t)["transfer_id"])
		})
	}
}
