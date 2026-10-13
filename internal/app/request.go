package app

import "po-trans/internal/domain"

// FieldKind classifies what was supplied for a string field.
type FieldKind int

const (
	FieldMissing   FieldKind = iota // absent or null
	FieldString                     // a string
	FieldWrongType                  // supplied, but not as a string
)

// StringField is a string field as received.
type StringField struct {
	Kind  FieldKind
	Value string
}

// AmountField is the amount as received: Valid is true only for a JSON integer
// literal that fits int64.
type AmountField struct {
	Valid bool
	Value int64
}

// TransferRequest is a transfer request as received, before any validation.
// It keeps the kind of every field so that a value of the wrong type can still
// be compared with a stored transfer: under a used idempotency key any
// difference is a conflict (409), not a validation error (400).
type TransferRequest struct {
	IdempotencyKey StringField
	FromAccountID  StringField
	ToAccountID    StringField
	Amount         AmountField
}

// ValidateKey validates only the idempotency key, the one field without which
// nothing can be looked up.
func (r TransferRequest) ValidateKey() error {
	return r.IdempotencyKey.validate(domain.FieldIdempotencyKey, domain.ValidateIdempotencyKey)
}

// Command validates all fields in contract order and returns the first error.
func (r TransferRequest) Command() (domain.TransferCommand, error) {
	if err := r.ValidateKey(); err != nil {
		return domain.TransferCommand{}, err
	}
	validateAccount := func(field string) func(string) error {
		return func(v string) error { return domain.ValidateAccountID(field, v) }
	}
	if err := r.FromAccountID.validate(domain.FieldFromAccountID, validateAccount(domain.FieldFromAccountID)); err != nil {
		return domain.TransferCommand{}, err
	}
	if err := r.ToAccountID.validate(domain.FieldToAccountID, validateAccount(domain.FieldToAccountID)); err != nil {
		return domain.TransferCommand{}, err
	}
	if !r.Amount.Valid {
		return domain.TransferCommand{}, &domain.ValidationError{Field: domain.FieldAmount, Err: domain.ErrInvalidAmount}
	}
	if err := domain.ValidateAmount(r.Amount.Value); err != nil {
		return domain.TransferCommand{}, err
	}
	return domain.TransferCommand{
		IdempotencyKey: r.IdempotencyKey.Value,
		FromAccountID:  r.FromAccountID.Value,
		ToAccountID:    r.ToAccountID.Value,
		Amount:         r.Amount.Value,
	}, nil
}

// validate applies the type check (required → string) and then the domain rule.
func (f StringField) validate(field string, rule func(string) error) error {
	switch f.Kind {
	case FieldMissing:
		return &domain.ValidationError{Field: field, Err: domain.ErrRequired}
	case FieldString:
		return rule(f.Value)
	default:
		return &domain.ValidationError{Field: field, Err: domain.ErrNotString}
	}
}
