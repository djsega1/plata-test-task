package quotes

import (
	"sync"
	"time"
)

// RateLimiter bounds the dispatcher's own outbound provider calls to two
// independent sliding windows. Self-counted: it tracks its own call history
// rather than trusting a provider's response headers.
type RateLimiter struct {
	perMinute int // 0 means no limit
	perHour   int // 0 means no limit

	mu    sync.Mutex
	calls []time.Time // ascending, pruned to the last hour
}

func NewRateLimiter(perMinute, perHour int) *RateLimiter {
	return &RateLimiter{perMinute: perMinute, perHour: perHour}
}

// Allow reports whether a call is permitted at now, and records it if so.
// When it isn't, retryAfter is how long until the window that's currently
// full has room again.
func (r *RateLimiter) Allow(now time.Time) (ok bool, retryAfter time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	hourCutoff := now.Add(-time.Hour)
	minuteCutoff := now.Add(-time.Minute)

	// One pass over calls does both jobs: prune anything older than an
	// hour, and (since calls stays sorted ascending) find the oldest call
	// still inside the last minute, i.e. the first one kept that's also
	// after minuteCutoff.
	kept := r.calls[:0]
	var inMinute int
	var oldestInMinute time.Time
	for _, t := range r.calls {
		if !t.After(hourCutoff) {
			continue
		}
		kept = append(kept, t)
		if t.After(minuteCutoff) {
			if inMinute == 0 {
				oldestInMinute = t
			}
			inMinute++
		}
	}
	r.calls = kept

	if r.perHour > 0 && len(r.calls) >= r.perHour {
		return false, r.calls[0].Add(time.Hour).Sub(now)
	}
	if r.perMinute > 0 && inMinute >= r.perMinute {
		return false, oldestInMinute.Add(time.Minute).Sub(now)
	}

	r.calls = append(r.calls, now)
	return true, 0
}
