//go:build e2e

// Package e2e tests the running service as a black box over HTTP. It does not
// import any internal package. Run it through `make e2e`, which starts a fresh
// environment; the tests assume the demo state (acc_1 … acc_10 with 1000 each).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

const (
	readyTimeout = 60 * time.Second
	specPath     = "../../docs/openapi.yaml"
	specServer   = "http://localhost:3000" // the server of the spec; used only to find routes
)

var (
	baseURL   string
	transport = &http.Transport{MaxIdleConnsPerHost: 64}
	client    = &http.Client{Transport: transport, Timeout: 30 * time.Second}
	router    routers.Router
)

// TestMain waits for the service, checks the initial state (E2: ten accounts with 1000 each), runs the tests one by one and looks for leaked goroutines.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	baseURL = strings.TrimRight(os.Getenv("E2E_BASE_URL"), "/")
	if baseURL == "" {
		fmt.Fprintln(os.Stderr, "E2E_BASE_URL is not set")
		return 1
	}

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(specPath)
	if err == nil {
		err = doc.Validate(context.Background())
	}
	if err == nil {
		router, err = legacy.NewRouter(doc)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "openapi spec %s: %v\n", specPath, err)
		return 1
	}

	// 1. wait for readiness
	if err := waitReady(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// 2. E2: the environment must be in its initial state
	if err := checkInitialState(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// 3. run (sequentially: no t.Parallel in this package)
	code := m.Run()

	// 4. leaks: idle keep-alive connections are not leaks, so close them first
	transport.CloseIdleConnections()
	if err := goleak.Find(); err != nil {
		fmt.Fprintln(os.Stderr, "goroutine leak:", err)
		code = 1
	}
	return code
}

func waitReady() error {
	deadline := time.Now().Add(readyTimeout)
	var last string
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/readyz", http.NoBody)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
			last = res.Status
		} else {
			last = err.Error()
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("service not ready at %s/readyz after %s (last: %s)", baseURL, readyTimeout, last)
}

func checkInitialState() error {
	var total int64
	for i := 1; i <= 10; i++ {
		id := fmt.Sprintf("acc_%d", i)
		status, body, err := rawGet("/api/v1/accounts/" + id)
		if err != nil || status != http.StatusOK {
			return fmt.Errorf("initial state: GET %s: status %d: %w", id, status, err)
		}
		var a struct {
			Balance int64 `json:"balance"`
		}
		if err := json.Unmarshal(body, &a); err != nil {
			return fmt.Errorf("initial state: %s: %w", id, err)
		}
		if a.Balance != 1000 {
			return fmt.Errorf("initial state: %s has %d, want 1000 (e2e needs a fresh environment: make e2e)", id, a.Balance)
		}
		total += a.Balance
	}
	if total != 10000 {
		return fmt.Errorf("initial state: total %d, want 10000", total)
	}
	return nil
}

func rawGet(path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+path, http.NoBody)
	if err != nil {
		return 0, nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	return res.StatusCode, b, err
}

// reply is one HTTP exchange.
type reply struct {
	Code   int
	Header http.Header
	Body   string
}

// call sends a request and, when the spec describes the operation and status,
// validates the response against docs/openapi.yaml (extra fields included).
func call(t *testing.T, method, path, contentType, body string) reply {
	t.Helper()
	r, err := send(t.Context(), method, path, contentType, body)
	require.NoError(t, err)
	checkReply(t, method, path, r)
	return r
}

// send performs one HTTP exchange. It never touches *testing.T, so it can be
// used from any goroutine; the checks are done by checkReply on the test goroutine.
func send(ctx context.Context, method, path, contentType, body string) (reply, error) {
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, strings.NewReader(body))
	if err != nil {
		return reply{}, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := client.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return reply{}, err
	}
	return reply{Code: res.StatusCode, Header: res.Header, Body: strings.TrimSpace(string(b))}, nil
}

// checkReply validates a response against docs/openapi.yaml and the rules every
// response of the service follows.
func checkReply(t *testing.T, method, path string, r reply) {
	t.Helper()
	validateAgainstSpec(t, method, path, r)
	require.Equal(t, "application/json", r.Header.Get("Content-Type"), "every response is JSON")
	require.NotEmpty(t, r.Header.Get("X-Request-ID"))
}

func callJSON(t *testing.T, method, path, body string) reply {
	t.Helper()
	return call(t, method, path, "application/json", body)
}

func validateAgainstSpec(t *testing.T, method, path string, r reply) {
	t.Helper()
	// Keys and ids may contain characters that do not match a path template
	// ("/" encoded); the route only depends on the template, so use a plain value.
	routePath := path
	switch {
	case strings.HasPrefix(path, "/api/v1/transfers/"):
		routePath = "/api/v1/transfers/key"
	case strings.HasPrefix(path, "/api/v1/accounts/"):
		routePath = "/api/v1/accounts/id"
	}
	req, err := http.NewRequestWithContext(t.Context(), method, specServer+routePath, http.NoBody)
	require.NoError(t, err)
	route, params, err := router.FindRoute(req)
	if err != nil {
		return // not an operation of the spec (unknown path, wrong method): checked by the test itself
	}
	err = openapi3filter.ValidateResponse(t.Context(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
		Status:                 r.Code,
		Header:                 r.Header,
		Body:                   io.NopCloser(bytes.NewReader([]byte(r.Body))),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true},
	})
	require.NoError(t, err, "response %d %s %s does not match docs/openapi.yaml: %s", r.Code, method, path, r.Body)
}

func (r reply) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.Body), &m), r.Body)
	return m
}

// balance reads an account through the API.
func balance(t *testing.T, id string) int64 {
	t.Helper()
	r := call(t, http.MethodGet, "/api/v1/accounts/"+id, "", "")
	require.Equal(t, http.StatusOK, r.Code, r.Body)
	return int64(r.json(t)["balance"].(float64))
}

func transferJSON(key, from, to string, amount int64) string {
	b, err := json.Marshal(map[string]any{"idempotency_key": key, "from_account_id": from, "to_account_id": to, "amount": amount})
	if err != nil {
		panic(err)
	}
	return string(b)
}
