package integration

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"po-trans/internal/config"
	"po-trans/internal/server"
)

// S1: app.Run, the function main calls, stops cleanly and closes its pool.
func TestS1_GracefulShutdownClosesThePool(t *testing.T) {
	observer := newPool(t) // a separate pool that watches pg_stat_activity

	// A unique application_name lets the test see exactly the connections of Run.
	appName := fmt.Sprintf("po-trans-s1-%d", time.Now().UnixNano())
	u, err := url.Parse(databaseURL())
	require.NoError(t, err)
	q := u.Query()
	q.Set("application_name", appName)
	u.RawQuery = q.Encode()

	cfg, err := config.Load(func(k string) string {
		switch k {
		case "DATABASE_URL":
			return u.String()
		case "HTTP_ADDR":
			return "127.0.0.1:0"
		}
		return ""
	})
	require.NoError(t, err)

	connections := func() int {
		return count(t, observer, `SELECT count(*) FROM pg_stat_activity WHERE application_name = $1`, appName)
	}

	appCtx, stop := context.WithCancel(context.Background())
	defer stop()
	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(appCtx, cfg, discard) }()

	// Run pings the database at start, so its pool holds a connection.
	require.Eventually(t, func() bool { return connections() > 0 }, 30*time.Second, 20*time.Millisecond, "Run never connected")

	stop()
	select {
	case err := <-runErr:
		require.NoError(t, err)
	case <-time.After(cfg.ShutdownTimeout):
		t.Fatal("Run did not return within SHUTDOWN_TIMEOUT")
	}

	// The server side drops closed connections asynchronously.
	require.Eventually(t, func() bool { return connections() == 0 }, 30*time.Second, 20*time.Millisecond, "the pool was not closed")
	// goleak runs after all tests (TestMain) and fails on a leaked pool goroutine.
}
