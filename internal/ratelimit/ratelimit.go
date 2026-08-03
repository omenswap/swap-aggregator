package ratelimit

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Limiter is a token bucket. A rate of zero means no limit.
type Limiter struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func New(rate, burst float64, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	if burst < 1 {
		burst = 1
	}
	return &Limiter{rate: rate, burst: burst, tokens: burst, last: now(), now: now}
}

// reserve takes a token and reports how long the caller must wait to use it.
func (l *Limiter) reserve() time.Duration {
	if l == nil || l.rate <= 0 {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens += elapsed.Seconds() * l.rate
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.last = now
	}
	l.tokens--
	if l.tokens >= 0 {
		return 0
	}
	return time.Duration(-l.tokens / l.rate * float64(time.Second))
}

type Transport struct {
	base    http.RoundTripper
	limiter *Limiter

	mu       sync.Mutex
	cooldown time.Time
}

// NewTransport paces every request through the limiter and, when a provider
// answers 429, stops sending until its Retry-After has passed.
func NewTransport(base http.RoundTripper, rate, burst float64) *Transport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &Transport{base: base, limiter: New(rate, burst, nil)}
}

func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.mu.Lock()
	until := t.cooldown
	t.mu.Unlock()
	if wait := time.Until(until); wait > 0 {
		return nil, fmt.Errorf("rate limited, retry in %s", wait.Round(time.Second))
	}

	if delay := t.limiter.reserve(); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-timer.C:
		}
	}

	resp, err := t.base.RoundTrip(r)
	if err == nil && resp.StatusCode == http.StatusTooManyRequests {
		after := resp.Header.Get("Retry-After")
		if after == "" {
			// BitcoinVN sends its own header instead of the standard one.
			after = resp.Header.Get("X-RateLimit-Retry-After")
		}
		t.mu.Lock()
		t.cooldown = time.Now().Add(retryAfter(after))
		t.mu.Unlock()
	}
	return resp, err
}

const defaultCooldown = 30 * time.Second

func retryAfter(h string) time.Duration {
	if h == "" {
		return defaultCooldown
	}
	if secs, err := strconv.Atoi(h); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(h); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
		return 0
	}
	return defaultCooldown
}
