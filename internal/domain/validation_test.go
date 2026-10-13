package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidPathValues: which keys and ids taken from a URL can exist at all; the others are 404 without a database call.
func TestValidPathValues(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantKey bool
		wantAcc bool
	}{
		{name: "plain", value: "acc_1", wantKey: true, wantAcc: true},
		{name: "empty", value: ""},
		{name: "spaces only", value: "  "},
		{name: "dots", value: "..", wantKey: true, wantAcc: true},
		{name: "slash and percent", value: "a/b%c", wantKey: true, wantAcc: true},
		{name: "invalid utf8", value: "a\xffb"},
		{name: "control", value: "a\x01"},
		{name: "65 runes: ok for key, too long for account", value: strings.Repeat("a", MaxAccountIDLen+1), wantKey: true},
		{name: "256 runes", value: strings.Repeat("a", MaxIdempotencyKeyLen+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.wantKey, ValidIdempotencyKey(tt.value), "key")
			require.Equal(t, tt.wantAcc, ValidAccountID(tt.value), "account id")
		})
	}
}

// TestValidators: the value rules for keys, account ids and amounts — lengths in characters, blank strings, control characters, positive amounts.
func TestValidators(t *testing.T) {
	t.Run("idempotency key", func(t *testing.T) {
		tests := []struct {
			name    string
			value   string
			wantErr error
		}{
			{"ok", "k", nil},
			{"255 characters", strings.Repeat("я", MaxIdempotencyKeyLen), nil},
			{"256 characters", strings.Repeat("я", MaxIdempotencyKeyLen+1), ErrTooLong},
			{"empty", "", ErrRequired},
			{"tabs only", "\t\t", ErrRequired},
			{"control character", "a\x00", ErrInvalidCharacters},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := ValidateIdempotencyKey(tt.value)
				if tt.wantErr == nil {
					require.NoError(t, err)
					return
				}
				require.ErrorIs(t, err, tt.wantErr)
				var ve *ValidationError
				require.ErrorAs(t, err, &ve)
				require.Equal(t, FieldIdempotencyKey, ve.Field)
			})
		}
	})

	t.Run("account id reports the field it was asked for", func(t *testing.T) {
		err := ValidateAccountID(FieldToAccountID, "")
		var ve *ValidationError
		require.ErrorAs(t, err, &ve)
		require.Equal(t, FieldToAccountID, ve.Field)
		require.ErrorIs(t, err, ErrRequired)
		require.ErrorIs(t, ValidateAccountID(FieldFromAccountID, strings.Repeat("a", MaxAccountIDLen+1)), ErrTooLong)
		require.NoError(t, ValidateAccountID(FieldFromAccountID, strings.Repeat("a", MaxAccountIDLen)))
	})

	t.Run("amount", func(t *testing.T) {
		require.NoError(t, ValidateAmount(1))
		require.NoError(t, ValidateAmount(1<<63-1))
		require.ErrorIs(t, ValidateAmount(0), ErrInvalidAmount)
		require.ErrorIs(t, ValidateAmount(-1), ErrInvalidAmount)
	})
}
