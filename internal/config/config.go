package config

import (
	"flag"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Provider selects which RateProvider adapter cmd/server wires up.
type Provider string

const (
	ProviderFake            Provider = "fake"
	ProviderExchangerateDev Provider = "exchangeratedev"
)

func (p Provider) valid() bool {
	switch p {
	case ProviderFake, ProviderExchangerateDev:
		return true
	default:
		return false
	}
}

// Config holds the server's runtime configuration.
type Config struct {
	HTTPAddr          string
	ReadTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	LogLevel          slog.Level
	Provider          Provider
}

const (
	defaultHTTPAddr          = ":8080"
	defaultReadTimeout       = 5 * time.Second
	defaultReadHeaderTimeout = 5 * time.Second
	defaultWriteTimeout      = 10 * time.Second
	defaultIdleTimeout       = 60 * time.Second
	defaultShutdownTimeout   = 10 * time.Second
	defaultLogLevel          = slog.LevelInfo
	defaultProvider          = ProviderFake
)

// Load builds a Config from environment variables, then applies args as
// --provider flag overrides. Pass os.Getenv and os.Args[1:] in production.
func Load(args []string, getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:          defaultHTTPAddr,
		ReadTimeout:       defaultReadTimeout,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
		ShutdownTimeout:   defaultShutdownTimeout,
		LogLevel:          defaultLogLevel,
		Provider:          defaultProvider,
	}

	if v := getenv("HTTP_ADDR"); v != "" {
		cfg.HTTPAddr = v
	}

	var err error
	for _, d := range []struct {
		name string
		dst  *time.Duration
	}{
		{"HTTP_READ_TIMEOUT", &cfg.ReadTimeout},
		{"HTTP_READ_HEADER_TIMEOUT", &cfg.ReadHeaderTimeout},
		{"HTTP_WRITE_TIMEOUT", &cfg.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT", &cfg.IdleTimeout},
		{"HTTP_SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout},
	} {
		if *d.dst, err = durationEnv(getenv, d.name, *d.dst); err != nil {
			return Config{}, err
		}
	}

	if v := getenv("LOG_LEVEL"); v != "" {
		if cfg.LogLevel, err = parseLogLevel(v); err != nil {
			return Config{}, err
		}
	}

	if v := getenv("PROVIDER"); v != "" {
		cfg.Provider = Provider(strings.ToLower(v))
	}

	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	provider := fs.String("provider", string(cfg.Provider), "rate provider: fake or exchangeratedev (overrides PROVIDER env)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	cfg.Provider = Provider(strings.ToLower(*provider))

	if !cfg.Provider.valid() {
		return Config{}, fmt.Errorf("config: invalid PROVIDER/--provider %q: must be %q or %q", cfg.Provider, ProviderFake, ProviderExchangerateDev)
	}

	return cfg, nil
}

// durationEnv parses name as a positive time.Duration, or returns def if unset.
func durationEnv(getenv func(string) string, name string, def time.Duration) (time.Duration, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: invalid %s %q: %w", name, v, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("config: %s must be positive, got %q", name, v)
	}
	return d, nil
}

// parseLogLevel maps a level name (debug/info/warn/error) to slog.Level.
func parseLogLevel(v string) (slog.Level, error) {
	switch strings.ToLower(v) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("config: invalid LOG_LEVEL %q: must be debug, info, warn, or error", v)
	}
}
