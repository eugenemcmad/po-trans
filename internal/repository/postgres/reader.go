package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"po-trans/internal/domain"
)

const (
	selectTransferSQL = `
SELECT id, idempotency_key, from_account_id, to_account_id, amount, status, reason, created_at
FROM transfers WHERE idempotency_key = $1`

	selectAccountSQL = `SELECT id, balance FROM accounts WHERE id = $1`
)

// QueryRepository is the query side: transfers and accounts.
type QueryRepository struct {
	pool *pgxpool.Pool
}

// NewQueryRepository creates the read repository.
func NewQueryRepository(pool *pgxpool.Pool) *QueryRepository { return &QueryRepository{pool: pool} }

// GetByKey implements app.TransferReader.
func (r *QueryRepository) GetByKey(ctx context.Context, key string) (domain.Transfer, error) {
	return scanTransfer(r.pool.QueryRow(ctx, selectTransferSQL, key))
}

// GetAccount implements app.AccountReader.
func (r *QueryRepository) GetAccount(ctx context.Context, id string) (domain.Account, error) {
	var a domain.Account
	err := r.pool.QueryRow(ctx, selectAccountSQL, id).Scan(&a.ID, &a.Balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Account{}, domain.ErrAccountNotFound
	}
	if err != nil {
		return domain.Account{}, fmt.Errorf("select account: %w", err)
	}
	return a, nil
}

// HealthCheck reports whether the database is reachable.
type HealthCheck struct {
	pool *pgxpool.Pool
}

// NewHealthCheck creates the readiness probe of the pool.
func NewHealthCheck(pool *pgxpool.Pool) *HealthCheck { return &HealthCheck{pool: pool} }

// Ping implements httpapi.Pinger.
func (h *HealthCheck) Ping(ctx context.Context) error {
	if err := h.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}

func scanTransfer(row pgx.Row) (domain.Transfer, error) {
	var (
		t      domain.Transfer
		status string
		reason *string
	)
	err := row.Scan(&t.ID, &t.IdempotencyKey, &t.FromAccountID, &t.ToAccountID, &t.Amount, &status, &reason, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Transfer{}, domain.ErrTransferNotFound
	}
	if err != nil {
		return domain.Transfer{}, fmt.Errorf("select transfer: %w", err)
	}
	t.Status = domain.TransferStatus(status)
	if reason != nil {
		t.Reason = domain.RejectReason(*reason)
	}
	t.CreatedAt = t.CreatedAt.UTC()
	return t, nil
}
