package app_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"po-trans/internal/app"
	"po-trans/internal/domain"
	"po-trans/tests/memstore"
)

func amt(v int64) app.AmountField { return app.AmountField{Valid: true, Value: v} }

func request(key, from, to string, amount int64) app.TransferRequest {
	return app.TransferRequest{IdempotencyKey: str(key), FromAccountID: str(from), ToAccountID: str(to), Amount: amt(amount)}
}

func newCreate(s *memstore.Store) *app.CreateTransfer {
	return app.NewCreateTransfer(s, s)
}

// TestCreateTransfer_Handle: the business rules on the in-memory store — completed or rejected (rules 1, 3, 4) and the balances after each.
func TestCreateTransfer_Handle(t *testing.T) {
	ctx := t.Context()

	tests := []struct {
		name string
		// prior is executed first (same store) and must succeed or be rejected
		prior   *app.TransferRequest
		req     app.TransferRequest
		wantErr error
		// balances expected after the call: acc_1, acc_2
		want1, want2 int64
	}{
		{name: "completed", req: request("k", "acc_1", "acc_2", 100), want1: 900, want2: 1100},
		{name: "whole balance", req: request("k", "acc_1", "acc_2", 1000), want1: 0, want2: 2000},
		{name: "insufficient funds", req: request("k", "acc_1", "acc_2", 1001), wantErr: domain.ErrInsufficientFunds, want1: 1000, want2: 1000},
		{name: "self transfer", req: request("k", "acc_1", "acc_1", 1), wantErr: domain.ErrSelfTransfer, want1: 1000, want2: 1000},
		{name: "self transfer wins over unknown account", req: request("k", "acc_99", "acc_99", 1), wantErr: domain.ErrSelfTransfer, want1: 1000, want2: 1000},
		{name: "unknown sender", req: request("k", "acc_99", "acc_2", 1), wantErr: domain.ErrAccountNotFound, want1: 1000, want2: 1000},
		{name: "unknown receiver", req: request("k", "acc_1", "acc_99", 1), wantErr: domain.ErrAccountNotFound, want1: 1000, want2: 1000},
		{name: "account ids are case sensitive", req: request("k", "ACC_1", "acc_2", 1), wantErr: domain.ErrAccountNotFound, want1: 1000, want2: 1000},

		{name: "repeat of completed is not applied twice", prior: reqPtr(request("k", "acc_1", "acc_2", 100)), req: request("k", "acc_1", "acc_2", 100), want1: 900, want2: 1100},
		{name: "repeat of rejected is rejected again", prior: reqPtr(request("k", "acc_1", "acc_2", 5000)), req: request("k", "acc_1", "acc_2", 5000), wantErr: domain.ErrInsufficientFunds, want1: 1000, want2: 1000},
		{name: "other amount under used key", prior: reqPtr(request("k", "acc_1", "acc_2", 100)), req: request("k", "acc_1", "acc_2", 101), wantErr: domain.ErrIdempotencyConflict, want1: 900, want2: 1100},
		{name: "other receiver under used key", prior: reqPtr(request("k", "acc_1", "acc_2", 100)), req: request("k", "acc_1", "acc_3", 100), wantErr: domain.ErrIdempotencyConflict, want1: 900, want2: 1100},
		{name: "conflict beats a new rejection", prior: reqPtr(request("k", "acc_1", "acc_2", 100)), req: request("k", "acc_1", "acc_2", 999999), wantErr: domain.ErrIdempotencyConflict, want1: 900, want2: 1100},
		{name: "other data under rejected key", prior: reqPtr(request("k", "acc_1", "acc_2", 5000)), req: request("k", "acc_1", "acc_2", 1), wantErr: domain.ErrIdempotencyConflict, want1: 1000, want2: 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := memstore.Seeded()
			uc := newCreate(store)
			if tt.prior != nil {
				_, _ = uc.Handle(ctx, *tt.prior)
			}

			got, err := uc.Handle(ctx, tt.req)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, domain.StatusCompleted, got.Status)
				require.NotEmpty(t, got.ID)
			}
			require.Equal(t, tt.want1, store.Balance("acc_1"))
			require.Equal(t, tt.want2, store.Balance("acc_2"))
		})
	}
}

func reqPtr(r app.TransferRequest) *app.TransferRequest { return &r }

// A repeat must return the same transfer, not a new one.
func TestCreateTransfer_RepeatReturnsSameTransfer(t *testing.T) {
	ctx := t.Context()
	uc := newCreate(memstore.Seeded())

	first, err := uc.Handle(ctx, request("k", "acc_1", "acc_2", 10))
	require.NoError(t, err)
	second, err := uc.Handle(ctx, request("k", "acc_1", "acc_2", 10))
	require.NoError(t, err)
	require.Equal(t, first, second)
}

// TestCreateTransfer_InvalidInput: invalid fields are 400 under a new key but 409 under a used one (rule 2 before rule 5).
func TestCreateTransfer_InvalidInput(t *testing.T) {
	ctx := t.Context()
	valid := request("used", "acc_1", "acc_2", 100)

	tests := []struct {
		name string
		req  app.TransferRequest
		// withKey: the key "used" already exists with valid data
		withKey   bool
		wantErr   error
		wantField string
	}{
		{name: "no key: missing from", req: app.TransferRequest{IdempotencyKey: str("new"), ToAccountID: str("acc_2"), Amount: amt(1)}, wantErr: domain.ErrRequired, wantField: domain.FieldFromAccountID},
		{name: "no key: bad amount", req: app.TransferRequest{IdempotencyKey: str("new"), FromAccountID: str("acc_1"), ToAccountID: str("acc_2"), Amount: amt(-1)}, wantErr: domain.ErrInvalidAmount, wantField: domain.FieldAmount},
		{name: "no key: from has another type", req: app.TransferRequest{IdempotencyKey: str("new"), FromAccountID: app.StringField{Kind: app.FieldWrongType}, ToAccountID: str("acc_2"), Amount: amt(1)}, wantErr: domain.ErrNotString, wantField: domain.FieldFromAccountID},
		{name: "invalid key is a 400 even when nothing can be compared", req: app.TransferRequest{IdempotencyKey: str("  "), FromAccountID: str("acc_1"), ToAccountID: str("acc_2"), Amount: amt(1)}, withKey: true, wantErr: domain.ErrRequired, wantField: domain.FieldIdempotencyKey},
		{name: "invalid key type", req: app.TransferRequest{IdempotencyKey: app.StringField{Kind: app.FieldWrongType}}, wantErr: domain.ErrNotString, wantField: domain.FieldIdempotencyKey},

		{name: "used key: negative amount is a conflict", withKey: true, req: request("used", "acc_1", "acc_2", -1), wantErr: domain.ErrIdempotencyConflict},
		{name: "used key: zero amount is a conflict", withKey: true, req: request("used", "acc_1", "acc_2", 0), wantErr: domain.ErrIdempotencyConflict},
		{name: "used key: amount as non-integer is a conflict", withKey: true, req: app.TransferRequest{IdempotencyKey: str("used"), FromAccountID: str("acc_1"), ToAccountID: str("acc_2")}, wantErr: domain.ErrIdempotencyConflict},
		{name: "used key: empty from is a conflict", withKey: true, req: request("used", "", "acc_2", 100), wantErr: domain.ErrIdempotencyConflict},
		{name: "used key: from of another type is a conflict", withKey: true, req: app.TransferRequest{IdempotencyKey: str("used"), FromAccountID: app.StringField{Kind: app.FieldWrongType}, ToAccountID: str("acc_2"), Amount: amt(100)}, wantErr: domain.ErrIdempotencyConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := memstore.Seeded()
			uc := newCreate(store)
			if tt.withKey {
				_, err := uc.Handle(ctx, valid)
				require.NoError(t, err)
			}
			before := store.TransferCount()

			_, err := uc.Handle(ctx, tt.req)
			require.ErrorIs(t, err, tt.wantErr)
			if tt.wantField != "" {
				var ve *domain.ValidationError
				require.ErrorAs(t, err, &ve)
				require.Equal(t, tt.wantField, ve.Field)
			}
			require.Equal(t, before, store.TransferCount(), "invalid input must not be stored")
		})
	}
}

// TestCreateTransfer_StorageErrors: a storage failure surfaces as an error (500), never as a validation answer.
func TestCreateTransfer_StorageErrors(t *testing.T) {
	ctx := t.Context()
	boom := errors.New("storage down")

	t.Run("writer error is returned", func(t *testing.T) {
		store := memstore.Seeded()
		store.Err = boom
		_, err := newCreate(store).Handle(ctx, request("k", "acc_1", "acc_2", 1))
		require.ErrorIs(t, err, boom)
	})

	t.Run("lookup error on invalid fields is returned, not hidden as 400", func(t *testing.T) {
		store := memstore.Seeded()
		store.Err = boom
		_, err := newCreate(store).Handle(ctx, request("k", "", "acc_2", 1))
		require.ErrorIs(t, err, boom)
		var ve *domain.ValidationError
		require.NotErrorAs(t, err, &ve)
	})
}

// TestQueries: balance and transfer lookups and their not-found errors.
func TestQueries(t *testing.T) {
	ctx := t.Context()
	store := memstore.Seeded()
	_, err := newCreate(store).Handle(ctx, request("k", "acc_1", "acc_2", 100))
	require.NoError(t, err)

	t.Run("balance", func(t *testing.T) {
		got, err := app.NewGetBalance(store).Handle(ctx, "acc_1")
		require.NoError(t, err)
		require.Equal(t, domain.Account{ID: "acc_1", Balance: 900}, got)
	})
	t.Run("unknown account", func(t *testing.T) {
		_, err := app.NewGetBalance(store).Handle(ctx, "acc_99")
		require.ErrorIs(t, err, domain.ErrAccountNotFound)
	})
	t.Run("transfer by key", func(t *testing.T) {
		got, err := app.NewGetTransfer(store).Handle(ctx, "k")
		require.NoError(t, err)
		require.Equal(t, domain.StatusCompleted, got.Status)
		require.Equal(t, int64(100), got.Amount)
	})
	t.Run("unknown transfer", func(t *testing.T) {
		_, err := app.NewGetTransfer(store).Handle(ctx, "nope")
		require.ErrorIs(t, err, domain.ErrTransferNotFound)
	})
}

// An id that cannot exist is answered without touching the storage: the broken
// storage below would otherwise turn the answer into an error.
func TestQueries_UnstorableValuesSkipTheStorage(t *testing.T) {
	ctx := t.Context()
	broken := memstore.Seeded()
	broken.Err = errors.New("storage down")

	tests := []struct {
		name  string
		value string
	}{
		{"too long", strings.Repeat("a", domain.MaxIdempotencyKeyLen+1)},
		{"control character", "a\x00b"},
		{"invalid utf8", "a\xffb"},
		{"blank", "  "},
	}
	for _, tt := range tests {
		t.Run("transfer key "+tt.name, func(t *testing.T) {
			_, err := app.NewGetTransfer(broken).Handle(ctx, tt.value)
			require.ErrorIs(t, err, domain.ErrTransferNotFound)
		})
		t.Run("account id "+tt.name, func(t *testing.T) {
			_, err := app.NewGetBalance(broken).Handle(ctx, tt.value)
			require.ErrorIs(t, err, domain.ErrAccountNotFound)
		})
	}

	t.Run("a valid value does reach the storage", func(t *testing.T) {
		_, err := app.NewGetTransfer(broken).Handle(ctx, "k")
		require.ErrorContains(t, err, "storage down")
	})
	t.Run("account id of 65 characters is too long, of 64 is asked for", func(t *testing.T) {
		_, err := app.NewGetBalance(broken).Handle(ctx, strings.Repeat("a", domain.MaxAccountIDLen+1))
		require.ErrorIs(t, err, domain.ErrAccountNotFound)
		_, err = app.NewGetBalance(broken).Handle(ctx, strings.Repeat("a", domain.MaxAccountIDLen))
		require.ErrorContains(t, err, "storage down")
	})
}

// TestNew_WiresAllUseCases: app.New connects every use case to the store.
func TestNew_WiresAllUseCases(t *testing.T) {
	store := memstore.Seeded()
	a := app.New(store, store)
	require.NotNil(t, a.CreateTransfer)
	require.NotNil(t, a.GetTransfer)
	require.NotNil(t, a.GetBalance)

	_, err := a.CreateTransfer.Handle(t.Context(), request("k", "acc_1", "acc_2", 1))
	require.NoError(t, err)
	got, err := a.GetBalance.Handle(t.Context(), "acc_2")
	require.NoError(t, err)
	require.EqualValues(t, 1001, got.Balance)
}

// The application assigns transfer ids: UUIDv7, a new one per transfer.
func TestCreateTransfer_AssignsUUIDv7(t *testing.T) {
	uc := newCreate(memstore.Seeded())

	a, err := uc.Handle(t.Context(), request("k1", "acc_1", "acc_2", 1))
	require.NoError(t, err)
	b, err := uc.Handle(t.Context(), request("k2", "acc_1", "acc_2", 1))
	require.NoError(t, err)

	require.NotEqual(t, a.ID, b.ID)
	for _, id := range []string{a.ID, b.ID} {
		require.Len(t, id, 36)
		require.Equal(t, byte('7'), id[14], "version nibble of %s", id)
	}
}
