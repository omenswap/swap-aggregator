package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"omenswap.com/swap-aggregator/internal/provider"
)

func postSwap(s *Server, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/swap", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func validForm() url.Values {
	return url.Values{
		"provider":            {"one"},
		"from":                {"ETH"},
		"to":                  {"USDC"},
		"amount":              {"1"},
		"destination_address": {"0xdest"},
	}
}

func TestCreateSwapRedirects(t *testing.T) {
	f := &fakeProvider{name: "one", pairs: []provider.Pair{ethPair},
		create: func(r provider.SwapRequest) (provider.Swap, error) {
			if r.From != "ETH" || r.Amount != "1" || r.DestinationAddress != "0xdest" {
				t.Errorf("req = %+v", r)
			}
			return provider.Swap{ID: "abc-123", Status: "pending"}, nil
		}}
	s := New([]provider.Provider{f}, time.Minute)
	rec := postSwap(s, validForm())
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/swap/one/abc-123" {
		t.Errorf("Location = %q", loc)
	}
}

func TestCreateSwapUnknownProvider(t *testing.T) {
	s := New([]provider.Provider{&fakeProvider{name: "one"}}, time.Minute)
	form := validForm()
	form.Set("provider", "nope")
	if rec := postSwap(s, form); rec.Code != http.StatusBadRequest {
		t.Errorf("status %d", rec.Code)
	}
}

func TestCreateSwapMissingDestination(t *testing.T) {
	s := New([]provider.Provider{&fakeProvider{name: "one"}}, time.Minute)
	form := validForm()
	form.Del("destination_address")
	if rec := postSwap(s, form); rec.Code != http.StatusBadRequest {
		t.Errorf("status %d", rec.Code)
	}
}

func TestCreateSwapRejectsUnbrokered(t *testing.T) {
	g := &gatedFake{
		fakeProvider: fakeProvider{name: "one", pairs: []provider.Pair{ethPair}},
		brokered:     false,
	}
	s := New([]provider.Provider{g}, time.Minute)
	rec := postSwap(s, validForm())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "api key") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func TestCreateSwapProviderError(t *testing.T) {
	f := &fakeProvider{name: "one", pairs: []provider.Pair{ethPair},
		create: func(provider.SwapRequest) (provider.Swap, error) {
			return provider.Swap{}, errors.New("omenswap: insufficient reserves")
		}}
	s := New([]provider.Provider{f}, time.Minute)
	rec := postSwap(s, validForm())
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/?error=") || !strings.Contains(loc, "insufficient+reserves") {
		t.Errorf("Location = %q", loc)
	}
}

func TestIndexShowsErrorFromQuery(t *testing.T) {
	s := New([]provider.Provider{&fakeProvider{name: "one", pairs: []provider.Pair{ethPair}}}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/?error=boom+happened", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "boom happened") {
		t.Errorf("body missing error: %s", rec.Body.String())
	}
}
