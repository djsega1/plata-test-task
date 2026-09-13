package config

import (
	"errors"
	"flag"
	"log/slog"
	"testing"
	"time"
)

func fakeGetenv(m map[string]string) func(string) string {
	return func(key string) string {
		return m[key]
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil, fakeGetenv(nil))
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	want := Config{
		HTTPAddr:          defaultHTTPAddr,
		ReadTimeout:       defaultReadTimeout,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
		ShutdownTimeout:   defaultShutdownTimeout,
		LogLevel:          defaultLogLevel,
		Provider:          defaultProvider,
	}
	if cfg != want {
		t.Fatalf("Load() = %+v, want %+v", cfg, want)
	}
}

func TestLoadHTTPAddrOverride(t *testing.T) {
	cfg, err := Load(nil, fakeGetenv(map[string]string{"HTTP_ADDR": ":9090"}))
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":9090" {
		t.Fatalf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":9090")
	}
}

func TestLoadDurationOverrides(t *testing.T) {
	env := map[string]string{
		"HTTP_READ_TIMEOUT":        "1s",
		"HTTP_READ_HEADER_TIMEOUT": "2s",
		"HTTP_WRITE_TIMEOUT":       "3s",
		"HTTP_IDLE_TIMEOUT":        "4s",
		"HTTP_SHUTDOWN_TIMEOUT":    "5s",
	}
	cfg, err := Load(nil, fakeGetenv(env))
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	if cfg.ReadTimeout != time.Second {
		t.Errorf("ReadTimeout = %v, want %v", cfg.ReadTimeout, time.Second)
	}
	if cfg.ReadHeaderTimeout != 2*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want %v", cfg.ReadHeaderTimeout, 2*time.Second)
	}
	if cfg.WriteTimeout != 3*time.Second {
		t.Errorf("WriteTimeout = %v, want %v", cfg.WriteTimeout, 3*time.Second)
	}
	if cfg.IdleTimeout != 4*time.Second {
		t.Errorf("IdleTimeout = %v, want %v", cfg.IdleTimeout, 4*time.Second)
	}
	if cfg.ShutdownTimeout != 5*time.Second {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, 5*time.Second)
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
			if _, err := Load(nil, fakeGetenv(tt.env)); err == nil {
				t.Fatal("Load: expected error, got nil")
			}
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
			cfg, err := Load(nil, fakeGetenv(map[string]string{"LOG_LEVEL": tt.env}))
			if err != nil {
				t.Fatalf("Load: unexpected error: %v", err)
			}
			if cfg.LogLevel != tt.want {
				t.Fatalf("LogLevel = %v, want %v", cfg.LogLevel, tt.want)
			}
		})
	}
}

func TestLoadLogLevelInvalid(t *testing.T) {
	if _, err := Load(nil, fakeGetenv(map[string]string{"LOG_LEVEL": "verbose"})); err == nil {
		t.Fatal("Load: expected error, got nil")
	}
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
			cfg, err := Load(nil, fakeGetenv(map[string]string{"PROVIDER": tt.env}))
			if err != nil {
				t.Fatalf("Load: unexpected error: %v", err)
			}
			if cfg.Provider != tt.want {
				t.Fatalf("Provider = %v, want %v", cfg.Provider, tt.want)
			}
		})
	}
}

func TestLoadProviderEnvInvalid(t *testing.T) {
	if _, err := Load(nil, fakeGetenv(map[string]string{"PROVIDER": "acme"})); err == nil {
		t.Fatal("Load: expected error, got nil")
	}
}

func TestLoadProviderFlagOverridesEnv(t *testing.T) {
	env := map[string]string{"PROVIDER": "fake"}
	cfg, err := Load([]string{"--provider=exchangeratedev"}, fakeGetenv(env))
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cfg.Provider != ProviderExchangerateDev {
		t.Fatalf("Provider = %v, want %v", cfg.Provider, ProviderExchangerateDev)
	}
}

func TestLoadProviderFlagWithoutEnv(t *testing.T) {
	cfg, err := Load([]string{"--provider=exchangeratedev"}, fakeGetenv(nil))
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cfg.Provider != ProviderExchangerateDev {
		t.Fatalf("Provider = %v, want %v", cfg.Provider, ProviderExchangerateDev)
	}
}

func TestLoadProviderFlagInvalid(t *testing.T) {
	if _, err := Load([]string{"--provider=acme"}, fakeGetenv(nil)); err == nil {
		t.Fatal("Load: expected error, got nil")
	}
}

func TestLoadUnknownFlag(t *testing.T) {
	_, err := Load([]string{"-h"}, fakeGetenv(nil))
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("Load: error = %v, want flag.ErrHelp", err)
	}
}
