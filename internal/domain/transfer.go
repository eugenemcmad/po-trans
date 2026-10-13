package domain

import (
	"time"
)

// TransferStatus is the persisted outcome of a transfer.
type TransferStatus string

const (
	StatusCompleted TransferStatus = "completed"
	StatusRejected  TransferStatus = "rejected"
)

// TransferCommand is a fully validated transfer request. TransferID is the
// identifier the new transfer will get; the application assigns it.
type TransferCommand struct {
	TransferID     string
	IdempotencyKey string
	FromAccountID  string
	ToAccountID    string
	Amount         int64
}

// Transfer is a stored transfer, completed or rejected.
type Transfer struct {
	ID             string
	IdempotencyKey string
	FromAccountID  string
	ToAccountID    string
	Amount         int64
	Status         TransferStatus
	Reason         RejectReason // set only when Status is StatusRejected
	CreatedAt      time.Time
}

// isSelfTransfer reports whether both accounts are the same (exact comparison).
func (c TransferCommand) isSelfTransfer() bool {
	return c.FromAccountID == c.ToAccountID
}

// covers reports whether balance can cover amount; transferring the whole
// balance is allowed. It is a one-liner on purpose: the name states rule 1 of
// the task ("never negative", so zero is fine) where Decide applies it, and the
// boundary is pinned by its own test as well as by TestDecide.
func covers(balance, amount int64) bool {
	return balance >= amount
}

// SameAs reports whether the stored transfer was made with exactly the
// parameters of the command (the idempotency key is already equal).
func (c TransferCommand) SameAs(t Transfer) bool {
	return c.FromAccountID == t.FromAccountID && c.ToAccountID == t.ToAccountID && c.Amount == t.Amount
}

// NewCompleted builds the stored form of a transfer that has just been made;
// it has no rejection reason.
func (c TransferCommand) NewCompleted() Transfer {
	return Transfer{
		ID:             c.TransferID,
		IdempotencyKey: c.IdempotencyKey,
		FromAccountID:  c.FromAccountID,
		ToAccountID:    c.ToAccountID,
		Amount:         c.Amount,
		Status:         StatusCompleted,
	}
}

// Reject returns the transfer as rejected for the given reason. It is the only
// transition away from completed, so a rejected transfer always has a reason.
func (t Transfer) Reject(reason RejectReason) Transfer {
	t.Status = StatusRejected
	t.Reason = reason
	return t
}

// RejectionError returns the sentinel error of a rejected transfer, nil otherwise.
func (t Transfer) RejectionError() error {
	if t.Status != StatusRejected {
		return nil
	}
	return t.Reason.Err()
}
