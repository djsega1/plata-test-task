package main

import (
	"fmt"
	"os"

	"github.com/djsega1/plata-test-task/internal/config"
	"github.com/djsega1/plata-test-task/internal/provider/exchangeratedev"
	"github.com/djsega1/plata-test-task/internal/provider/fake"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
	"github.com/djsega1/plata-test-task/pkg/clock"
	"github.com/djsega1/plata-test-task/pkg/httpclient"
)

// newRateProvider picks the RateProvider adapter cfg.Provider names. The
// upstream API key, if any, comes from the environment only — never a flag
// or config file — so a reviewer running PROVIDER=fake never needs one.
func newRateProvider(cfg config.Config) (quotes.RateProvider, error) {
	switch cfg.Provider {
	case config.ProviderFake:
		// Delay/quota fields default to zero (RateLimiter is the real quota
		// source); a demo run can opt in via env to show async behavior.
		return fake.NewFakeRateProvider(
			clock.NewSystemClock(), cfg.FakeProviderMinDelay, cfg.FakeProviderMaxDelay, cfg.FakeProviderRPMQuota, cfg.QuoteTTL,
		), nil
	case config.ProviderExchangerateDev:
		client := httpclient.New(cfg.ProviderHTTPTimeout, cfg.ProviderHTTPMaxIdleConnsPerHost, cfg.ProviderHTTPIdleConnTimeout)
		return exchangeratedev.NewExchangerateDevProvider(
			exchangeratedev.DefaultBaseURL, os.Getenv("PROVIDER_API_KEY"), client, cfg.QuoteTTL,
		), nil
	default:
		// Unreachable today (config.Load rejects other values); kept as a
		// real error so a future Provider value fails loudly at startup.
		return nil, fmt.Errorf("no adapter wired for provider %q", cfg.Provider)
	}
}
