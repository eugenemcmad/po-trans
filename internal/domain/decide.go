package domain

import "fmt"

// AccountLoader returns the account with the given id, or nil if there is none.
// Storage adapters implement it; the PostgreSQL one also locks the row.
type AccountLoader func(id string) (*Account, error)

// Decide applies the transfer rules in the order of the contract and returns the
// rejection reason, or "" when the transfer may proceed:
//
//  1. self-transfer — needs no account, so nothing is loaded;
//  2. both accounts must exist;
//  3. the sender must hold at least the amount (the whole balance is allowed).
//
// Accounts are loaded one at a time in ascending byte order of their ids,
// whatever the direction of the transfer. An adapter that locks rows on load
// therefore always locks in the same order, which makes a wait cycle between
// two transfers impossible. Loading stops at the first missing account.
func Decide(cmd TransferCommand, load AccountLoader) (RejectReason, error) {
	if cmd.isSelfTransfer() {
		return ReasonSelfTransfer, nil
	}

	first, second := cmd.FromAccountID, cmd.ToAccountID
	if first > second {
		first, second = second, first
	}
	accounts := make(map[string]*Account, 2)
	for _, id := range [...]string{first, second} {
		account, err := load(id)
		if err != nil {
			return "", fmt.Errorf("load account %q: %w", id, err)
		}
		if account == nil {
			return ReasonAccountNotFound, nil
		}
		accounts[id] = account
	}

	if !covers(accounts[cmd.FromAccountID].Balance, cmd.Amount) {
		return ReasonInsufficientFunds, nil
	}
	return "", nil
}
