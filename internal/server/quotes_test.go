package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"omenswap.com/swap-aggregator/internal/provider"
)

type fakeProvider struct {
	name       string
	pairs      []provider.Pair
	pairsErr   error
	pairsCalls atomic.Int64
	quote      func(provider.QuoteRequest) (provider.Quote, error)
	create     func(provider.SwapRequest) (provider.Swap, error)
	status     func(string) (provider.Swap, error)
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Pairs(ctx context.Context) ([]provider.Pair, error) {
	f.pairsCalls.Add(1)
	return f.pairs, f.pairsErr
}
func (f *fakeProvider) Quote(ctx context.Context, r provider.QuoteRequest) (provider.Quote, error) {
	if f.quote != nil {
		return f.quote(r)
	}
	return provider.Quote{Provider: f.name}, nil
}
func (f *fakeProvider) CreateSwap(ctx context.Context, r provider.SwapRequest) (provider.Swap, error) {
	if f.create != nil {
		return f.create(r)
	}
	return provider.Swap{}, errors.New("not implemented")
}
func (f *fakeProvider) Status(ctx context.Context, id string) (provider.Swap, error) {
	if f.status != nil {
		return f.status(id)
	}
	return provider.Swap{}, errors.New("not implemented")
}

var ethPair = provider.Pair{From: "ETH", To: "USDC", Symbol: "ETHUSDC", Rate: "3000",
	MinFrom: "0.01", MaxFrom: "10", Fee: "0.5%"}

func quoteFor(name, amount string) func(provider.QuoteRequest) (provider.Quote, error) {
	return func(r provider.QuoteRequest) (provider.Quote, error) {
		return provider.Quote{Provider: name, Pair: ethPair, ToAmount: amount}, nil
	}
}

func TestIndexRenders(t *testing.T) {
	f := &fakeProvider{name: "one", pairs: []provider.Pair{ethPair}}
	s := New([]provider.Provider{f}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ETH") || !strings.Contains(body, "USDC") {
		t.Errorf("body missing tokens: %s", body)
	}
}

type gatedFake struct {
	fakeProvider
	brokered bool
	link     string
}

func (g *gatedFake) Brokered() bool { return g.brokered }
func (g *gatedFake) SwapLink(r provider.QuoteRequest) string {
	return g.link + "?from=" + r.From
}

func TestQuotesLinkWhenNotBrokered(t *testing.T) {
	g := &gatedFake{
		fakeProvider: fakeProvider{name: "gated", pairs: []provider.Pair{ethPair},
			quote: quoteFor("gated", "2950")},
		brokered: false,
		link:     "https://example.com/swap",
	}
	s := New([]provider.Provider{g}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/quotes?from=ETH&to=USDC&amount=1", nil))
	var resp struct {
		Quotes []struct {
			Provider        string `json:"provider"`
			ToAmount        string `json:"to_amount"`
			Link            string `json:"link"`
			SwapNeedsAPIKey bool   `json:"swap_requires_api_key"`
		} `json:"quotes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Quotes[0].Link != "https://example.com/swap?from=ETH" {
		t.Errorf("link = %q", resp.Quotes[0].Link)
	}
	if !resp.Quotes[0].SwapNeedsAPIKey || resp.Quotes[0].ToAmount != "2950" {
		t.Errorf("expected public quote with keyed swap: %+v", resp.Quotes[0])
	}
}

type quoteGatedFake struct {
	gatedFake
	quoteCalled atomic.Bool
}

func (g *quoteGatedFake) APIKeyRequiredForQuote() bool { return true }
func (g *quoteGatedFake) APIKeyRequiredForSwap() bool  { return true }
func (g *quoteGatedFake) Quote(context.Context, provider.QuoteRequest) (provider.Quote, error) {
	g.quoteCalled.Store(true)
	return provider.Quote{}, errors.New("should not be called without a key")
}

func TestQuotesKeyAndLinkWhenQuoteIsGated(t *testing.T) {
	g := &quoteGatedFake{gatedFake: gatedFake{
		fakeProvider: fakeProvider{name: "gated", pairs: []provider.Pair{ethPair}},
		brokered:     false,
		link:         "https://example.com/swap",
	}}
	s := New([]provider.Provider{g}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/quotes?from=ETH&to=USDC&amount=1", nil))
	if g.quoteCalled.Load() {
		t.Fatal("quote endpoint was called even though it requires an API key")
	}
	var resp struct {
		Quotes []struct {
			Link             string `json:"link"`
			QuoteNeedsAPIKey bool   `json:"quote_requires_api_key"`
			SwapNeedsAPIKey  bool   `json:"swap_requires_api_key"`
		} `json:"quotes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	q := resp.Quotes[0]
	if !q.QuoteNeedsAPIKey || !q.SwapNeedsAPIKey || q.Link != "https://example.com/swap?from=ETH" {
		t.Fatalf("gated quote = %+v", q)
	}
}

func TestQuotesNoLinkWhenBrokered(t *testing.T) {
	g := &gatedFake{
		fakeProvider: fakeProvider{name: "gated", pairs: []provider.Pair{ethPair},
			quote: quoteFor("gated", "2950")},
		brokered: true,
		link:     "https://example.com/swap",
	}
	s := New([]provider.Provider{g}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/quotes?from=ETH&to=USDC&amount=1", nil))
	if strings.Contains(rec.Body.String(), "example.com") {
		t.Errorf("brokered provider should have no link: %s", rec.Body.String())
	}
}

func TestIndexIncludesPairData(t *testing.T) {
	f := &fakeProvider{name: "one", pairs: []provider.Pair{
		ethPair,
		{From: "USDC", To: "ETH", Symbol: "USDCETH", Rate: "0.0005"},
	}}
	s := New([]provider.Provider{f}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `[["ETH","USDC"],["USDC","ETH"]]`) {
		t.Errorf("body missing pair data: %s", body)
	}
}

func TestQuotesSortedBestFirst(t *testing.T) {
	low := &fakeProvider{name: "low", pairs: []provider.Pair{ethPair}, quote: quoteFor("low", "2900")}
	high := &fakeProvider{name: "high", pairs: []provider.Pair{ethPair}, quote: quoteFor("high", "3000")}
	s := New([]provider.Provider{low, high}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/quotes?from=ETH&to=USDC&amount=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Quotes []struct {
			Provider string `json:"provider"`
			ToAmount string `json:"to_amount"`
			Err      string `json:"err"`
		} `json:"quotes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Quotes) != 2 || resp.Quotes[0].Provider != "high" || resp.Quotes[1].Provider != "low" {
		t.Errorf("quotes = %+v", resp.Quotes)
	}
}

func TestQuotesProviderErrorIsolated(t *testing.T) {
	bad := &fakeProvider{name: "bad", quote: func(provider.QuoteRequest) (provider.Quote, error) {
		return provider.Quote{}, errors.New("connection refused")
	}}
	good := &fakeProvider{name: "good", pairs: []provider.Pair{ethPair}, quote: quoteFor("good", "3000")}
	s := New([]provider.Provider{bad, good}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/quotes?from=ETH&to=USDC&amount=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var resp struct {
		Quotes []struct {
			Provider string `json:"provider"`
			ToAmount string `json:"to_amount"`
			Err      string `json:"err"`
		} `json:"quotes"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Quotes) != 2 {
		t.Fatalf("quotes = %+v", resp.Quotes)
	}
	if resp.Quotes[0].Provider != "good" || resp.Quotes[0].ToAmount != "3000" {
		t.Errorf("good quote = %+v", resp.Quotes[0])
	}
	if resp.Quotes[1].Provider != "bad" || resp.Quotes[1].Err == "" {
		t.Errorf("bad quote = %+v", resp.Quotes[1])
	}
}

func TestQuotesMissingParams(t *testing.T) {
	f := &fakeProvider{name: "one", pairs: []provider.Pair{ethPair}}
	s := New([]provider.Provider{f}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/quotes?from=ETH", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d", rec.Code)
	}
}

func TestPairsCached(t *testing.T) {
	f := &fakeProvider{name: "one", pairs: []provider.Pair{ethPair}}
	s := New([]provider.Provider{f}, time.Minute)
	for range 3 {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	}
	if n := f.pairsCalls.Load(); n != 1 {
		t.Errorf("Pairs called %d times", n)
	}
}

func TestHealthz(t *testing.T) {
	s := New(nil, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
}

type ratedFake struct {
	fakeProvider
	modes map[string]bool
}

func (r *ratedFake) SupportsRateMode(t provider.RateType, d provider.Direction) bool {
	return r.modes[string(t)+"/"+string(d)]
}

func TestQuotesFixedIsPricedFromTheSendAmount(t *testing.T) {
	mk := func(name, to string) *ratedFake {
		return &ratedFake{
			fakeProvider: fakeProvider{name: name, pairs: []provider.Pair{ethPair},
				quote: func(req provider.QuoteRequest) (provider.Quote, error) {
					if req.RateType != provider.Fixed || req.Direction != provider.FromSide {
						t.Errorf("%s got %+v", name, req)
					}
					return provider.Quote{Provider: name, Pair: ethPair,
						FromAmount: req.Amount, ToAmount: to, RateType: provider.Fixed}, nil
				}},
			modes: map[string]bool{"fixed/from": true, "floating/from": true},
		}
	}
	low, high := mk("low", "2900"), mk("high", "3000")
	s := New([]provider.Provider{low, high}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/quotes?from=ETH&to=USDC&amount=1&rate=fixed", nil))
	var resp struct {
		Quotes []struct {
			Provider   string `json:"provider"`
			FromAmount string `json:"from_amount"`
			ToAmount   string `json:"to_amount"`
			RateType   string `json:"rate_type"`
		} `json:"quotes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Quotes) != 2 || resp.Quotes[0].Provider != "high" {
		t.Fatalf("expected the biggest payout first: %+v", resp.Quotes)
	}
	if resp.Quotes[0].FromAmount != "1" || resp.Quotes[0].RateType != "fixed" {
		t.Errorf("quote = %+v", resp.Quotes[0])
	}
}

func TestQuotesFixedMarksProvidersWithoutFixedRates(t *testing.T) {
	plain := &fakeProvider{name: "floatonly", pairs: []provider.Pair{ethPair},
		quote: func(provider.QuoteRequest) (provider.Quote, error) {
			t.Error("provider without fixed support must not be asked for a quote")
			return provider.Quote{}, nil
		}}
	s := New([]provider.Provider{plain}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/quotes?from=ETH&to=USDC&amount=1&rate=fixed", nil))
	var resp struct {
		Quotes []struct {
			Provider string `json:"provider"`
			Err      string `json:"err"`
			NoFixed  bool   `json:"no_fixed_rate"`
		} `json:"quotes"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Quotes) != 1 || !resp.Quotes[0].NoFixed || resp.Quotes[0].Err == "" {
		t.Errorf("quotes = %+v", resp.Quotes)
	}
}

func TestQuotesFloatingStillSortsByReceiveAmount(t *testing.T) {
	low := &fakeProvider{name: "low", pairs: []provider.Pair{ethPair}, quote: quoteFor("low", "2900")}
	high := &fakeProvider{name: "high", pairs: []provider.Pair{ethPair}, quote: quoteFor("high", "3000")}
	s := New([]provider.Provider{low, high}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/quotes?from=ETH&to=USDC&amount=1&rate=floating", nil))
	var resp struct {
		Quotes []struct{ Provider string } `json:"quotes"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Quotes[0].Provider != "high" {
		t.Errorf("quotes = %+v", resp.Quotes)
	}
}
