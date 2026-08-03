package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"omenswap.com/swap-aggregator/internal/provider"
)

func TestSiteNameOnEveryPage(t *testing.T) {
	f := &fakeProvider{name: "one", pairs: []provider.Pair{ethPair},
		status: func(string) (provider.Swap, error) {
			return provider.Swap{ID: "abc", Status: "pending", From: "ETH", To: "USDC"}, nil
		}}
	s := New([]provider.Provider{f}, time.Minute)
	s.SetSiteName("Omen Swap")
	for _, path := range []string{"/", "/swaps", "/swap/one/abc"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "<title>Omen Swap</title>") {
			t.Errorf("%s: title not set:\n%s", path, body)
		}
		if strings.Contains(body, "aggregator</span>") {
			t.Errorf("%s: hardcoded wordmark still present", path)
		}
		if !strings.Contains(body, "Omen") || !strings.Contains(body, "Swap</span>") {
			t.Errorf("%s: wordmark not split from the configured name:\n%s", path, body)
		}
	}
}

func TestSiteNameDefaultsWhenUnset(t *testing.T) {
	s := New([]provider.Provider{&fakeProvider{name: "one", pairs: []provider.Pair{ethPair}}}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "<title>Swap Aggregator</title>") {
		t.Errorf("default title missing")
	}
}

func TestSiteNameIsEscaped(t *testing.T) {
	s := New([]provider.Provider{&fakeProvider{name: "one", pairs: []provider.Pair{ethPair}}}, time.Minute)
	s.SetSiteName(`x<script>alert(1)</script>`)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if strings.Contains(rec.Body.String(), "<script>alert(1)") {
		t.Errorf("site name not escaped:\n%s", rec.Body.String())
	}
}

func TestSiteNameSingleWord(t *testing.T) {
	s := New([]provider.Provider{&fakeProvider{name: "one", pairs: []provider.Pair{ethPair}}}, time.Minute)
	s.SetSiteName("Omenswap")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "<title>Omenswap</title>") {
		t.Errorf("single word name missing")
	}
}
