// Package postgres implements the application ports (the write and the read
// repositories) on PostgreSQL with pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"po-trans/internal/domain"
)

const (
	insertTransferSQL = `
INSERT INTO transfers (id, idempotency_key, from_account_id, to_account_id, amount, status, reason)
VALUES ($1, $2, $3, $4, $5, $6, NULL)
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING created_at`

	rejectTransferSQL = `UPDATE transfers SET status = $2, reason = $3 WHERE id = $1`

	lockAccountSQL = `SELECT balance FROM accounts WHERE id = $1 FOR UPDATE`

	debitSQL  = `UPDATE accounts SET balance = balance - $2 WHERE id = $1`
	creditSQL = `UPDATE accounts SET balance = balance + $2 WHERE id = $1`
)

// TransferRepository is the command side: one transfer is one transaction.
type TransferRepository struct {
	pool   *pgxpool.Pool
	retry  RetryPolicy
	logger *slog.Logger
}

// NewTransferRepository creates the write repository; logger receives what
// cannot be returned to the caller (a failed rollback).
func NewTransferRepository(pool *pgxpool.Pool, retry RetryPolicy, logger *slog.Logger) *TransferRepository {
	return &TransferRepository{pool: pool, retry: retry, logger: logger}
}

// Transfer implements app.TransferWriter.
func (r *TransferRepository) Transfer(ctx context.Context, cmd domain.TransferCommand) (domain.Transfer, error) {
	var result domain.Transfer
	err := r.retry.do(ctx, func(ctx context.Context) error {
		t, err := r.transferOnce(ctx, cmd)
		if err != nil {
			return err
		}
		result = t
		return nil
	})
	if err != nil {
		return domain.Transfer{}, err
	}
	return result, nil
}

// transferOnce runs the whole transfer in a single READ COMMITTED transaction.
func (r *TransferRepository) transferOnce(ctx context.Context, cmd domain.TransferCommand) (domain.Transfer, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return domain.Transfer{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer r.rollback(ctx, tx)

	t := cmd.NewCompleted()
	err = tx.QueryRow(ctx, insertTransferSQL, cmd.TransferID, cmd.IdempotencyKey, cmd.FromAccountID, cmd.ToAccountID, cmd.Amount, string(domain.StatusCompleted)).
		Scan(&t.CreatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// The key exists (possibly committed by a transaction we just waited for).
		return replay(ctx, tx, cmd)
	case err != nil:
		return domain.Transfer{}, fmt.Errorf("insert transfer: %w", err)
	}
	t.CreatedAt = t.CreatedAt.UTC()

	rejection, err := apply(ctx, tx, cmd)
	if err != nil {
		return domain.Transfer{}, err
	}
	if rejection != "" {
		if _, err := tx.Exec(ctx, rejectTransferSQL, cmd.TransferID, string(domain.StatusRejected), string(rejection)); err != nil {
			return domain.Transfer{}, fmt.Errorf("mark transfer rejected: %w", err)
		}
		t = t.Reject(rejection)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Transfer{}, fmt.Errorf("commit transfer: %w", err)
	}
	return t, nil
}

// replay answers a repeated key: the stored transfer, or a conflict when the
// parameters differ.
func replay(ctx context.Context, tx pgx.Tx, cmd domain.TransferCommand) (domain.Transfer, error) {
	existing, err := scanTransfer(tx.QueryRow(ctx, selectTransferSQL, cmd.IdempotencyKey))
	if err != nil {
		if errors.Is(err, domain.ErrTransferNotFound) {
			// The key vanished between the conflict and the read; only possible if
			// rows are deleted, which the service never does.
			return domain.Transfer{}, fmt.Errorf("idempotency key conflicted but no row found: %w", err)
		}
		return domain.Transfer{}, err
	}
	if !cmd.SameAs(existing) {
		return domain.Transfer{}, domain.ErrIdempotencyConflict
	}
	return existing, nil
}

// apply lets the domain decide and, if the transfer may proceed, moves the
// money. It returns a non-empty reason when the transfer must be rejected.
func apply(ctx context.Context, tx pgx.Tx, cmd domain.TransferCommand) (domain.RejectReason, error) {
	reason, err := domain.Decide(cmd, func(id string) (*domain.Account, error) {
		return lockAccount(ctx, tx, id)
	})
	if err != nil || reason != "" {
		return reason, err
	}

	if _, err := tx.Exec(ctx, debitSQL, cmd.FromAccountID, cmd.Amount); err != nil {
		return "", fmt.Errorf("debit account: %w", err)
	}
	if _, err := tx.Exec(ctx, creditSQL, cmd.ToAccountID, cmd.Amount); err != nil {
		return "", fmt.Errorf("credit account: %w", err)
	}
	return "", nil
}

// lockAccount reads an account and holds its row lock until the transaction
// ends; it returns nil when the account does not exist.
func lockAccount(ctx context.Context, tx pgx.Tx, id string) (*domain.Account, error) {
	account := domain.Account{ID: id}
	err := tx.QueryRow(ctx, lockAccountSQL, id).Scan(&account.Balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // nil account: does not exist (domain.AccountLoader)
	}
	if err != nil {
		return nil, fmt.Errorf("lock account: %w", err)
	}
	return &account, nil
}

// rollback ends a transaction that was not committed. It uses a context that
// survives cancellation of the request so the server releases its locks.
func (r *TransferRepository) rollback(ctx context.Context, tx pgx.Tx) {
	if err := tx.Rollback(context.WithoutCancel(ctx)); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		r.logger.Error("rollback transfer transaction", "error", err)
	}
}
