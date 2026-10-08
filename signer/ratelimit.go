package signer

import (
	"sync"
	"time"
)

// Limiter is a token bucket per rule.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	rate   Rate
	tokens float64
	last   time.Time
}

func NewLimiter() *Limiter {
	return &Limiter{buckets: map[string]*bucket{}}
}

// Allow takes one token from rule's bucket at now. A bucket starts full
// (N tokens) and refills at N per r.Per; a changed rate resets it.
func (l *Limiter) Allow(rule string, r Rate, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.buckets[rule]
	if b == nil || b.rate != r {
		b = &bucket{rate: r, tokens: float64(r.N), last: now}
		l.buckets[rule] = b
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += float64(r.N) * float64(elapsed) / float64(r.Per)
		if b.tokens > float64(r.N) {
			b.tokens = float64(r.N)
		}
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
