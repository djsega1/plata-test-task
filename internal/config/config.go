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
	// outbound calls to the configured provider.
	RateLimitPerMinute int
	RateLimitPerHour   int

	// DispatchTickInterval drives cmd/server's real ticker — the only
	// mechanism that picks up backoff-deferred work and work created by
	// another replica. A POST's nudge covers the common case; the tick is
	// the fallback.
	DispatchTickInterval time.Duration
	// DispatchBatchSize is ClaimBatch's limit per pass.
	DispatchBatchSize int
	// DispatchPoolSize bounds requests in flight at once across a batch.
	DispatchPoolSize int
	// DispatchVisibilityTimeout is how long a claimed row may stay
	// in_progress before the reaper (folded into ClaimBatch) reclaims it.
	// Must comfortably exceed a provider adapter's own HTTP timeout.
	DispatchVisibilityTimeout time.Duration
	// DispatchBaseBackoff is Worker's floor retry delay — raised to a
	// failure's own Retry-After when that's longer, then jittered.
	DispatchBaseBackoff time.Duration

	// ProviderHTTPTimeout bounds internal/provider/exchangeratedev's HTTP
	// client (only used when Provider is exchangeratedev).
	ProviderHTTPTimeout time.Duration
	// ProviderHTTPMaxIdleConnsPerHost/IdleConnTimeout tune that same
	// client's connection reuse against the single upstream host — see
	// pkg/httpclient, which cmd/server builds the client through.
	ProviderHTTPMaxIdleConnsPerHost int
	ProviderHTTPIdleConnTimeout     time.Duration
	// FakeProviderMinDelay/MaxDelay simulate upstream latency when Provider
	// is fake; both zero (the default) matches cmd/server's production
	// wiring, which leaves them at zero on purpose — RateLimiter is the
	// single source of quota truth for the real dispatcher — but a demo
	// run can opt into some delay to make the async nature of the API
	// visible.
	FakeProviderMinDelay time.Duration
	FakeProviderMaxDelay time.Duration
	// FakeProviderRPMQuota, if positive, makes the fake provider enforce
	// its own per-minute quota independently of RateLimitPerMinute above.
	// 0 (the default) disables it.
	FakeProviderRPMQuota int
}

const (
	defaultHTTPAddr           = ":8080"
	defaultReadTimeout        = 5 * time.Second
	defaultReadHeaderTimeout  = 5 * time.Second
	defaultWriteTimeout       = 10 * time.Second
	defaultIdleTimeout        = 60 * time.Second
	defaultShutdownTimeout    = 10 * time.Second
	defaultLogLevel           = slog.LevelInfo
	defaultProvider           = ProviderExchangerateDev
	defaultStorage            = StoragePostgres
	defaultQuoteTTL           = 5 * time.Minute
	defaultRateLimitPerMinute = 12
	defaultRateLimitPerHour   = 100

	defaultDispatchTickInterval      = 5 * time.Second
	defaultDispatchBatchSize         = 100
	defaultDispatchPoolSize          = 8
	defaultDispatchVisibilityTimeout = 30 * time.Second
	defaultDispatchBaseBackoff       = 5 * time.Second

	defaultProviderHTTPTimeout = 5 * time.Second
	// defaultProviderHTTPMaxIdleConnsPerHost matches DispatchPoolSize's
	// default: that many workers is the most concurrent outbound calls
	// this service ever makes at once, so it's also the most connections
	// to the same upstream host worth keeping idle for reuse.
	defaultProviderHTTPMaxIdleConnsPerHost = 8
	// defaultProviderHTTPIdleConnTimeout matches net/http's own
	// DefaultTransport default (90s) — no evidence yet that this
	// workload needs a different one.
	defaultProviderHTTPIdleConnTimeout = 90 * time.Second
	// FakeProviderMinDelay/MaxDelay/RPMQuota default to zero (Go's zero
	// value) — no named constant needed for "off".
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

		DispatchTickInterval:      defaultDispatchTickInterval,
		DispatchBatchSize:         defaultDispatchBatchSize,
		DispatchPoolSize:          defaultDispatchPoolSize,
		DispatchVisibilityTimeout: defaultDispatchVisibilityTimeout,
		DispatchBaseBackoff:       defaultDispatchBaseBackoff,

		ProviderHTTPTimeout:             defaultProviderHTTPTimeout,
		ProviderHTTPMaxIdleConnsPerHost: defaultProviderHTTPMaxIdleConnsPerHost,
		ProviderHTTPIdleConnTimeout:     defaultProviderHTTPIdleConnTimeout,
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
		{"DISPATCH_TICK_INTERVAL", &cfg.DispatchTickInterval},
		{"DISPATCH_VISIBILITY_TIMEOUT", &cfg.DispatchVisibilityTimeout},
		{"DISPATCH_BASE_BACKOFF", &cfg.DispatchBaseBackoff},
		{"PROVIDER_HTTP_TIMEOUT", &cfg.ProviderHTTPTimeout},
		{"PROVIDER_HTTP_IDLE_CONN_TIMEOUT", &cfg.ProviderHTTPIdleConnTimeout},
	} {
		if *d.dst, err = durationEnv(getenv, d.name, *d.dst); err != nil {
			return Config{}, err
		}
	}

	for _, d := range []struct {
		name string
		dst  *time.Duration
	}{
		{"FAKE_PROVIDER_MIN_DELAY", &cfg.FakeProviderMinDelay},
		{"FAKE_PROVIDER_MAX_DELAY", &cfg.FakeProviderMaxDelay},
	} {
		if *d.dst, err = nonNegativeDurationEnv(getenv, d.name, *d.dst); err != nil {
			return Config{}, err
		}
	}

	for _, n := range []struct {
		name string
		dst  *int
	}{
		{"RATE_LIMIT_PER_MINUTE", &cfg.RateLimitPerMinute},
		{"RATE_LIMIT_PER_HOUR", &cfg.RateLimitPerHour},
		{"DISPATCH_BATCH_SIZE", &cfg.DispatchBatchSize},
		{"DISPATCH_POOL_SIZE", &cfg.DispatchPoolSize},
		{"PROVIDER_HTTP_MAX_IDLE_CONNS_PER_HOST", &cfg.ProviderHTTPMaxIdleConnsPerHost},
	} {
		if *n.dst, err = intEnv(getenv, n.name, *n.dst); err != nil {
			return Config{}, err
		}
	}

	if cfg.FakeProviderRPMQuota, err = nonNegativeIntEnv(getenv, "FAKE_PROVIDER_RPM_QUOTA", cfg.FakeProviderRPMQuota); err != nil {
		return Config{}, err
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

// nonNegativeDurationEnv is durationEnv but also accepts zero — for a
// duration where zero is a meaningful "disabled", not a misconfiguration.
func nonNegativeDurationEnv(getenv func(string) string, name string, def time.Duration) (time.Duration, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: invalid %s %q: %w", name, v, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("config: %s must not be negative, got %q", name, v)
	}
	return d, nil
}

// nonNegativeIntEnv is intEnv but also accepts zero — for a count where
// zero is a meaningful "disabled", not a misconfiguration.
func nonNegativeIntEnv(getenv func(string) string, name string, def int) (int, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: invalid %s %q: %w", name, v, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("config: %s must not be negative, got %q", name, v)
	}
	return n, nil
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
