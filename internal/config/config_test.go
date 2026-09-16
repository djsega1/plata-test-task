package config

import (
	"flag"
	"log/slog"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeGetenv(m map[string]string) func(string) string {
	return func(key string) string {
		return m[key]
	}
}

// baseEnv is the minimum env that satisfies validation (DATABASE_URL is
// required once STORAGE defaults to postgres). Tests that don't care about
// a particular variable start from this and override with withEnv.
func baseEnv() map[string]string {
	return map[string]string{"DATABASE_URL": "postgres://localhost/quotes"}
}

func withEnv(overrides map[string]string) map[string]string {
	env := baseEnv()
	maps.Copy(env, overrides)
	return env
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil, fakeGetenv(baseEnv()))
	require.NoError(t, err)

	want := Config{
		HTTPAddr:           defaultHTTPAddr,
		ReadTimeout:        defaultReadTimeout,
		ReadHeaderTimeout:  defaultReadHeaderTimeout,
		WriteTimeout:       defaultWriteTimeout,
		IdleTimeout:        defaultIdleTimeout,
		ShutdownTimeout:    defaultShutdownTimeout,
		LogLevel:           defaultLogLevel,
		Provider:           defaultProvider,
		Storage:            defaultStorage,
		DatabaseURL:        "postgres://localhost/quotes",
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
	assert.Equal(t, want, cfg)
}

func TestLoadProviderHTTPTimeoutOverride(t *testing.T) {
	cfg, err := Load(nil, fakeGetenv(withEnv(map[string]string{"PROVIDER_HTTP_TIMEOUT": "2s"})))
	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, cfg.ProviderHTTPTimeout)
}

func TestLoadProviderHTTPMaxIdleConnsPerHostOverride(t *testing.T) {
	cfg, err := Load(nil, fakeGetenv(withEnv(map[string]string{"PROVIDER_HTTP_MAX_IDLE_CONNS_PER_HOST": "20"})))
	require.NoError(t, err)
	assert.Equal(t, 20, cfg.ProviderHTTPMaxIdleConnsPerHost)
}

func TestLoadProviderHTTPMaxIdleConnsPerHostInvalid(t *testing.T) {
	_, err := Load(nil, fakeGetenv(withEnv(map[string]string{"PROVIDER_HTTP_MAX_IDLE_CONNS_PER_HOST": "0"})))
	assert.Error(t, err)
}

func TestLoadProviderHTTPIdleConnTimeoutOverride(t *testing.T) {
	cfg, err := Load(nil, fakeGetenv(withEnv(map[string]string{"PROVIDER_HTTP_IDLE_CONN_TIMEOUT": "30s"})))
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, cfg.ProviderHTTPIdleConnTimeout)
}

func TestLoadFakeProviderDelayOverride(t *testing.T) {
	cfg, err := Load(nil, fakeGetenv(withEnv(map[string]string{
		"FAKE_PROVIDER_MIN_DELAY": "10ms",
		"FAKE_PROVIDER_MAX_DELAY": "50ms",
	})))
	require.NoError(t, err)
	assert.Equal(t, 10*time.Millisecond, cfg.FakeProviderMinDelay)
	assert.Equal(t, 50*time.Millisecond, cfg.FakeProviderMaxDelay)
}

func TestLoadFakeProviderDelayZeroIsValid(t *testing.T) {
	cfg, err := Load(nil, fakeGetenv(withEnv(map[string]string{"FAKE_PROVIDER_MIN_DELAY": "0s"})))
	require.NoError(t, err)
	assert.Zero(t, cfg.FakeProviderMinDelay)
}

func TestLoadFakeProviderDelayNegativeInvalid(t *testing.T) {
	_, err := Load(nil, fakeGetenv(withEnv(map[string]string{"FAKE_PROVIDER_MIN_DELAY": "-1s"})))
	assert.Error(t, err)
}

func TestLoadFakeProviderRPMQuotaOverride(t *testing.T) {
	cfg, err := Load(nil, fakeGetenv(withEnv(map[string]string{"FAKE_PROVIDER_RPM_QUOTA": "5"})))
	require.NoError(t, err)
	assert.Equal(t, 5, cfg.FakeProviderRPMQuota)
}

func TestLoadFakeProviderRPMQuotaZeroIsValid(t *testing.T) {
	cfg, err := Load(nil, fakeGetenv(withEnv(map[string]string{"FAKE_PROVIDER_RPM_QUOTA": "0"})))
	require.NoError(t, err)
	assert.Zero(t, cfg.FakeProviderRPMQuota)
}

func TestLoadFakeProviderRPMQuotaNegativeInvalid(t *testing.T) {
	_, err := Load(nil, fakeGetenv(withEnv(map[string]string{"FAKE_PROVIDER_RPM_QUOTA": "-1"})))
	assert.Error(t, err)
}

func TestLoadHTTPAddrOverride(t *testing.T) {
	cfg, err := Load(nil, fakeGetenv(withEnv(map[string]string{"HTTP_ADDR": ":9090"})))
	require.NoError(t, err)
	assert.Equal(t, ":9090", cfg.HTTPAddr)
}

func TestLoadDurationOverrides(t *testing.T) {
	env := withEnv(map[string]string{
		"HTTP_READ_TIMEOUT":        "1s",
		"HTTP_READ_HEADER_TIMEOUT": "2s",
		"HTTP_WRITE_TIMEOUT":       "3s",
		"HTTP_IDLE_TIMEOUT":        "4s",
		"HTTP_SHUTDOWN_TIMEOUT":    "5s",
	})
	cfg, err := Load(nil, fakeGetenv(env))
	require.NoError(t, err)

	assert.Equal(t, time.Second, cfg.ReadTimeout)
	assert.Equal(t, 2*time.Second, cfg.ReadHeaderTimeout)
	assert.Equal(t, 3*time.Second, cfg.WriteTimeout)
	assert.Equal(t, 4*time.Second, cfg.IdleTimeout)
	assert.Equal(t, 5*time.Second, cfg.ShutdownTimeout)
}

func TestLoadQuoteTTLOverride(t *testing.T) {
	cfg, err := Load(nil, fakeGetenv(withEnv(map[string]string{"QUOTE_TTL": "10m"})))
	require.NoError(t, err)
	assert.Equal(t, 10*time.Minute, cfg.QuoteTTL)
}

func TestLoadRateLimitOverrides(t *testing.T) {
	env := withEnv(map[string]string{
		"RATE_LIMIT_PER_MINUTE": "5",
		"RATE_LIMIT_PER_HOUR":   "50",
	})
	cfg, err := Load(nil, fakeGetenv(env))
	require.NoError(t, err)

	assert.Equal(t, 5, cfg.RateLimitPerMinute)
	assert.Equal(t, 50, cfg.RateLimitPerHour)
}

func TestLoadDispatchOverrides(t *testing.T) {
	env := withEnv(map[string]string{
		"DISPATCH_TICK_INTERVAL":      "10s",
		"DISPATCH_BATCH_SIZE":         "50",
		"DISPATCH_POOL_SIZE":          "4",
		"DISPATCH_VISIBILITY_TIMEOUT": "1m",
		"DISPATCH_BASE_BACKOFF":       "2s",
	})
	cfg, err := Load(nil, fakeGetenv(env))
	require.NoError(t, err)

	assert.Equal(t, 10*time.Second, cfg.DispatchTickInterval)
	assert.Equal(t, 50, cfg.DispatchBatchSize)
	assert.Equal(t, 4, cfg.DispatchPoolSize)
	assert.Equal(t, time.Minute, cfg.DispatchVisibilityTimeout)
	assert.Equal(t, 2*time.Second, cfg.DispatchBaseBackoff)
}

func TestLoadRateLimitInvalid(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"not a number", map[string]string{"RATE_LIMIT_PER_MINUTE": "many"}},
		{"zero", map[string]string{"RATE_LIMIT_PER_HOUR": "0"}},
		{"negative", map[string]string{"RATE_LIMIT_PER_MINUTE": "-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(nil, fakeGetenv(withEnv(tt.env)))
			require.Error(t, err)
		})
	}
}

func TestLoadDurationInvalid(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"not a duration", map[string]string{"HTTP_READ_TIMEOUT": "soon"}},
		{"zero", map[string]string{"HTTP_WRITE_TIMEOUT": "0s"}},
		{"negative", map[string]string{"HTTP_IDLE_TIMEOUT": "-1s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(nil, fakeGetenv(withEnv(tt.env)))
			require.Error(t, err)
		})
	}
}

func TestLoadLogLevel(t *testing.T) {
	tests := []struct {
		env  string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"DEBUG", slog.LevelDebug},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			cfg, err := Load(nil, fakeGetenv(withEnv(map[string]string{"LOG_LEVEL": tt.env})))
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.LogLevel)
		})
	}
}

func TestLoadLogLevelInvalid(t *testing.T) {
	_, err := Load(nil, fakeGetenv(withEnv(map[string]string{"LOG_LEVEL": "verbose"})))
	require.Error(t, err)
}

func TestLoadProviderEnv(t *testing.T) {
	tests := []struct {
		env  string
		want Provider
	}{
		{"fake", ProviderFake},
		{"exchangeratedev", ProviderExchangerateDev},
		{"Fake", ProviderFake},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			cfg, err := Load(nil, fakeGetenv(withEnv(map[string]string{"PROVIDER": tt.env})))
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Provider)
		})
	}
}

func TestLoadProviderEnvInvalid(t *testing.T) {
	_, err := Load(nil, fakeGetenv(withEnv(map[string]string{"PROVIDER": "acme"})))
	require.Error(t, err)
}

func TestLoadProviderFlagOverridesEnv(t *testing.T) {
	cfg, err := Load([]string{"--provider=exchangeratedev"}, fakeGetenv(withEnv(map[string]string{"PROVIDER": "fake"})))
	require.NoError(t, err)
	assert.Equal(t, ProviderExchangerateDev, cfg.Provider)
}

func TestLoadProviderFlagWithoutEnv(t *testing.T) {
	cfg, err := Load([]string{"--provider=exchangeratedev"}, fakeGetenv(baseEnv()))
	require.NoError(t, err)
	assert.Equal(t, ProviderExchangerateDev, cfg.Provider)
}

func TestLoadProviderFlagInvalid(t *testing.T) {
	_, err := Load([]string{"--provider=acme"}, fakeGetenv(baseEnv()))
	require.Error(t, err)
}

func TestLoadStorageDefaultRequiresDatabaseURL(t *testing.T) {
	_, err := Load(nil, fakeGetenv(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

func TestLoadStorageEnv(t *testing.T) {
	cfg, err := Load(nil, fakeGetenv(withEnv(map[string]string{"STORAGE": "Postgres"})))
	require.NoError(t, err)
	assert.Equal(t, StoragePostgres, cfg.Storage)
}

func TestLoadStorageInvalid(t *testing.T) {
	_, err := Load(nil, fakeGetenv(withEnv(map[string]string{"STORAGE": "memory"})))
	require.Error(t, err)
}

func TestLoadStorageFlagOverridesEnv(t *testing.T) {
	cfg, err := Load([]string{"--storage=postgres"}, fakeGetenv(withEnv(map[string]string{"STORAGE": "postgres"})))
	require.NoError(t, err)
	assert.Equal(t, StoragePostgres, cfg.Storage)
}

func TestLoadDatabaseURL(t *testing.T) {
	cfg, err := Load(nil, fakeGetenv(map[string]string{"DATABASE_URL": "postgres://user@host/db"}))
	require.NoError(t, err)
	assert.Equal(t, "postgres://user@host/db", cfg.DatabaseURL)
}

func TestLoadUnknownFlag(t *testing.T) {
	_, err := Load([]string{"-h"}, fakeGetenv(baseEnv()))
	require.ErrorIs(t, err, flag.ErrHelp)
}
