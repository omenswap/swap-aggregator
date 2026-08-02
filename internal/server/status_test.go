package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"omenswap.com/swap-aggregator/internal/provider"
)

var testSwap = provider.Swap{
	ID: "abc-123", Status: "pending", From: "ETH", To: "USDC",
	FromAmount: "1.5", ToAmountEstimated: "4850.1",
	DepositAddress: "0xdeadbeef", DestinationAddress: "0xdest",
	ExpiresAt: "2026-08-02T20:00:00Z", CreatedAt: "2026-08-01T20:00:00Z",
}

func statusServer(t *testing.T, status func(string) (provider.Swap, error)) *Server {
	t.Helper()
	f := &fakeProvider{name: "one", status: status}
	return New([]provider.Provider{f}, time.Minute)
}

func get(s *Server, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestStatusPage(t *testing.T) {
	s := statusServer(t, func(id string) (provider.Swap, error) {
		if id != "abc-123" {
			t.Errorf("id = %q", id)
		}
		return testSwap, nil
	})
	rec := get(s, "/swap/one/abc-123")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"0xdeadbeef", "0xdest", "pending", "1.5"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestStatusPageUnknownProvider(t *testing.T) {
	s := statusServer(t, nil)
	if rec := get(s, "/swap/nope/abc-123"); rec.Code != http.StatusNotFound {
		t.Errorf("status %d", rec.Code)
	}
}

func TestStatusPageProviderError(t *testing.T) {
	s := statusServer(t, func(string) (provider.Swap, error) {
		return provider.Swap{}, errors.New("omenswap: swap not found")
	})
	rec := get(s, "/swap/one/abc-123")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "swap not found") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func TestStatusJSON(t *testing.T) {
	s := statusServer(t, func(string) (provider.Swap, error) { return testSwap, nil })
	rec := get(s, "/api/swap/one/abc-123")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "abc-123" || got["status"] != "pending" ||
		got["deposit_address"] != "0xdeadbeef" || got["to_amount_estimated"] != "4850.1" {
		t.Errorf("json = %v", got)
	}
}

func TestStatusJSONErrors(t *testing.T) {
	s := statusServer(t, func(string) (provider.Swap, error) {
		return provider.Swap{}, errors.New("boom")
	})
	if rec := get(s, "/api/swap/nope/abc-123"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown provider: status %d", rec.Code)
	}
	rec := get(s, "/api/swap/one/abc-123")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("provider error: status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "error") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func TestStatusPageIncludesQR(t *testing.T) {
	f := &fakeProvider{name: "one", status: func(string) (provider.Swap, error) {
		return provider.Swap{ID: "abc", Status: "pending", From: "BTC", To: "XMR",
			DepositAddress: "bc1q4r04hxptugjtl6cmvkggz5t0f7plqj4e7napft"}, nil
	}}
	s := New([]provider.Provider{f}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/swap/one/abc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `<svg class="qr"`) {
		t.Errorf("status page has no qr code: %s", rec.Body.String())
	}
}

func TestStatusPageWithoutDepositAddressHasNoQR(t *testing.T) {
	f := &fakeProvider{name: "one", status: func(string) (provider.Swap, error) {
		return provider.Swap{ID: "abc", Status: "expired", From: "BTC", To: "XMR"}, nil
	}}
	s := New([]provider.Provider{f}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/swap/one/abc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<svg") {
		t.Errorf("expected no qr code: %s", rec.Body.String())
	}
}
