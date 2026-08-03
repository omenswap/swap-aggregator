package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAllowsBurstThenPaces(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(2, 2, func() time.Time { return now })
	for i := range 2 {
		if d := l.reserve(); d != 0 {
			t.Fatalf("burst call %d waited %v", i, d)
		}
	}
	d := l.reserve()
	if d <= 0 || d > 500*time.Millisecond {
		t.Fatalf("third call waited %v, want ~500ms", d)
	}
}

func TestRefillsOverTime(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(2, 1, func() time.Time { return now })
	l.reserve()
	now = now.Add(2 * time.Second)
	if d := l.reserve(); d != 0 {
		t.Fatalf("waited %v after refill", d)
	}
}

func TestZeroRateIsUnlimited(t *testing.T) {
	l := New(0, 0, time.Now)
	for range 100 {
		if d := l.reserve(); d != 0 {
			t.Fatal("unlimited limiter throttled")
		}
	}
}

func TestTransportPacesRequests(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	c := &http.Client{Transport: NewTransport(nil, 50, 1)}
	start := time.Now()
	for range 3 {
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("3 requests at 50/s took %v, expected pacing", elapsed)
	}
	if hits.Load() != 3 {
		t.Errorf("%d requests reached the server", hits.Load())
	}
}

func TestTransportHonoursRetryAfter(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := &http.Client{Transport: NewTransport(nil, 0, 0)}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// The cooldown must be reported locally instead of hammering the provider.
	if _, err := c.Get(srv.URL); err == nil {
		t.Fatal("expected the second request to be refused during the cooldown")
	}
	if hits.Load() != 1 {
		t.Errorf("%d requests reached the server during the cooldown", hits.Load())
	}
}

func TestTransportRespectsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	c := &http.Client{Transport: NewTransport(nil, 1, 1)}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if _, err := c.Do(req); err == nil {
		t.Fatal("expected the queued request to give up with the context")
	}
}
