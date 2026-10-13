package domain

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// loader serves the given balances and records which ids were loaded, in order.
func loader(balances map[string]int64, loaded *[]string) AccountLoader {
	return func(id string) (*Account, error) {
		*loaded = append(*loaded, id)
		b, ok := balances[id]
		if !ok {
			return nil, nil
		}
		return &Account{ID: id, Balance: b}, nil
	}
}

// TestDecide: the order of the transfer rules (self-transfer, accounts, funds) and one lock order of the accounts whatever the direction — the guard against deadlocks.
func TestDecide(t *testing.T) {
	balances := map[string]int64{"acc_1": 1000, "acc_2": 1000, "acc_10": 50}
	tests := []struct {
		name       string
		cmd        TransferCommand
		wantReason RejectReason
		wantLoaded []string
	}{
		{"ok", TransferCommand{FromAccountID: "acc_1", ToAccountID: "acc_2", Amount: 100}, "", []string{"acc_1", "acc_2"}},
		{"whole balance", TransferCommand{FromAccountID: "acc_1", ToAccountID: "acc_2", Amount: 1000}, "", []string{"acc_1", "acc_2"}},
		{"one coin too many", TransferCommand{FromAccountID: "acc_1", ToAccountID: "acc_2", Amount: 1001}, ReasonInsufficientFunds, []string{"acc_1", "acc_2"}},
		{"self transfer loads nothing", TransferCommand{FromAccountID: "acc_1", ToAccountID: "acc_1", Amount: 1}, ReasonSelfTransfer, nil},
		{"self transfer wins over a missing account", TransferCommand{FromAccountID: "acc_99", ToAccountID: "acc_99", Amount: 1}, ReasonSelfTransfer, nil},
		{"self transfer wins over lack of funds", TransferCommand{FromAccountID: "acc_10", ToAccountID: "acc_10", Amount: 1000}, ReasonSelfTransfer, nil},
		{"ids differing in case are not a self transfer", TransferCommand{FromAccountID: "ACC_1", ToAccountID: "acc_1", Amount: 1}, ReasonAccountNotFound, []string{"ACC_1"}},
		{"missing sender", TransferCommand{FromAccountID: "acc_0", ToAccountID: "acc_1", Amount: 1}, ReasonAccountNotFound, []string{"acc_0"}},
		{"missing receiver", TransferCommand{FromAccountID: "acc_1", ToAccountID: "acc_99", Amount: 1}, ReasonAccountNotFound, []string{"acc_1", "acc_99"}},
		{"missing account wins over lack of funds", TransferCommand{FromAccountID: "acc_10", ToAccountID: "acc_99", Amount: 1000}, ReasonAccountNotFound, []string{"acc_10", "acc_99"}},
		// the order is by bytes, not by number or by direction
		{"opposite direction loads in the same order", TransferCommand{FromAccountID: "acc_2", ToAccountID: "acc_1", Amount: 1}, "", []string{"acc_1", "acc_2"}},
		{"acc_10 sorts before acc_2", TransferCommand{FromAccountID: "acc_2", ToAccountID: "acc_10", Amount: 1}, "", []string{"acc_10", "acc_2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var loaded []string
			reason, err := Decide(tt.cmd, loader(balances, &loaded))

			require.NoError(t, err)
			require.Equal(t, tt.wantReason, reason)
			require.Equal(t, tt.wantLoaded, loaded)
		})
	}
}

// TestDecide_LoadError: a storage error stops the decision at once, with no reason.
func TestDecide_LoadError(t *testing.T) {
	boom := errors.New("connection lost")
	var calls int
	reason, err := Decide(TransferCommand{FromAccountID: "acc_1", ToAccountID: "acc_2", Amount: 1}, func(string) (*Account, error) {
		calls++
		return nil, boom
	})

	require.ErrorIs(t, err, boom)
	require.Empty(t, reason)
	require.Equal(t, 1, calls, "stops at the first failure")
}
