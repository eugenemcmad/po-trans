// Package config reads the service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// ErrMissingDatabaseURL is returned when DATABASE_URL is not set.
var ErrMissingDatabaseURL = errors.New("DATABASE_URL is required")

// Config is the service configuration.
type Config struct {
	HTTPAddr          string
	DatabaseURL       string
	DBMaxConns        int32
	RequestTimeout    time.Duration
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	// DocsEnabled turns on Swagger UI at /docs. Off by default: it is for a
	// quick check of the demo, never for production.
	DocsEnabled bool
}

// Load builds a Config from the environment; getenv is os.Getenv in production.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:    ":3000",
		DatabaseURL: getenv("DATABASE_URL"),
	}
	if v := getenv("HTTP_ADDR"); v != "" {
		cfg.HTTPAddr = v
	}
	if cfg.DatabaseURL == "" {
		return Config{}, ErrMissingDatabaseURL
	}

	maxConns, err := intVar(getenv, "DB_MAX_CONNS", 20)
	if err != nil {
		return Config{}, err
	}
	cfg.DBMaxConns = maxConns

	if cfg.DocsEnabled, err = boolVar(getenv, "DOCS_ENABLED", false); err != nil {
		return Config{}, err
	}

	durations := []struct {
		name string
		def  time.Duration
		dst  *time.Duration
	}{
		{"REQUEST_TIMEOUT", 10 * time.Second, &cfg.RequestTimeout},
		{"HTTP_READ_HEADER_TIMEOUT", 5 * time.Second, &cfg.ReadHeaderTimeout},
		{"HTTP_READ_TIMEOUT", 5 * time.Second, &cfg.ReadTimeout},
		{"HTTP_WRITE_TIMEOUT", 15 * time.Second, &cfg.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT", 60 * time.Second, &cfg.IdleTimeout},
		{"SHUTDOWN_TIMEOUT", 15 * time.Second, &cfg.ShutdownTimeout},
	}
	for _, d := range durations {
		v, err := durationVar(getenv, d.name, d.def)
		if err != nil {
			return Config{}, err
		}
		*d.dst = v
	}

	// The handler's work is bounded by REQUEST_TIMEOUT and the write deadline
	// runs from the same point, so only a longer write timeout leaves time to
	// send the response, an error one included (spec 5.7).
	if cfg.WriteTimeout <= cfg.RequestTimeout {
		return Config{}, fmt.Errorf("HTTP_WRITE_TIMEOUT (%s) must be longer than REQUEST_TIMEOUT (%s)", cfg.WriteTimeout, cfg.RequestTimeout)
	}
	return cfg, nil
}

// intVar, boolVar and durationVar share only "empty means the default"; each
// parses and checks its own type and says so in its error, so they are not
// folded into one generic helper.
func intVar(getenv func(string) string, name string, def int32) (int32, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: %q is not a positive integer", name, raw)
	}
	return int32(n), nil
}

func boolVar(getenv func(string) string, name string, def bool) (bool, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %q is not a boolean (true or false)", name, raw)
	}
	return b, nil
}

func durationVar(getenv func(string) string, name string, def time.Duration) (time.Duration, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s: %q is not a positive duration", name, raw)
	}
	return d, nil
}
