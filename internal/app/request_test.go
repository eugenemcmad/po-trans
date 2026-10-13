package app_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"po-trans/internal/app"
	"po-trans/internal/domain"
)

func str(v string) app.StringField { return app.StringField{Kind: app.FieldString, Value: v} }

func amount(v int64) app.AmountField { return app.AmountField{Valid: true, Value: v} }

func validRequest() app.TransferRequest {
	return app.TransferRequest{
		IdempotencyKey: str("key-1"),
		FromAccountID:  str("acc_1"),
		ToAccountID:    str("acc_2"),
		Amount:         amount(100),
	}
}

// TestTransferRequest_Command: field validation (rule 5 and our limits) in contract order; the first error wins; values are never normalised.
func TestTransferRequest_Command(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*app.TransferRequest)
		wantField string
		wantKind  error // nil means the request is valid
	}{
		{name: "valid", mutate: func(*app.TransferRequest) {}},
		{name: "valid non-ascii and spaces inside", mutate: func(r *app.TransferRequest) { r.IdempotencyKey = str("ключ 1/%") }},
		{name: "valid key of 255 runes", mutate: func(r *app.TransferRequest) {
			r.IdempotencyKey = str(strings.Repeat("я", domain.MaxIdempotencyKeyLen))
		}},
		{name: "valid account id of 64 runes", mutate: func(r *app.TransferRequest) { r.FromAccountID = str(strings.Repeat("a", domain.MaxAccountIDLen)) }},
		{name: "valid max int64", mutate: func(r *app.TransferRequest) { r.Amount = amount(1<<63 - 1) }},
		{name: "valid zero-width joiner is not a control char", mutate: func(r *app.TransferRequest) { r.ToAccountID = str("a" + string(rune(0x200d)) + "b") }},
		{name: "valid surrounding spaces are kept", mutate: func(r *app.TransferRequest) { r.ToAccountID = str(" acc_2") }},

		{name: "key missing", mutate: func(r *app.TransferRequest) { r.IdempotencyKey = app.StringField{} }, wantField: domain.FieldIdempotencyKey, wantKind: domain.ErrRequired},
		{name: "key empty", mutate: func(r *app.TransferRequest) { r.IdempotencyKey = str("") }, wantField: domain.FieldIdempotencyKey, wantKind: domain.ErrRequired},
		{name: "key spaces only", mutate: func(r *app.TransferRequest) { r.IdempotencyKey = str("   ") }, wantField: domain.FieldIdempotencyKey, wantKind: domain.ErrRequired},
		{name: "key tabs only is required, not invalid characters", mutate: func(r *app.TransferRequest) { r.IdempotencyKey = str("\t\t") }, wantField: domain.FieldIdempotencyKey, wantKind: domain.ErrRequired},
		{name: "key not a string", mutate: func(r *app.TransferRequest) { r.IdempotencyKey = app.StringField{Kind: app.FieldWrongType} }, wantField: domain.FieldIdempotencyKey, wantKind: domain.ErrNotString},
		{name: "key 256 runes", mutate: func(r *app.TransferRequest) {
			r.IdempotencyKey = str(strings.Repeat("я", domain.MaxIdempotencyKeyLen+1))
		}, wantField: domain.FieldIdempotencyKey, wantKind: domain.ErrTooLong},
		{name: "key with newline", mutate: func(r *app.TransferRequest) { r.IdempotencyKey = str("a\nb") }, wantField: domain.FieldIdempotencyKey, wantKind: domain.ErrInvalidCharacters},
		{name: "key with NUL", mutate: func(r *app.TransferRequest) { r.IdempotencyKey = str("a\x00b") }, wantField: domain.FieldIdempotencyKey, wantKind: domain.ErrInvalidCharacters},
		{name: "key with C1 control", mutate: func(r *app.TransferRequest) { r.IdempotencyKey = str("a\u0085b") }, wantField: domain.FieldIdempotencyKey, wantKind: domain.ErrInvalidCharacters},

		{name: "from missing", mutate: func(r *app.TransferRequest) { r.FromAccountID = app.StringField{} }, wantField: domain.FieldFromAccountID, wantKind: domain.ErrRequired},
		{name: "from not a string", mutate: func(r *app.TransferRequest) { r.FromAccountID = app.StringField{Kind: app.FieldWrongType} }, wantField: domain.FieldFromAccountID, wantKind: domain.ErrNotString},
		{name: "from 65 runes", mutate: func(r *app.TransferRequest) { r.FromAccountID = str(strings.Repeat("a", domain.MaxAccountIDLen+1)) }, wantField: domain.FieldFromAccountID, wantKind: domain.ErrTooLong},
		{name: "from with control", mutate: func(r *app.TransferRequest) { r.FromAccountID = str("acc\x07") }, wantField: domain.FieldFromAccountID, wantKind: domain.ErrInvalidCharacters},
		{name: "to empty", mutate: func(r *app.TransferRequest) { r.ToAccountID = str("") }, wantField: domain.FieldToAccountID, wantKind: domain.ErrRequired},
		{name: "to not a string", mutate: func(r *app.TransferRequest) { r.ToAccountID = app.StringField{Kind: app.FieldWrongType} }, wantField: domain.FieldToAccountID, wantKind: domain.ErrNotString},

		{name: "amount missing", mutate: func(r *app.TransferRequest) { r.Amount = app.AmountField{} }, wantField: domain.FieldAmount, wantKind: domain.ErrInvalidAmount},
		{name: "amount zero", mutate: func(r *app.TransferRequest) { r.Amount = amount(0) }, wantField: domain.FieldAmount, wantKind: domain.ErrInvalidAmount},
		{name: "amount negative", mutate: func(r *app.TransferRequest) { r.Amount = amount(-1) }, wantField: domain.FieldAmount, wantKind: domain.ErrInvalidAmount},

		// first error wins, in contract order: key → from → to → amount
		{name: "key error beats from error", mutate: func(r *app.TransferRequest) { r.IdempotencyKey, r.FromAccountID = str(""), str("") }, wantField: domain.FieldIdempotencyKey, wantKind: domain.ErrRequired},
		{name: "from error beats amount error", mutate: func(r *app.TransferRequest) { r.FromAccountID, r.Amount = str(""), amount(-5) }, wantField: domain.FieldFromAccountID, wantKind: domain.ErrRequired},
		{name: "to error beats amount error", mutate: func(r *app.TransferRequest) { r.ToAccountID, r.Amount = str(""), amount(-5) }, wantField: domain.FieldToAccountID, wantKind: domain.ErrRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validRequest()
			tt.mutate(&req)

			cmd, err := req.Command()
			if tt.wantKind == nil {
				require.NoError(t, err)
				require.Equal(t, req.IdempotencyKey.Value, cmd.IdempotencyKey)
				require.Equal(t, req.FromAccountID.Value, cmd.FromAccountID)
				require.Equal(t, req.ToAccountID.Value, cmd.ToAccountID)
				require.Equal(t, req.Amount.Value, cmd.Amount)
				return
			}
			require.ErrorIs(t, err, tt.wantKind)
			var ve *domain.ValidationError
			require.ErrorAs(t, err, &ve)
			require.Equal(t, tt.wantField, ve.Field)
		})
	}
}

// TestTransferRequest_ValidateKey: before the lookup only the key is checked; the other fields may still be invalid.
func TestTransferRequest_ValidateKey(t *testing.T) {
	require.NoError(t, validRequest().ValidateKey())

	req := validRequest()
	req.FromAccountID = app.StringField{} // other fields are not looked at
	require.NoError(t, req.ValidateKey())

	req.IdempotencyKey = app.StringField{}
	require.ErrorIs(t, req.ValidateKey(), domain.ErrRequired)
}
