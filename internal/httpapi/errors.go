// Package httpapi is the HTTP transport: routing, handlers, DTOs, error mapping
// and middleware.
package httpapi

import (
	"errors"
	"net/http"

	"po-trans/internal/domain"
)

// Stable reasons of transport-level errors (3.4 of the spec). Business
// rejections use domain.RejectReason; validation reasons are "<field> <text>"
// with the text taken from validationTexts.
const (
	reasonInvalidJSON            = "invalid json"
	reasonUnsupportedContentType = "unsupported content type"
	reasonBodyTooLarge           = "request body too large"
	reasonConflict               = "idempotency key reused with different parameters"
	reasonTransferNotFound       = "transfer not found"
	reasonAccountNotFound        = "account not found"
	reasonNotFound               = "not found"
	reasonMethodNotAllowed       = "method not allowed"
	reasonInternal               = "internal error"
	reasonDatabaseUnavailable    = "database unavailable"
)

// Values of the "status" field.
const (
	statusRejected = "rejected"
	statusError    = "error"
	statusOK       = "ok"
)

// errorBody is the single error format of the API.
type errorBody struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// apiError is a transport-level error that already knows its HTTP answer.
type apiError struct {
	status int
	reason string
}

func (e *apiError) Error() string { return e.reason }

var (
	errInvalidJSON            = &apiError{status: http.StatusBadRequest, reason: reasonInvalidJSON}
	errUnsupportedContentType = &apiError{status: http.StatusBadRequest, reason: reasonUnsupportedContentType}
	errBodyTooLarge           = &apiError{status: http.StatusBadRequest, reason: reasonBodyTooLarge}
	errDatabaseUnavailable    = &apiError{status: http.StatusServiceUnavailable, reason: reasonDatabaseUnavailable}
)

// endpoint tells the mapper where an error happened: ErrAccountNotFound is a
// 422 rejection in POST /transfers but a 404 for GET /accounts/{id}.
//
// Only endpointGetAccount changes the mapping today. The other values are
// still passed by their handlers so that every call names its endpoint
// truthfully and a new endpoint-specific rule is one more case in classify,
// not a change of every caller.
type endpoint int

const (
	endpointTransfer endpoint = iota
	endpointGetTransfer
	endpointGetAccount
	endpointReady
)

// classify is the one place that maps an error to an HTTP status and body.
func classify(ep endpoint, err error) (int, errorBody) {
	var (
		ae *apiError
		ve *domain.ValidationError
	)
	switch {
	case errors.As(err, &ae):
		return ae.status, errorBody{Status: statusError, Reason: ae.reason}
	case errors.As(err, &ve):
		if reason, ok := validationReason(ve); ok {
			return http.StatusBadRequest, errorBody{Status: statusError, Reason: reason}
		}
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return http.StatusConflict, errorBody{Status: statusError, Reason: reasonConflict}
	case errors.Is(err, domain.ErrTransferNotFound):
		return http.StatusNotFound, errorBody{Status: statusError, Reason: reasonTransferNotFound}
	case errors.Is(err, domain.ErrAccountNotFound) && ep == endpointGetAccount:
		return http.StatusNotFound, errorBody{Status: statusError, Reason: reasonAccountNotFound}
	}
	if reason, ok := domain.RejectReasonOf(err); ok {
		return http.StatusUnprocessableEntity, errorBody{Status: statusRejected, Reason: string(reason)}
	}
	return http.StatusInternalServerError, errorBody{Status: statusError, Reason: reasonInternal}
}

// validationTexts are the published texts of the validation kinds. They are API
// strings owned by the transport: the messages of the domain errors never reach
// a response, so rewording one cannot change the contract.
var validationTexts = [...]struct {
	kind error
	text string
}{
	{domain.ErrRequired, "is required"},
	{domain.ErrNotString, "must be a string"},
	{domain.ErrTooLong, "is too long"},
	{domain.ErrInvalidCharacters, "contains invalid characters"},
	{domain.ErrInvalidAmount, "must be a positive integer"},
}

// validationReason renders a validation error as "<field> <text>", e.g.
// "from_account_id is required" or "amount must be a positive integer". An
// unknown kind is reported as not ok, so that it ends up as an internal error
// instead of leaking a domain message.
func validationReason(ve *domain.ValidationError) (string, bool) {
	for _, v := range validationTexts {
		if errors.Is(ve.Err, v.kind) {
			return ve.Field + " " + v.text, true
		}
	}
	return "", false
}
