package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"omenswap.com/swap-aggregator/internal/provider"
)

type credentialedFake struct {
	fakeProvider
	apiKey string
}

func (f *credentialedFake) Brokered() bool { return f.apiKey != "" }

func (f *credentialedFake) WithAPIKey(apiKey string) (provider.Provider, error) {
	return &credentialedFake{
		fakeProvider: fakeProvider{name: f.name, pairs: f.pairs, pairsErr: f.pairsErr,
			quote: f.quote, create: f.create, status: f.status},
		apiKey: apiKey,
	}, nil
}

func (f *credentialedFake) Quote(_ context.Context, _ provider.QuoteRequest) (provider.Quote, error) {
	amount := "keyless"
	if f.apiKey == "visitor-secret" {
		amount = "3000"
	}
	return provider.Quote{Provider: f.name, Pair: ethPair, ToAmount: amount}, nil
}

func (f *credentialedFake) CreateSwap(_ context.Context, _ provider.SwapRequest) (provider.Swap, error) {
	if f.apiKey != "visitor-secret" {
		return provider.Swap{}, errors.New("missing visitor credential")
	}
	return provider.Swap{ID: "byok-swap", Status: "pending"}, nil
}

func (f *credentialedFake) Status(_ context.Context, id string) (provider.Swap, error) {
	if f.apiKey != "visitor-secret" {
		return provider.Swap{}, errors.New("missing visitor credential")
	}
	return provider.Swap{ID: id, Status: "pending", From: "ETH", To: "USDC"}, nil
}

func saveProviderKey(t *testing.T, s *Server, providerName, apiKey string) *http.Cookie {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"provider": providerName, "api_key": apiKey})
	req := httptest.NewRequest(http.MethodPost, "/api/provider-keys", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save key: status %d body %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("save key: cookies = %d", len(cookies))
	}
	return cookies[0]
}

func TestProviderKeyIsEncryptedAndRequestScoped(t *testing.T) {
	f := &credentialedFake{fakeProvider: fakeProvider{name: "gated", pairs: []provider.Pair{ethPair}}}
	s := New([]provider.Provider{f}, time.Minute)
	cookie := saveProviderKey(t, s, "gated", "visitor-secret")

	if strings.Contains(cookie.Value, "visitor-secret") {
		t.Fatal("credential cookie contains the plaintext API key")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe cookie attributes: %+v", cookie)
	}

	keyedReq := httptest.NewRequest(http.MethodGet, "/api/quotes?from=ETH&to=USDC&amount=1", nil)
	keyedReq.AddCookie(cookie)
	keyedRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(keyedRec, keyedReq)
	if !strings.Contains(keyedRec.Body.String(), `"to_amount":"3000"`) || strings.Contains(keyedRec.Body.String(), "needs_api_key") {
		t.Fatalf("keyed quote = %s", keyedRec.Body.String())
	}

	plainRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(plainRec, httptest.NewRequest(http.MethodGet, "/api/quotes?from=ETH&to=USDC&amount=1", nil))
	if !strings.Contains(plainRec.Body.String(), `"swap_requires_api_key":true`) {
		t.Fatalf("keyless quote = %s", plainRec.Body.String())
	}
	if f.apiKey != "" {
		t.Fatal("configured provider was mutated by a visitor credential")
	}
}

func TestProviderKeyValidation(t *testing.T) {
	f := &credentialedFake{fakeProvider: fakeProvider{name: "gated"}}
	s := New([]provider.Provider{f}, time.Minute)

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"missing key", `{"provider":"gated","api_key":""}`, http.StatusBadRequest},
		{"unknown provider", `{"provider":"other","api_key":"x"}`, http.StatusNotFound},
		{"invalid json", `{`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/provider-keys", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestProviderKeyFollowsCreateAndStatusFlow(t *testing.T) {
	f := &credentialedFake{fakeProvider: fakeProvider{name: "one", pairs: []provider.Pair{ethPair}}}
	s := New([]provider.Provider{f}, time.Minute)
	cookie := saveProviderKey(t, s, "one", "visitor-secret")

	form := validForm()
	req := httptest.NewRequest(http.MethodPost, "/swap", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/swap/one/byok-swap" {
		t.Fatalf("create: status %d location %q body %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/api/swap/one/byok-swap", nil)
	statusReq.AddCookie(cookie)
	statusRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK || !strings.Contains(statusRec.Body.String(), `"id":"byok-swap"`) {
		t.Fatalf("status: code %d body %s", statusRec.Code, statusRec.Body.String())
	}
}
