package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestMain fails the package on leaked goroutines.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func pgErr(code string) error { return &pgconn.PgError{Code: code} }

// script returns a function failing with the given errors in order, then succeeding.
func script(calls *int, errs ...error) func(context.Context) error {
	return func(context.Context) error {
		i := *calls
		*calls++
		if i < len(errs) {
			return errs[i]
		}
		return nil
	}
}

// TestRetryPolicy_Do: only deadlocks and serialization failures are retried, at most MaxRetries times; then the attempts and the last code are reported.
func TestRetryPolicy_Do(t *testing.T) {
	other := errors.New("syntax error")
	tests := []struct {
		name         string
		policy       RetryPolicy
		errs         []error
		wantCalls    int
		wantErr      error
		wantExhaust  bool
		wantLastCode string
	}{
		{name: "success first time", policy: RetryPolicy{MaxRetries: 3}, wantCalls: 1},
		{name: "deadlock then success", policy: RetryPolicy{MaxRetries: 3}, errs: []error{pgErr("40P01")}, wantCalls: 2},
		{name: "serialization failure then success", policy: RetryPolicy{MaxRetries: 3}, errs: []error{pgErr("40001"), pgErr("40001")}, wantCalls: 3},
		{name: "three retries are allowed", policy: RetryPolicy{MaxRetries: 3}, errs: []error{pgErr("40P01"), pgErr("40P01"), pgErr("40P01")}, wantCalls: 4},
		{name: "exhausted after 4 attempts", policy: RetryPolicy{MaxRetries: 3}, errs: []error{pgErr("40P01"), pgErr("40P01"), pgErr("40P01"), pgErr("40001"), pgErr("40P01")}, wantCalls: 4, wantExhaust: true, wantLastCode: "40001"},
		{name: "no retries: first deadlock is final", policy: RetryPolicy{MaxRetries: 0}, errs: []error{pgErr("40P01")}, wantCalls: 1, wantExhaust: true, wantLastCode: "40P01"},
		{name: "other pg error is not retried", policy: RetryPolicy{MaxRetries: 3}, errs: []error{pgErr("23505")}, wantCalls: 1, wantErr: &pgconn.PgError{Code: "23505"}},
		{name: "plain error is not retried", policy: RetryPolicy{MaxRetries: 3}, errs: []error{other}, wantCalls: 1, wantErr: other},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			err := tt.policy.do(t.Context(), script(&calls, tt.errs...))
			require.Equal(t, tt.wantCalls, calls)

			switch {
			case tt.wantExhaust:
				var ex *RetriesExhaustedError
				require.ErrorAs(t, err, &ex)
				require.Equal(t, tt.wantCalls, ex.Attempts)
				require.Equal(t, tt.wantLastCode, ex.Code)
				require.ErrorContains(t, err, tt.wantLastCode)
			case tt.wantErr != nil:
				require.Equal(t, tt.wantErr, err)
			default:
				require.NoError(t, err)
			}
		})
	}
}

// TestRetryPolicy_Do_WrappedPgError: a retryable code is found inside wrapped errors too.
func TestRetryPolicy_Do_WrappedPgError(t *testing.T) {
	var calls int
	err := RetryPolicy{MaxRetries: 1}.do(t.Context(), script(&calls, errors.Join(errors.New("ctx"), pgErr("40P01"))))
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}

// TestRetryPolicy_Do_ContextEnded: no retry outlives the request — an ended context stops both the loop and the pause.
func TestRetryPolicy_Do_ContextEnded(t *testing.T) {
	t.Run("cancelled before the retry: no further attempt", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		var calls int
		fn := func(context.Context) error { calls++; cancel(); return pgErr("40P01") }

		err := RetryPolicy{MaxRetries: 3}.do(ctx, fn)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 1, calls)
	})

	t.Run("cancelled during the pause", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		var calls int
		policy := RetryPolicy{MaxRetries: 3, Delay: func(int) time.Duration {
			cancel() // the pause is long, the context ends first
			return time.Hour
		}}
		err := policy.do(ctx, script(&calls, pgErr("40P01"), pgErr("40P01")))
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 1, calls)
	})
}

// TestDefaultRetryPolicy_DelayWindows: backoff with jitter stays in its windows (5–25, 10–50, 20–100 ms).
func TestDefaultRetryPolicy_DelayWindows(t *testing.T) {
	p := DefaultRetryPolicy()
	require.Equal(t, 3, p.MaxRetries)

	windows := []struct{ lo, hi time.Duration }{
		{5 * time.Millisecond, 25 * time.Millisecond},
		{10 * time.Millisecond, 50 * time.Millisecond},
		{20 * time.Millisecond, 100 * time.Millisecond},
	}
	for i, w := range windows {
		for range 200 {
			d := p.Delay(i + 1)
			require.GreaterOrEqual(t, d, w.lo, "retry %d", i+1)
			require.LessOrEqual(t, d, w.hi, "retry %d", i+1)
		}
	}
}

// TestRetriesExhaustedError_Unwrap: the final error keeps the PostgreSQL error and says how many attempts were made.
func TestRetriesExhaustedError_Unwrap(t *testing.T) {
	inner := pgErr("40P01")
	err := &RetriesExhaustedError{Attempts: 4, Code: "40P01", Err: inner}
	require.ErrorIs(t, err, inner)
	require.ErrorContains(t, err, "4 attempts")
}
