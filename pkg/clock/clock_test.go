package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/djsega1/plata-test-task/pkg/clock"
)

func TestSystemClock_Now(t *testing.T) {
	c := clock.NewSystemClock()

	before := time.Now()
	got := c.Now()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Errorf("Now() = %v, want between %v and %v", got, before, after)
	}
}

func TestFakeClock_Set(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := clock.NewFakeClock(start)

	if got := c.Now(); !got.Equal(start) {
		t.Errorf("Now() = %v, want %v", got, start)
	}

	next := start.Add(24 * time.Hour)
	c.Set(next)
	if got := c.Now(); !got.Equal(next) {
		t.Errorf("after Set: Now() = %v, want %v", got, next)
	}
}

func TestFakeClock_Advance(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := clock.NewFakeClock(start)

	c.Advance(90 * time.Second)
	want := start.Add(90 * time.Second)
	if got := c.Now(); !got.Equal(want) {
		t.Errorf("Now() = %v, want %v", got, want)
	}

	c.Advance(-30 * time.Second) // negative moves it back
	want = want.Add(-30 * time.Second)
	if got := c.Now(); !got.Equal(want) {
		t.Errorf("after negative Advance: Now() = %v, want %v", got, want)
	}
}

// checks Advance is safe under concurrent calls; run with -race.
func TestFakeClock_ConcurrentAdvance(t *testing.T) {
	start := time.Unix(0, 0)
	c := clock.NewFakeClock(start)

	const goroutines = 50
	const perGoroutine = 100

	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range perGoroutine {
				c.Advance(time.Second)
			}
		})
	}
	wg.Wait()

	want := start.Add(time.Duration(goroutines*perGoroutine) * time.Second)
	if got := c.Now(); !got.Equal(want) {
		t.Errorf("Now() = %v, want %v", got, want)
	}
}
