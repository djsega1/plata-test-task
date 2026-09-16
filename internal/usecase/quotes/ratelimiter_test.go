package quotes_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
)

func TestRateLimiter_AllowsUpToPerMinute(t *testing.T) {
	limiter := quotes.NewRateLimiter(2, 100)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	ok, _ := limiter.Allow(now)
	require.True(t, ok)
	ok, _ = limiter.Allow(now)
	require.True(t, ok)

	ok, retryAfter := limiter.Allow(now)
	assert.False(t, ok)
	assert.Equal(t, time.Minute, retryAfter)
}

func TestRateLimiter_MinuteWindowResets(t *testing.T) {
	limiter := quotes.NewRateLimiter(1, 100)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	ok, _ := limiter.Allow(now)
	require.True(t, ok)

	ok, _ = limiter.Allow(now.Add(59 * time.Second))
	assert.False(t, ok, "still inside the same minute window")

	ok, _ = limiter.Allow(now.Add(61 * time.Second))
	assert.True(t, ok, "the first call has aged out of the minute window")
}

func TestRateLimiter_PerHourAcrossMinuteWindows(t *testing.T) {
	limiter := quotes.NewRateLimiter(1, 3)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := range 3 {
		ok, _ := limiter.Allow(now.Add(time.Duration(i) * time.Minute))
		require.True(t, ok, "call %d", i)
	}

	ok, retryAfter := limiter.Allow(now.Add(3 * time.Minute))
	assert.False(t, ok, "hourly budget is exhausted even though each call was its own minute window")
	assert.Equal(t, 57*time.Minute, retryAfter)
}

func TestRateLimiter_ZeroMeansNoLimit(t *testing.T) {
	limiter := quotes.NewRateLimiter(0, 0)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for range 1000 {
		ok, _ := limiter.Allow(now)
		require.True(t, ok)
	}
}

func TestRateLimiter_CoolDownBlocksUntilExpiry(t *testing.T) {
	limiter := quotes.NewRateLimiter(100, 1000)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	limiter.CoolDown(now.Add(60 * time.Second))

	ok, retryAfter := limiter.Allow(now)
	assert.False(t, ok, "an active cooldown must block a call even with room left in both windows")
	assert.Equal(t, 60*time.Second, retryAfter)

	ok, _ = limiter.Allow(now.Add(59 * time.Second))
	assert.False(t, ok, "still inside the cooldown")

	ok, _ = limiter.Allow(now.Add(61 * time.Second))
	assert.True(t, ok, "cooldown has expired")
}

func TestRateLimiter_CoolDownNeverShortensAnExistingOne(t *testing.T) {
	limiter := quotes.NewRateLimiter(100, 1000)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	limiter.CoolDown(now.Add(60 * time.Second))
	limiter.CoolDown(now.Add(10 * time.Second)) // shorter: must not override the longer one

	ok, _ := limiter.Allow(now.Add(30 * time.Second))
	assert.False(t, ok, "the earlier, longer cooldown must still be in effect")
}

func TestRateLimiter_ConcurrentAllow(t *testing.T) {
	const quota = 5
	const callers = 20
	limiter := quotes.NewRateLimiter(quota, 1000)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	var allowed, blocked int64
	for range callers {
		wg.Go(func() {
			if ok, _ := limiter.Allow(now); ok {
				atomic.AddInt64(&allowed, 1)
			} else {
				atomic.AddInt64(&blocked, 1)
			}
		})
	}
	wg.Wait()

	assert.EqualValues(t, quota, allowed)
	assert.EqualValues(t, callers-quota, blocked)
}
