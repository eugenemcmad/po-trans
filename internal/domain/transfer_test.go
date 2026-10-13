package domain

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRejectReason_RoundTrip: each rejection has exactly one reason, and the stored text is the published one.
func TestRejectReason_RoundTrip(t *testing.T) {
	tests := []struct {
		reason RejectReason
		err    error
	}{
		{ReasonInsufficientFunds, ErrInsufficientFunds},
		{ReasonSelfTransfer, ErrSelfTransfer},
		{ReasonAccountNotFound, ErrAccountNotFound},
	}
	for _, tt := range tests {
		t.Run(string(tt.reason), func(t *testing.T) {
			require.Equal(t, tt.err, tt.reason.Err())

			got, ok := RejectReasonOf(fmt.Errorf("context: %w", tt.err))
			require.True(t, ok)
			require.Equal(t, tt.reason, got)

			// the stored text is the published text, not an error message that may change
			require.Equal(t, tt.err.Error(), string(tt.reason))
		})
	}
}

// TestRejectReasonOf_NotARejection: other errors are never taken for a rejection, so they cannot turn into a 422.
func TestRejectReasonOf_NotARejection(t *testing.T) {
	for _, err := range []error{nil, errors.New("boom"), ErrIdempotencyConflict, ErrTransferNotFound, &ValidationError{Field: "x", Err: ErrRequired}} {
		_, ok := RejectReasonOf(err)
		require.False(t, ok, "%v", err)
	}
	require.NoError(t, RejectReason("unknown").Err())
}

// TestCovers: rule 1 at its boundary — the whole balance may be sent, one coin more may not.
func TestCovers(t *testing.T) {
	tests := []struct {
		name            string
		balance, amount int64
		want            bool
	}{
		{"enough", 1000, 100, true},
		{"whole balance is allowed", 100, 100, true},
		{"one coin short", 99, 100, false},
		{"empty account", 0, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, covers(tt.balance, tt.amount))
		})
	}
}

// TestTransferCommand: exact self-transfer check, comparison with a stored transfer (rule 2) and the error of a rejected one.
func TestTransferCommand(t *testing.T) {
	cmd := TransferCommand{IdempotencyKey: "k", FromAccountID: "acc_1", ToAccountID: "acc_2", Amount: 5}

	t.Run("self transfer", func(t *testing.T) {
		require.False(t, cmd.isSelfTransfer())
		same := cmd
		same.ToAccountID = "acc_1"
		require.True(t, same.isSelfTransfer())
		// the check is exact, case matters
		same.ToAccountID = "ACC_1"
		require.False(t, same.isSelfTransfer())
	})

	t.Run("same as stored", func(t *testing.T) {
		stored := cmd.NewCompleted()
		require.True(t, cmd.SameAs(stored))
		other := cmd
		other.Amount = 6
		require.False(t, other.SameAs(stored))
	})

	t.Run("rejection error", func(t *testing.T) {
		require.NoError(t, cmd.NewCompleted().RejectionError())
		rejected := cmd.NewCompleted().Reject(ReasonInsufficientFunds)
		require.ErrorIs(t, rejected.RejectionError(), ErrInsufficientFunds)
	})
}

// TestContainsControl: what counts as a control character in keys, account ids and X-Request-ID.
func TestContainsControl(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"plain", false},
		{"", false},
		{"tab\t", true},
		{"del\x7f", true},
		{"c1\u0080", true},
		{"zwj" + string(rune(0x200d)), false},
		{"bidi" + string(rune(0x202e)), false},
		{"emoji 👩‍👩‍👧", false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.in), func(t *testing.T) {
			require.Equal(t, tt.want, ContainsControl(tt.in))
		})
	}
}

// TestTransferTransitions: a transfer starts completed, and only Reject turns it into rejected, with a reason, leaving the original intact.
func TestTransferTransitions(t *testing.T) {
	cmd := TransferCommand{TransferID: "id-1", IdempotencyKey: "k", FromAccountID: "acc_1", ToAccountID: "acc_2", Amount: 5}

	completed := cmd.NewCompleted()
	require.Equal(t, Transfer{ID: "id-1", IdempotencyKey: "k", FromAccountID: "acc_1", ToAccountID: "acc_2", Amount: 5, Status: StatusCompleted}, completed)
	require.Empty(t, completed.Reason)

	rejected := completed.Reject(ReasonSelfTransfer)
	require.Equal(t, StatusRejected, rejected.Status)
	require.Equal(t, ReasonSelfTransfer, rejected.Reason)
	require.Equal(t, completed.ID, rejected.ID, "same transfer, new outcome")
	require.Equal(t, StatusCompleted, completed.Status, "the original is not modified")
}
