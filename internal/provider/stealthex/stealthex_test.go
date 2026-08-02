package stealthex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"omenswap.com/swap-aggregator/internal/config"
	"omenswap.com/swap-aggregator/internal/provider"
)

const rangeJSON = `{"min_amount":0.01,"max_amount":10.5}`

const estimateJSON = `{"estimated_amount":6501.0,"rate":{"id":"rate-1","valid_until":"2026-08-01T20:10:00Z"}}`

const exchangeJSON = `{
"id":"ex123","status":"waiting","created_at":"2026-08-01T20:00:00Z","refund_address":"",
"deposit":{"symbol":"eth","network":"mainnet","amount":1.5,"address":"0xdeadbeef","extra_id":null,"tx_hash":null},
"withdrawal":{"symbol":"usdc","network":"eth","amount":4850.1,"address":"0xdest","tx_hash":null}}`

func newTestProvider(t *testing.T, h http.Handler, affiliate string) provider.Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(config.Provider{Name: "stealthex", Type: "stealthex", URL: srv.URL, Enabled: true, APIKey: "key123", AffiliateCode: affiliate})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func quoteHandler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/rates/range", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key123" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(rangeJSON))
	})
	mux.HandleFunc("POST /v4/rates/estimated-amount", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key123" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		route, _ := body["route"].(map[string]any)
		from, _ := route["from"].(map[string]any)
		to, _ := route["to"].(map[string]any)
		if from["symbol"] != "eth" || from["network"] != "mainnet" ||
			to["symbol"] != "usdc" || to["network"] != "eth" {
			t.Errorf("route = %v", route)
		}
		if body["estimation"] != "direct" || body["rate"] != "floating" || body["amount"] != 2.0 {
			t.Errorf("body = %v", body)
		}
		w.Write([]byte(estimateJSON))
	})
	return mux
}

func TestQuoteComputesAmount(t *testing.T) {
	p := newTestProvider(t, quoteHandler(t), "")
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "USDC", Amount: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Err != "" {
		t.Fatalf("quote err: %s", q.Err)
	}
	if q.ToAmount != "6501" {
		t.Errorf("ToAmount = %q", q.ToAmount)
	}
	if q.Pair.Rate != "3250.5" {
		t.Errorf("Rate = %q", q.Pair.Rate)
	}
	if q.Pair.MinFrom != "0.01" || q.Pair.MaxFrom != "10.5" {
		t.Errorf("MinFrom = %q, MaxFrom = %q", q.Pair.MinFrom, q.Pair.MaxFrom)
	}
	if q.Provider != "stealthex" {
		t.Errorf("Provider = %q", q.Provider)
	}
}

func TestQuoteBelowMin(t *testing.T) {
	p := newTestProvider(t, quoteHandler(t), "")
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "USDC", Amount: "0.001"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "minimum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteAboveMax(t *testing.T) {
	p := newTestProvider(t, quoteHandler(t), "")
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "USDC", Amount: "100"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "maximum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteUnsupportedPair(t *testing.T) {
	p := newTestProvider(t, http.NewServeMux(), "")
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "SHIB", Amount: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "pair not supported") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestCreateSwap(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/exchanges", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key123" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(exchangeJSON))
	})
	p := newTestProvider(t, mux, "")
	s, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest",
	})
	if err != nil {
		t.Fatal(err)
	}
	route, _ := body["route"].(map[string]any)
	from, _ := route["from"].(map[string]any)
	to, _ := route["to"].(map[string]any)
	if from["symbol"] != "eth" || from["network"] != "mainnet" ||
		to["symbol"] != "usdc" || to["network"] != "eth" {
		t.Errorf("route = %v", route)
	}
	if body["amount"] != 1.5 || body["address"] != "0xdest" ||
		body["estimation"] != "direct" || body["rate"] != "floating" {
		t.Errorf("body = %v", body)
	}
	if _, ok := body["refund_address"]; ok {
		t.Error("refund_address should be omitted")
	}
	if _, ok := body["additional_fee_percent"]; ok {
		t.Error("additional_fee_percent should be omitted")
	}
	if s.ID != "ex123" || s.DepositAddress != "0xdeadbeef" || s.Status != "pending" ||
		s.FromAmount != "1.5" || s.ToAmountEstimated != "4850.1" {
		t.Errorf("swap = %+v", s)
	}
	if s.From != "ETH" || s.To != "USDC" || s.DestinationAddress != "0xdest" ||
		s.CreatedAt != "2026-08-01T20:00:00Z" {
		t.Errorf("swap = %+v", s)
	}
}

func TestCreateSwapSendsRefundAndAffiliate(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/exchanges", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(exchangeJSON))
	})
	p := newTestProvider(t, mux, "0.5")
	_, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest", RefundAddress: "0xrefund",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["refund_address"] != "0xrefund" {
		t.Errorf("refund_address = %v", body["refund_address"])
	}
	if body["additional_fee_percent"] != 0.5 {
		t.Errorf("additional_fee_percent = %v", body["additional_fee_percent"])
	}
}

func TestStatusMapping(t *testing.T) {
	cases := map[string]string{
		"waiting":    "pending",
		"confirming": "awaiting_confirmation",
		"exchanging": "deposited",
		"sending":    "deposited",
		"finished":   "completed",
		"expired":    "expired",
		"refunded":   "refunded",
		"failed":     "failed",
		"verifying":  "verifying",
	}
	for apiStatus, want := range cases {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /v4/exchanges/{id}", func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") != "ex123" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write([]byte(strings.Replace(exchangeJSON, `"waiting"`, fmt.Sprintf("%q", apiStatus), 1)))
		})
		p := newTestProvider(t, mux, "")
		s, err := p.Status(context.Background(), "ex123")
		if err != nil {
			t.Fatal(err)
		}
		if s.Status != want {
			t.Errorf("status %q mapped to %q, want %q", apiStatus, s.Status, want)
		}
		if s.From != "ETH" || s.To != "USDC" || s.DepositAddress != "0xdeadbeef" {
			t.Errorf("swap = %+v", s)
		}
	}
}

func TestErrorPayloadSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/exchanges", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"err":{"kind":"Validation","details":"amount is too small"}}`))
	})
	p := newTestProvider(t, mux, "")
	_, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest",
	})
	if err == nil || !strings.Contains(err.Error(), "amount is too small") {
		t.Errorf("err = %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "stealthex") {
		t.Errorf("err = %v", err)
	}
}

func TestErrorFallsBackToHTTPStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/rates/range", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	p := newTestProvider(t, mux, "")
	_, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "USDC", Amount: "1"})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("err = %v", err)
	}
}

func TestPairs(t *testing.T) {
	p := newTestProvider(t, http.NewServeMux(), "")
	pairs, err := p.Pairs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 110 {
		t.Errorf("got %d pairs, want 110", len(pairs))
	}
	seen := map[string]bool{}
	for _, pr := range pairs {
		if pr.From == pr.To {
			t.Errorf("pair %s/%s has from == to", pr.From, pr.To)
		}
		if pr.Unavailable {
			t.Errorf("pair %s/%s unavailable", pr.From, pr.To)
		}
		key := pr.From + "/" + pr.To
		if seen[key] {
			t.Errorf("duplicate pair %s", key)
		}
		seen[key] = true
	}
}

func TestGatingAndSwapLink(t *testing.T) {
	keyless, _ := New(config.Provider{Name: "stealthex", URL: "https://api.stealthex.io", AffiliateCode: "ref1"})
	if keyless.(provider.Gated).Brokered() {
		t.Error("expected not brokered without key")
	}
	policy := keyless.(provider.APIKeyPolicy)
	if !policy.APIKeyRequiredForQuote() || !policy.APIKeyRequiredForSwap() {
		t.Error("StealthEX requires a key for both quotes and swaps")
	}
	link := keyless.(provider.Linker).SwapLink(provider.QuoteRequest{From: "BTC", To: "XMR", Amount: "0.5"})
	for _, want := range []string{"stealthex.io", "from=btc", "to=xmr", "amount=0.5", "ref=ref1"} {
		if !strings.Contains(link, want) {
			t.Errorf("link %q missing %q", link, want)
		}
	}
	keyed, _ := New(config.Provider{Name: "stealthex", URL: "https://api.stealthex.io", APIKey: "k"})
	if !keyed.(provider.Gated).Brokered() {
		t.Error("expected brokered with key")
	}
}

func TestWithAPIKeyReturnsIsolatedClient(t *testing.T) {
	base, _ := New(config.Provider{Name: "stealthex", URL: "https://api.stealthex.io"})
	keyed, err := base.(provider.Credentialed).WithAPIKey(" visitor-key ")
	if err != nil {
		t.Fatal(err)
	}
	if base.(provider.Gated).Brokered() {
		t.Fatal("base provider was mutated")
	}
	if !keyed.(provider.Gated).Brokered() || keyed.(*client).apiKey != "visitor-key" {
		t.Fatalf("keyed provider = %#v", keyed)
	}
}
