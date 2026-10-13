package domain

import (
	"unicode"
	"unicode/utf8"
)

// Request field names, as used in the API and in validation errors.
const (
	FieldIdempotencyKey = "idempotency_key"
	FieldFromAccountID  = "from_account_id"
	FieldToAccountID    = "to_account_id"
	FieldAmount         = "amount"
)

// Length limits in Unicode code points.
const (
	MaxIdempotencyKeyLen = 255
	MaxAccountIDLen      = 64
)

// ContainsControl reports whether s has a control character (Unicode category Cc).
func ContainsControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func isBlank(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// validateString applies required → length → characters to a string value.
// Valid UTF-8 is not checked here: it is guaranteed by whoever decoded the input.
func validateString(field, value string, maxLen int) error {
	switch {
	case isBlank(value):
		return &ValidationError{Field: field, Err: ErrRequired}
	case utf8.RuneCountInString(value) > maxLen:
		return &ValidationError{Field: field, Err: ErrTooLong}
	case ContainsControl(value):
		return &ValidationError{Field: field, Err: ErrInvalidCharacters}
	}
	return nil
}

// ValidateIdempotencyKey checks the value of an idempotency key.
func ValidateIdempotencyKey(value string) error {
	return validateString(FieldIdempotencyKey, value, MaxIdempotencyKeyLen)
}

// ValidateAccountID checks the value of an account id; field names the request
// field (FieldFromAccountID or FieldToAccountID) for the error.
func ValidateAccountID(field, value string) error {
	return validateString(field, value, MaxAccountIDLen)
}

// ValidateAmount checks that the amount is a positive number of coins.
func ValidateAmount(value int64) error {
	if value <= 0 {
		return &ValidationError{Field: FieldAmount, Err: ErrInvalidAmount}
	}
	return nil
}

// ValidIdempotencyKey reports whether s can be a stored idempotency key.
func ValidIdempotencyKey(s string) bool {
	return utf8.ValidString(s) && ValidateIdempotencyKey(s) == nil
}

// ValidAccountID reports whether s can be a stored account id.
func ValidAccountID(s string) bool {
	return utf8.ValidString(s) && ValidateAccountID(FieldFromAccountID, s) == nil
}
