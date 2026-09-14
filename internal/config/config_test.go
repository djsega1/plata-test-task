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
		HTTPAddr:          defaultHTTPAddr,
		ReadTimeout:       defaultReadTimeout,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
		ShutdownTimeout:   defaultShutdownTimeout,
		LogLevel:          defaultLogLevel,
		Provider:          defaultProvider,
		Storage:           defaultStorage,
		DatabaseURL:       "postgres://localhost/quotes",
	}
	assert.Equal(t, want, cfg)
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
