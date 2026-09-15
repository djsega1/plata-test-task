package config

import (
	"flag"
	"fmt"
	"log/slog"
	"strconv"
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

// Storage selects which Repository adapter cmd/server wires up. Only
// postgres exists so far; memory is for use-case tests (CLAUDE.md), not
// yet a runtime option.
type Storage string

const StoragePostgres Storage = "postgres"

func (s Storage) valid() bool {
	return s == StoragePostgres
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
	Storage           Storage
	DatabaseURL       string

	// QuoteTTL is the StaleAfter fallback an adapter uses for any quality
	// it doesn't otherwise recognize.
	QuoteTTL time.Duration
	// RateLimitPerMinute/RateLimitPerHour bound the dispatcher's own
	// outbound calls to the configured provider (docs/design.md §2).
	RateLimitPerMinute int
	RateLimitPerHour   int
}

const (
	defaultHTTPAddr           = ":8080"
	defaultReadTimeout        = 5 * time.Second
	defaultReadHeaderTimeout  = 5 * time.Second
	defaultWriteTimeout       = 10 * time.Second
	defaultIdleTimeout        = 60 * time.Second
	defaultShutdownTimeout    = 10 * time.Second
	defaultLogLevel           = slog.LevelInfo
	defaultProvider           = ProviderFake
	defaultStorage            = StoragePostgres
	defaultQuoteTTL           = 5 * time.Minute
	defaultRateLimitPerMinute = 12
	defaultRateLimitPerHour   = 100
)

// Load builds a Config from environment variables, then applies args as
// --provider flag overrides. Pass os.Getenv and os.Args[1:] in production.
func Load(args []string, getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:           defaultHTTPAddr,
		ReadTimeout:        defaultReadTimeout,
		ReadHeaderTimeout:  defaultReadHeaderTimeout,
		WriteTimeout:       defaultWriteTimeout,
		IdleTimeout:        defaultIdleTimeout,
		ShutdownTimeout:    defaultShutdownTimeout,
		LogLevel:           defaultLogLevel,
		Provider:           defaultProvider,
		Storage:            defaultStorage,
		QuoteTTL:           defaultQuoteTTL,
		RateLimitPerMinute: defaultRateLimitPerMinute,
		RateLimitPerHour:   defaultRateLimitPerHour,
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
		{"QUOTE_TTL", &cfg.QuoteTTL},
	} {
		if *d.dst, err = durationEnv(getenv, d.name, *d.dst); err != nil {
			return Config{}, err
		}
	}

	for _, n := range []struct {
		name string
		dst  *int
	}{
		{"RATE_LIMIT_PER_MINUTE", &cfg.RateLimitPerMinute},
		{"RATE_LIMIT_PER_HOUR", &cfg.RateLimitPerHour},
	} {
		if *n.dst, err = intEnv(getenv, n.name, *n.dst); err != nil {
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

	if v := getenv("STORAGE"); v != "" {
		cfg.Storage = Storage(strings.ToLower(v))
	}

	cfg.DatabaseURL = getenv("DATABASE_URL")

	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	provider := fs.String("provider", string(cfg.Provider), "rate provider: fake or exchangeratedev (overrides PROVIDER env)")
	storage := fs.String("storage", string(cfg.Storage), "repository backend: postgres (overrides STORAGE env)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	cfg.Provider = Provider(strings.ToLower(*provider))
	cfg.Storage = Storage(strings.ToLower(*storage))

	if !cfg.Provider.valid() {
		return Config{}, fmt.Errorf("config: invalid PROVIDER/--provider %q: must be %q or %q", cfg.Provider, ProviderFake, ProviderExchangerateDev)
	}
	if !cfg.Storage.valid() {
		return Config{}, fmt.Errorf("config: invalid STORAGE/--storage %q: must be %q", cfg.Storage, StoragePostgres)
	}
	if cfg.Storage == StoragePostgres && cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("config: DATABASE_URL is required when STORAGE/--storage is %q", StoragePostgres)
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

// intEnv parses name as a positive int, or returns def if unset.
func intEnv(getenv func(string) string, name string, def int) (int, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: invalid %s %q: %w", name, v, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("config: %s must be positive, got %q", name, v)
	}
	return n, nil
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
