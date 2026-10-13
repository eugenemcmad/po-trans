// Package app holds the use cases (one command, two queries) and the ports
// they depend on.
package app

import (
	"context"

	"po-trans/internal/domain"
)

// TransferWriter is the command side: it executes a transfer atomically.
//
// It returns the stored transfer — completed or rejected — for a new key, and
// the stored transfer again for a repeat of the same key with the same data.
// A repeat with other data yields domain.ErrIdempotencyConflict.
type TransferWriter interface {
	Transfer(ctx context.Context, cmd domain.TransferCommand) (domain.Transfer, error)
}

// TransferReader is the query side for transfers.
type TransferReader interface {
	// GetByKey returns domain.ErrTransferNotFound when there is no such transfer.
	GetByKey(ctx context.Context, idempotencyKey string) (domain.Transfer, error)
}

// AccountReader is the query side for accounts.
type AccountReader interface {
	// GetAccount returns domain.ErrAccountNotFound when there is no such account.
	GetAccount(ctx context.Context, id string) (domain.Account, error)
}

// Reader is the whole query side.
type Reader interface {
	TransferReader
	AccountReader
}
