package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestMain fails the package on leaked goroutines.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// TestLoad_Defaults: every variable but DATABASE_URL has a default; Swagger UI is off.
func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x"}))
	require.NoError(t, err)
	require.Equal(t, Config{
		HTTPAddr:          ":3000",
		DatabaseURL:       "postgres://x",
		DBMaxConns:        20,
		RequestTimeout:    10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   15 * time.Second,
	}, cfg)
}

// TestLoad_Overrides: every variable is read from the environment.
func TestLoad_Overrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"DATABASE_URL":             "postgres://x",
		"HTTP_ADDR":                "127.0.0.1:0",
		"DB_MAX_CONNS":             "5",
		"REQUEST_TIMEOUT":          "2s",
		"HTTP_READ_HEADER_TIMEOUT": "1s",
		"HTTP_READ_TIMEOUT":        "3s",
		"HTTP_WRITE_TIMEOUT":       "4s",
		"HTTP_IDLE_TIMEOUT":        "5m",
		"SHUTDOWN_TIMEOUT":         "500ms",
		"DOCS_ENABLED":             "true",
	}))
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:0", cfg.HTTPAddr)
	require.Equal(t, int32(5), cfg.DBMaxConns)
	require.Equal(t, 2*time.Second, cfg.RequestTimeout)
	require.Equal(t, time.Second, cfg.ReadHeaderTimeout)
	require.Equal(t, 3*time.Second, cfg.ReadTimeout)
	require.Equal(t, 4*time.Second, cfg.WriteTimeout)
	require.Equal(t, 5*time.Minute, cfg.IdleTimeout)
	require.Equal(t, 500*time.Millisecond, cfg.ShutdownTimeout)
	require.True(t, cfg.DocsEnabled)
}

// TestLoad_Errors: a bad value stops the start, and the error names the variable.
func TestLoad_Errors(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
		wantIs  error // the sentinel, when the error has one
	}{
		{name: "no database url", env: map[string]string{}, wantErr: "DATABASE_URL is required", wantIs: ErrMissingDatabaseURL},
		{name: "max conns not a number", env: map[string]string{"DATABASE_URL": "x", "DB_MAX_CONNS": "many"}, wantErr: "DB_MAX_CONNS"},
		{name: "max conns zero", env: map[string]string{"DATABASE_URL": "x", "DB_MAX_CONNS": "0"}, wantErr: "DB_MAX_CONNS"},
		{name: "timeout garbage", env: map[string]string{"DATABASE_URL": "x", "REQUEST_TIMEOUT": "soon"}, wantErr: "REQUEST_TIMEOUT"},
		{name: "timeout without unit", env: map[string]string{"DATABASE_URL": "x", "SHUTDOWN_TIMEOUT": "15"}, wantErr: "SHUTDOWN_TIMEOUT"},
		{name: "timeout negative", env: map[string]string{"DATABASE_URL": "x", "HTTP_IDLE_TIMEOUT": "-1s"}, wantErr: "HTTP_IDLE_TIMEOUT"},
		{name: "write timeout equal to request timeout", env: map[string]string{"DATABASE_URL": "x", "REQUEST_TIMEOUT": "15s"}, wantErr: "HTTP_WRITE_TIMEOUT"},
		{name: "docs flag not a boolean", env: map[string]string{"DATABASE_URL": "x", "DOCS_ENABLED": "yes"}, wantErr: "DOCS_ENABLED"},
		{name: "write timeout shorter than request timeout", env: map[string]string{"DATABASE_URL": "x", "HTTP_WRITE_TIMEOUT": "5s"}, wantErr: "HTTP_WRITE_TIMEOUT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(env(tt.env))
			require.ErrorContains(t, err, tt.wantErr)
			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			}
		})
	}
}
