package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"unicode/utf8"

	"po-trans/internal/app"
	"po-trans/internal/domain"
)

// maxBodyBytes is the request body limit (64 KiB).
const maxBodyBytes = 64 << 10

// parseTransferRequest performs the transport checks in the order of the spec
// (content type, size, UTF-8, JSON object) and returns the raw fields. Field
// types are classified, never rejected here: a wrong type under an existing
// idempotency key must end up as 409, not 400.
func parseTransferRequest(w http.ResponseWriter, r *http.Request) (app.TransferRequest, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return app.TransferRequest{}, errUnsupportedContentType
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return app.TransferRequest{}, errBodyTooLarge
		}
		return app.TransferRequest{}, errInvalidJSON
	}
	if !utf8.Valid(body) {
		return app.TransferRequest{}, errInvalidJSON
	}

	// Field names are matched exactly (a map, not a struct, which encoding/json
	// would match case-insensitively); unknown fields are ignored; for a
	// duplicated name the last value wins.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return app.TransferRequest{}, errInvalidJSON
	}

	var req app.TransferRequest
	if req.IdempotencyKey, err = stringField(fields[domain.FieldIdempotencyKey]); err != nil {
		return app.TransferRequest{}, err
	}
	if req.FromAccountID, err = stringField(fields[domain.FieldFromAccountID]); err != nil {
		return app.TransferRequest{}, err
	}
	if req.ToAccountID, err = stringField(fields[domain.FieldToAccountID]); err != nil {
		return app.TransferRequest{}, err
	}
	req.Amount = amountField(fields[domain.FieldAmount])
	return req, nil
}

// stringField classifies a raw JSON value: absent or null, a string, or other.
func stringField(raw json.RawMessage) (app.StringField, error) {
	switch {
	case len(raw) == 0 || string(raw) == "null":
		return app.StringField{Kind: app.FieldMissing}, nil
	case raw[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return app.StringField{}, errInvalidJSON
		}
		return app.StringField{Kind: app.FieldString, Value: s}, nil
	default:
		return app.StringField{Kind: app.FieldWrongType}, nil
	}
}

// amountField accepts only an integer literal (no fraction, no exponent) that
// fits int64; anything else is "not valid".
func amountField(raw json.RawMessage) app.AmountField {
	if len(raw) == 0 {
		return app.AmountField{}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return app.AmountField{}
	}
	n, ok := v.(json.Number)
	if !ok {
		return app.AmountField{}
	}
	value, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil {
		return app.AmountField{}
	}
	return app.AmountField{Valid: true, Value: value}
}
