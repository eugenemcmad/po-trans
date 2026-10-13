package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// defaultApplicationName names the service in pg_stat_activity unless the
	// connection string already sets one.
	defaultApplicationName = "po-trans"
	// idleInTransactionTimeout makes the server end a transaction that a client
	// left open (a stuck or leaked one) so that its row locks are released. A
	// transfer transaction is never idle for long: it only waits for the database.
	idleInTransactionTimeout = "30000" // milliseconds
)

// NewPool creates a connection pool; connections are opened lazily.
func NewPool(ctx context.Context, databaseURL string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = maxConns
	params := cfg.ConnConfig.RuntimeParams
	if _, set := params["application_name"]; !set {
		params["application_name"] = defaultApplicationName
	}
	if _, set := params["idle_in_transaction_session_timeout"]; !set {
		params["idle_in_transaction_session_timeout"] = idleInTransactionTimeout
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	return pool, nil
}
