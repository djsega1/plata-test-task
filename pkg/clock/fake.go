package clock

import (
	"sync"
	"time"
)

// FakeClock is a time source for tests. Now returns the last time set.
type FakeClock struct {
	mu          sync.Mutex
	currentTime time.Time
}

// NewFakeClock returns a *FakeClock starting at currentTime. It is a pointer because
// Set/Advance change shared state: every holder must see the same time.
func NewFakeClock(currentTime time.Time) *FakeClock {
	return &FakeClock{currentTime: currentTime}
}

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.currentTime
}

// Set jumps the clock to t.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentTime = t
}

// Advance moves the clock forward by d. A negative d moves it back.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentTime = c.currentTime.Add(d)
}
