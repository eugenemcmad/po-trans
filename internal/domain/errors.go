// Package domain holds the entities, business rules and errors of the transfer
// service. It depends on neither HTTP nor the database.
package domain

import "errors"

// RejectReason is the stored and published reason of a rejected transfer.
type RejectReason string

// The only three rejection reasons.
const (
	ReasonInsufficientFunds RejectReason = "insufficient funds"
	ReasonSelfTransfer      RejectReason = "self-transfer not allowed"
	ReasonAccountNotFound   RejectReason = "account not found"
)

// Business errors. The first three are persisted rejections: each has exactly
// one RejectReason (see rejections).
var (
	ErrInsufficientFunds   = errors.New(string(ReasonInsufficientFunds))
	ErrSelfTransfer        = errors.New(string(ReasonSelfTransfer))
	ErrAccountNotFound     = errors.New(string(ReasonAccountNotFound))
	ErrIdempotencyConflict = errors.New("idempotency key reused with different parameters")
	ErrTransferNotFound    = errors.New("transfer not found")
)

// Kinds of validation errors; they are wrapped into *ValidationError.
var (
	ErrRequired          = errors.New("is required")
	ErrNotString         = errors.New("must be a string")
	ErrTooLong           = errors.New("is too long")
	ErrInvalidCharacters = errors.New("contains invalid characters")
	ErrInvalidAmount     = errors.New("must be a positive integer")
)

// ValidationError ties a validation kind to the request field it concerns.
type ValidationError struct {
	Field string
	Err   error
}

func (e *ValidationError) Error() string { return e.Field + " " + e.Err.Error() }

func (e *ValidationError) Unwrap() error { return e.Err }

// rejections is the one place that pairs a reason with its error; a new
// rejection is added here and nowhere else in the domain.
var rejections = [...]struct {
	reason RejectReason
	err    error
}{
	{ReasonInsufficientFunds, ErrInsufficientFunds},
	{ReasonSelfTransfer, ErrSelfTransfer},
	{ReasonAccountNotFound, ErrAccountNotFound},
}

// Err returns the sentinel error matching the reason (nil for an unknown reason).
func (r RejectReason) Err() error {
	for _, p := range rejections {
		if p.reason == r {
			return p.err
		}
	}
	return nil
}

// RejectReasonOf reports the rejection reason carried by err, if it is one of
// the business rejections.
func RejectReasonOf(err error) (RejectReason, bool) {
	for _, p := range rejections {
		if errors.Is(err, p.err) {
			return p.reason, true
		}
	}
	return "", false
}
