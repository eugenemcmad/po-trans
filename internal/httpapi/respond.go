package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// responder writes JSON responses. A failure to write cannot be reported to the
// client any more, so it is logged with the injected logger.
type responder struct{ logger *slog.Logger }

func (p responder) writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		p.logger.Error("marshal response", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	p.write(w, status, "application/json", append(body, '\n'))
}

// write sends a ready body with the given status and content type.
func (p responder) write(w http.ResponseWriter, status int, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		p.logger.Warn("write response", "error", err)
	}
}

// statusWriter remembers the status code and whether the header was sent.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// routeErrorWriter replaces the plain-text 404 and 405 answers of ServeMux with
// the API error format, keeping headers such as Allow.
type routeErrorWriter struct {
	http.ResponseWriter
	respond  responder
	replaced bool
}

func (w *routeErrorWriter) WriteHeader(code int) {
	isMuxError := (code == http.StatusNotFound || code == http.StatusMethodNotAllowed) &&
		strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain")
	if !isMuxError {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.replaced = true
	reason := reasonNotFound
	if code == http.StatusMethodNotAllowed {
		reason = reasonMethodNotAllowed
	}
	w.respond.writeJSON(w.ResponseWriter, code, errorBody{Status: statusError, Reason: reason})
}

func (w *routeErrorWriter) Write(b []byte) (int, error) {
	if w.replaced {
		return len(b), nil // drop the plain-text body of ServeMux
	}
	return w.ResponseWriter.Write(b)
}

func (w *routeErrorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
