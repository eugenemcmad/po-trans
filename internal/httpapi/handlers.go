package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"po-trans/internal/app"
)

// Pinger checks that the storage is reachable; it backs /readyz.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps are what the handlers need: the use cases and the readiness probe.
type Deps struct {
	App    *app.Application
	Pinger Pinger
}

type handlers struct {
	Deps
	respond responder
}

// fail answers with the mapped status and remembers the error for the log.
func (h handlers) fail(w http.ResponseWriter, r *http.Request, ep endpoint, err error) {
	status, body := classify(ep, err)
	info := infoFrom(r.Context())
	if status >= http.StatusInternalServerError {
		info.err = err
	} else {
		info.attrs = append(info.attrs, slog.String("reason", body.Reason))
	}
	h.respond.writeJSON(w, status, body)
}

func (h handlers) createTransfer(w http.ResponseWriter, r *http.Request) {
	req, err := parseTransferRequest(w, r)
	if err != nil {
		h.fail(w, r, endpointTransfer, err)
		return
	}
	annotateTransfer(r, req)

	t, err := h.App.CreateTransfer.Handle(r.Context(), req)
	if err != nil {
		h.fail(w, r, endpointTransfer, err)
		return
	}
	h.respond.writeJSON(w, http.StatusOK, newCreateTransferResponse(t))
}

// annotateTransfer adds the request fields that were parsed to the log record.
func annotateTransfer(r *http.Request, req app.TransferRequest) {
	info := infoFrom(r.Context())
	if req.IdempotencyKey.Kind == app.FieldString {
		info.attrs = append(info.attrs, slog.String("idempotency_key", req.IdempotencyKey.Value))
	}
	if req.FromAccountID.Kind == app.FieldString {
		info.attrs = append(info.attrs, slog.String("from_account_id", req.FromAccountID.Value))
	}
	if req.ToAccountID.Kind == app.FieldString {
		info.attrs = append(info.attrs, slog.String("to_account_id", req.ToAccountID.Value))
	}
	if req.Amount.Valid {
		info.attrs = append(info.attrs, slog.Int64("amount", req.Amount.Value))
	}
}

func (h handlers) getTransfer(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("idempotency_key")
	t, err := h.App.GetTransfer.Handle(r.Context(), key)
	if err != nil {
		h.fail(w, r, endpointGetTransfer, err)
		return
	}
	h.respond.writeJSON(w, http.StatusOK, newGetTransferResponse(t))
}

func (h handlers) getAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, err := h.App.GetBalance.Handle(r.Context(), id)
	if err != nil {
		h.fail(w, r, endpointGetAccount, err)
		return
	}
	h.respond.writeJSON(w, http.StatusOK, accountResponse{AccountID: a.ID, Balance: a.Balance})
}

func (h handlers) ok(w http.ResponseWriter, _ *http.Request) {
	h.respond.writeJSON(w, http.StatusOK, statusResponse{Status: statusOK})
}

func (h handlers) ready(w http.ResponseWriter, r *http.Request) {
	if err := h.Pinger.Ping(r.Context()); err != nil {
		h.fail(w, r, endpointReady, fmt.Errorf("%w: %w", errDatabaseUnavailable, err))
		return
	}
	h.respond.writeJSON(w, http.StatusOK, statusResponse{Status: statusOK})
}
