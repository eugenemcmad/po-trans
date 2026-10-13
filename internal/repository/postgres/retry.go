package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// PostgreSQL error codes that mean the transaction rolled back completely and
// may be run again.
const (
	codeDeadlockDetected     = "40P01"
	codeSerializationFailure = "40001"
)

// RetryPolicy controls how often a transaction is retried after a deadlock or
// serialization failure. The zero value never retries.
type RetryPolicy struct {
	// MaxRetries is the number of retries after the first attempt.
	MaxRetries int
	// Delay returns the pause before the n-th retry (n starts at 1); nil means no pause.
	Delay func(retry int) time.Duration
}

// DefaultRetryPolicy is up to 3 retries with exponential backoff and jitter:
// 5–25 ms, 10–50 ms, 20–100 ms.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries: 3,
		Delay: func(retry int) time.Duration {
			lo := 5 * time.Millisecond << (retry - 1)
			hi := 25 * time.Millisecond << (retry - 1)
			return lo + rand.N(hi-lo+1) //nolint:gosec // jitter to spread retries, not a security use
		},
	}
}

// RetriesExhaustedError reports that every attempt ended in a retryable error.
type RetriesExhaustedError struct {
	Attempts int
	Code     string
	Err      error
}

func (e *RetriesExhaustedError) Error() string {
	return fmt.Sprintf("transaction failed after %d attempts, last error code %s: %v", e.Attempts, e.Code, e.Err)
}

func (e *RetriesExhaustedError) Unwrap() error { return e.Err }

// retryableCode returns the PostgreSQL code of err when the transaction may be retried.
func retryableCode(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == codeDeadlockDetected || pgErr.Code == codeSerializationFailure) {
		return pgErr.Code, true
	}
	return "", false
}

// do runs fn, repeating it on retryable errors. The whole transaction is
// repeated, so fn must open its own transaction.
func (p RetryPolicy) do(ctx context.Context, fn func(context.Context) error) error {
	for attempt := 0; ; attempt++ {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		code, retryable := retryableCode(err)
		if !retryable {
			return err
		}
		if attempt >= p.MaxRetries {
			return &RetriesExhaustedError{Attempts: attempt + 1, Code: code, Err: err}
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("retry abandoned after error code %s: %w", code, ctxErr)
		}
		if p.Delay == nil {
			continue
		}
		timer := time.NewTimer(p.Delay(attempt + 1))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("retry abandoned after error code %s: %w", code, ctx.Err())
		}
	}
}
