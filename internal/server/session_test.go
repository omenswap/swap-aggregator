package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"omenswap.com/swap-aggregator/internal/provider"
)

func sessionCookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", sessionCookie, rec.Result().Cookies())
	return nil
}

func TestCreateSwapRecordsSession(t *testing.T) {
	f := &fakeProvider{name: "one", pairs: []provider.Pair{ethPair},
		create: func(provider.SwapRequest) (provider.Swap, error) {
			return provider.Swap{ID: "abc-123", Status: "pending"}, nil
		}}
	s := New([]provider.Provider{f}, time.Minute)
	rec := postSwap(s, validForm())
	c := sessionCookieFrom(t, rec)
	if !c.HttpOnly || c.Path != "/" {
		t.Errorf("cookie = %+v", c)
	}
	if c.MaxAge != 0 || !c.Expires.IsZero() {
		t.Errorf("expected a session cookie, got MaxAge=%d Expires=%v", c.MaxAge, c.Expires)
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		t.Fatal(err)
	}
	var got []sessionSwap
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Provider != "one" || got[0].ID != "abc-123" ||
		got[0].From != "ETH" || got[0].To != "USDC" || got[0].Amount != "1" {
		t.Errorf("session = %+v", got)
	}
}

func TestSessionKeepsNewestFirstAndDeduplicates(t *testing.T) {
	ids := []string{"a", "b", "a"}
	i := 0
	f := &fakeProvider{name: "one", pairs: []provider.Pair{ethPair},
		create: func(provider.SwapRequest) (provider.Swap, error) {
			id := ids[i]
			i++
			return provider.Swap{ID: id, Status: "pending"}, nil
		}}
	s := New([]provider.Provider{f}, time.Minute)

	var cookie *http.Cookie
	for range ids {
		req := httptest.NewRequest("POST", "/swap", strings.NewReader(validForm().Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		cookie = sessionCookieFrom(t, rec)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(cookie.Value)
	var got []sessionSwap
	json.Unmarshal(raw, &got)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("session = %+v", got)
	}
}

func TestSessionCapsEntries(t *testing.T) {
	swaps := make([]sessionSwap, maxSessionSwaps+5)
	for i := range swaps {
		swaps[i] = sessionSwap{Provider: "one", ID: string(rune('a' + i))}
	}
	if got := len(capSession(swaps)); got != maxSessionSwaps {
		t.Errorf("len = %d, want %d", got, maxSessionSwaps)
	}
}

func TestSwapsPageListsSessionSwaps(t *testing.T) {
	f := &fakeProvider{name: "one", pairs: []provider.Pair{ethPair}}
	s := New([]provider.Provider{f}, time.Minute)
	raw, _ := json.Marshal([]sessionSwap{{Provider: "one", ID: "abc-123", From: "ETH", To: "USDC", Amount: "1"}})
	req := httptest.NewRequest("GET", "/swaps", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: base64.RawURLEncoding.EncodeToString(raw)})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"abc-123", "/swap/one/abc-123", "ETH", "USDC"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestSwapsPageEmpty(t *testing.T) {
	s := New([]provider.Provider{&fakeProvider{name: "one"}}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/swaps", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no swaps") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestSwapsPageSkipsUnknownProviders(t *testing.T) {
	s := New([]provider.Provider{&fakeProvider{name: "one"}}, time.Minute)
	raw, _ := json.Marshal([]sessionSwap{{Provider: "gone", ID: "zzz"}})
	req := httptest.NewRequest("GET", "/swaps", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: base64.RawURLEncoding.EncodeToString(raw)})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "zzz") {
		t.Errorf("listed a swap for a provider that is no longer configured:\n%s", rec.Body.String())
	}
}

func TestSessionIgnoresGarbageCookie(t *testing.T) {
	s := New([]provider.Provider{&fakeProvider{name: "one"}}, time.Minute)
	req := httptest.NewRequest("GET", "/swaps", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "!!!not-base64!!!"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "no swaps") {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
}
