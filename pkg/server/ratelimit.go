package server

import (
	"sync"
	"time"
)

// fixedWindowLimiter allows at most limit events per key in each one-minute window.
//
// Keys are usernames (login) or a single constant (registration), never client IPs: the IP the
// backend sees is the frontend nginx pod, and X-Forwarded-For is set by whoever sends the request.
// The count lives in this pod only, so N replicas allow N times the limit. That is enough to turn
// a password-guessing loop into a slow one; a shared limit would need Redis or the edge.
type fixedWindowLimiter struct {
	limit int
	now   func() time.Time

	mu      sync.Mutex
	windows map[string]limiterWindow
}

type limiterWindow struct {
	start time.Time
	count int
}

const limiterWindowLength = time.Minute

// limiterMaxKeys bounds memory. When it is reached, expired windows are dropped first; if every
// window is still live, the oldest one is evicted to make room. Callers also cap the key length.
//
// Why evict and not refuse: refusing new keys would let anyone lock every user out of login by
// cycling through 10 000 made-up usernames a minute. Evicting keeps login open for everyone; the
// price for an attacker who wants more guesses on one account is 10 000 requests per extra window.
const limiterMaxKeys = 10000

func newFixedWindowLimiter(limit int) *fixedWindowLimiter {
	return &fixedWindowLimiter{
		limit:   limit,
		now:     time.Now,
		windows: make(map[string]limiterWindow),
	}
}

// allow records one event for key. It returns false, plus how long until the window resets,
// when the key has already used its limit.
func (l *fixedWindowLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	window, found := l.windows[key]
	if !found || now.Sub(window.start) >= limiterWindowLength {
		if !found && len(l.windows) >= limiterMaxKeys {
			l.dropExpired(now)
			if len(l.windows) >= limiterMaxKeys {
				l.evictOldest()
			}
		}
		window = limiterWindow{start: now}
	}

	if window.count >= l.limit {
		return false, window.start.Add(limiterWindowLength).Sub(now)
	}

	window.count++
	l.windows[key] = window
	return true, 0
}

// evictOldest removes the window that started first. O(n), and only runs while the map is full.
func (l *fixedWindowLimiter) evictOldest() {
	var oldestKey string
	var oldestStart time.Time
	first := true
	for key, window := range l.windows {
		if first || window.start.Before(oldestStart) {
			oldestKey, oldestStart, first = key, window.start, false
		}
	}
	if !first {
		delete(l.windows, oldestKey)
	}
}

func (l *fixedWindowLimiter) dropExpired(now time.Time) {
	for key, window := range l.windows {
		if now.Sub(window.start) >= limiterWindowLength {
			delete(l.windows, key)
		}
	}
}
