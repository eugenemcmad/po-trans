package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"po-trans/internal/domain"
)

// The 400 reasons are API strings owned by the transport (spec 0.8.4): every
// validation kind maps to a fixed text, and a kind without a text is an
// internal error rather than a leaked domain message.
func TestClassify_ValidationReasons(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantReason string
	}{
		{"required", &domain.ValidationError{Field: domain.FieldIdempotencyKey, Err: domain.ErrRequired}, http.StatusBadRequest, "idempotency_key is required"},
		{"not a string", &domain.ValidationError{Field: domain.FieldFromAccountID, Err: domain.ErrNotString}, http.StatusBadRequest, "from_account_id must be a string"},
		{"too long", &domain.ValidationError{Field: domain.FieldToAccountID, Err: domain.ErrTooLong}, http.StatusBadRequest, "to_account_id is too long"},
		{"invalid characters", &domain.ValidationError{Field: domain.FieldToAccountID, Err: domain.ErrInvalidCharacters}, http.StatusBadRequest, "to_account_id contains invalid characters"},
		{"invalid amount", &domain.ValidationError{Field: domain.FieldAmount, Err: domain.ErrInvalidAmount}, http.StatusBadRequest, "amount must be a positive integer"},
		{"wrapped", fmt.Errorf("context: %w", &domain.ValidationError{Field: domain.FieldAmount, Err: domain.ErrInvalidAmount}), http.StatusBadRequest, "amount must be a positive integer"},
		{"unknown kind", &domain.ValidationError{Field: domain.FieldAmount, Err: errors.New("some new kind")}, http.StatusInternalServerError, reasonInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := classify(endpointTransfer, tt.err)
			require.Equal(t, tt.wantStatus, status)
			require.Equal(t, tt.wantReason, body.Reason)
		})
	}
}
