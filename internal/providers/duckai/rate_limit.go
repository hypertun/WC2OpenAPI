package duckai

import (
	"sync"
	"time"
)

type RateLimiter struct {
	mu          sync.Mutex
	timestamps  []time.Time
	maxPerWin   int
	window      time.Duration
	minInterval time.Duration
	lastReq     time.Time
}

func NewRateLimiter(maxPerWindow int, window time.Duration, minInterval time.Duration) *RateLimiter {
	return &RateLimiter{
		timestamps:  make([]time.Time, 0, maxPerWindow),
		maxPerWin:   maxPerWindow,
		window:      window,
		minInterval: minInterval,
	}
}

func (r *RateLimiter) cleanup(now time.Time) {
	cutoff := now.Add(-r.window)
	j := 0
	for _, t := range r.timestamps {
		if t.After(cutoff) {
			r.timestamps[j] = t
			j++
		}
	}
	r.timestamps = r.timestamps[:j]
}

func (r *RateLimiter) WaitIfNeeded() {
	for {
		wait := r.computeWait()
		if wait <= 0 {
			break
		}
		time.Sleep(wait)
	}
}

func (r *RateLimiter) computeWait() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	r.cleanup(now)

	if len(r.timestamps) >= r.maxPerWin {
		oldest := r.timestamps[0]
		if waitUntil := oldest.Add(r.window); now.Before(waitUntil) {
			return waitUntil.Sub(now) + 100*time.Millisecond
		}
		r.cleanup(time.Now())
	}

	if elapsed := now.Sub(r.lastReq); elapsed < r.minInterval {
		return r.minInterval - elapsed
	}

	r.timestamps = append(r.timestamps, now)
	r.lastReq = now
	return 0
}
