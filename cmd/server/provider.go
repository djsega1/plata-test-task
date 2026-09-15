package main

import (
	"fmt"
	"os"

	"github.com/djsega1/plata-test-task/internal/config"
	"github.com/djsega1/plata-test-task/internal/provider/exchangeratedev"
	"github.com/djsega1/plata-test-task/internal/provider/fake"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
	"github.com/djsega1/plata-test-task/pkg/clock"
)

// newRateProvider picks the RateProvider adapter cfg.Provider names. The
// upstream API key, if any, comes from the environment only (CLAUDE.md) —
// never a flag or config file — so a reviewer running PROVIDER=fake never
// needs one.
func newRateProvider(cfg config.Config) (quotes.RateProvider, error) {
	switch cfg.Provider {
	case config.ProviderFake:
		// No simulated delay or internal quota: RateLimiter is the single
		// source of quota truth for the real dispatcher, unlike in tests
		// that construct FakeRateProvider directly to exercise it.
		return fake.NewFakeRateProvider(clock.NewSystemClock(), 0, 0, 0, cfg.QuoteTTL), nil
	case config.ProviderExchangerateDev:
		return exchangeratedev.NewExchangerateDevProvider(
			exchangeratedev.DefaultBaseURL, os.Getenv("PROVIDER_API_KEY"), nil, cfg.QuoteTTL,
		), nil
	default:
		// Unreachable in practice: config.Load already rejects any other
		// value. Kept as a real error, not a panic, so a future Provider
		// value added to config without a matching case here fails loudly
		// at startup instead of nil-pointer-dereferencing later.
		return nil, fmt.Errorf("no adapter wired for provider %q", cfg.Provider)
	}
}
