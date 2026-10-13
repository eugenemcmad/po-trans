package httpapi

import (
	"log/slog"
	"net/http"
	"time"
)

// Paths of the liveness and readiness probes; the access log treats them apart.
const (
	pathHealthz = "/healthz"
	pathReadyz  = "/readyz"
)

// Options configure the handler chain.
type Options struct {
	// RequestTimeout bounds each request, counted from entering the handler.
	RequestTimeout time.Duration
	// Logger receives one record per request and every error the transport
	// cannot report to the client; nil means slog.Default().
	Logger *slog.Logger
	// OpenAPISpec is served at /docs/openapi.yaml and shown by Swagger UI at
	// /docs; empty means no /docs routes.
	OpenAPISpec []byte
}

// NewHandler builds the router and middleware chain:
// request id → access log → panic recovery → timeout → JSON route errors → ServeMux.
func NewHandler(deps Deps, opts Options) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	respond := responder{logger: logger}

	h := handlers{Deps: deps, respond: respond}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/transfers", h.createTransfer)
	mux.HandleFunc("GET /api/v1/transfers/{idempotency_key}", h.getTransfer)
	mux.HandleFunc("GET /api/v1/accounts/{id}", h.getAccount)
	mux.HandleFunc("GET /{$}", h.ok)
	mux.HandleFunc("GET "+pathHealthz, h.ok)
	mux.HandleFunc("GET "+pathReadyz, h.ready)
	if len(opts.OpenAPISpec) > 0 {
		d := docsHandlers{spec: opts.OpenAPISpec, respond: respond}
		mux.HandleFunc("GET "+pathDocs, d.page)
		mux.HandleFunc("GET "+pathDocsSpec, d.openAPI)
	}

	return withRequestID(accessLog(logger, recoverPanic(respond, withTimeout(opts.RequestTimeout, jsonRouteErrors(respond, mux)))))
}
