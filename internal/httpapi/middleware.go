package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"po-trans/internal/domain"
)

const (
	requestIDHeader = "X-Request-ID"
	maxRequestIDLen = 128
)

type ctxKey struct{}

// requestInfo collects what the handler learned about the request, for the
// single access-log record.
type requestInfo struct {
	attrs []slog.Attr
	err   error
	stack string // set only for a recovered panic
}

func infoFrom(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(ctxKey{}).(*requestInfo)
	if info == nil {
		return &requestInfo{} // outside the middleware chain (never in production)
	}
	return info
}

// withRequestID gives every response an X-Request-ID: the client's own when it
// is sane, otherwise a generated one.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(requestIDHeader, sanitizeRequestID(r.Header.Get(requestIDHeader)))
		next.ServeHTTP(w, r)
	})
}

// sanitizeRequestID keeps a client-provided id when it is a sane string, else
// makes a new one.
func sanitizeRequestID(client string) string {
	if client != "" && utf8.ValidString(client) && utf8.RuneCountInString(client) <= maxRequestIDLen &&
		!domain.ContainsControl(client) {
		return client
	}
	return uuid.NewString()
}

// isProbe reports whether the path is a liveness or readiness probe.
func isProbe(path string) bool { return path == pathHealthz || path == pathReadyz }

// accessLog writes exactly one record per request, after the handler is done.
// It sits inside withRequestID (the id is read from the response header) and
// outside recoverPanic, so that a recovered panic is logged as a 500. Probes
// are logged at debug level so that they do not bury real requests; their
// failures (5xx) are still errors.
func accessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info := &requestInfo{}
		w := &statusWriter{ResponseWriter: rw, status: http.StatusOK}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, info)))

		attrs := []slog.Attr{
			slog.String("request_id", rw.Header().Get(requestIDHeader)),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", w.status),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		}
		attrs = append(attrs, info.attrs...)
		level := slog.LevelInfo
		switch {
		case w.status >= http.StatusInternalServerError:
			level = slog.LevelError
			if info.err != nil {
				attrs = append(attrs, slog.String("error", info.err.Error()))
			}
			if info.stack != "" {
				attrs = append(attrs, slog.String("stack", info.stack))
			}
		case isProbe(r.URL.Path):
			level = slog.LevelDebug
		}
		logger.LogAttrs(r.Context(), level, "request", attrs...)
	})
}

// recoverPanic turns a panic in a handler into a 500 and leaves the details
// (error and stack) for the access log.
func recoverPanic(respond responder, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { //nolint:contextcheck // runs after the handler; the request context is read from r
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value, compared by identity per net/http docs
				panic(rec)
			}
			info := infoFrom(r.Context())
			info.err = fmt.Errorf("panic: %v", rec)
			info.stack = string(debug.Stack())
			if sw, ok := w.(*statusWriter); !ok || !sw.wrote {
				respond.writeJSON(w, http.StatusInternalServerError, errorBody{Status: statusError, Reason: reasonInternal})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withTimeout bounds the whole handler, including reading the body.
func withTimeout(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// jsonRouteErrors answers the 404 and 405 of ServeMux in the API error format.
func jsonRouteErrors(respond responder, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&routeErrorWriter{ResponseWriter: w, respond: respond}, r)
	})
}
