package wizardswap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"omenswap.com/swap-aggregator/internal/config"
	"omenswap.com/swap-aggregator/internal/provider"
)

const estimateJSON = `{"estimated_amount":"82.30677239"}`

const belowMinJSON = `{"estimated_amount":false}`

const aboveMaxJSON = `{"estimated_amount":"Insufficient liquidity."}`

const exchangeJSON = `{
"id":"abc123","type":"floating",
"timestamp":"2026-08-01T20:00:00.000Z","updated_at":"2026-08-01T20:01:00.000Z",
"currency_from":"eth","currency_to":"xmr",
"amount_from":"","expected_amount":"1.5","amount_to":"12.34",
"address_from":"0xdeposit","address_to":"moneroaddr",
"extra_id_from":"","extra_id_to":"",
"tx_from":"","tx_to":"",
"status":"waiting","refund_address":""}`

func newTestProvider(t *testing.T, h http.Handler) provider.Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(config.Provider{Name: "wizardswap", Type: "wizardswap", URL: srv.URL + "/", Enabled: true, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestQuoteComputesAmount(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/estimate", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Write([]byte(estimateJSON))
	})
	p := newTestProvider(t, mux)
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "XMR", Amount: "0.5"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Err != "" {
		t.Fatalf("quote err: %s", q.Err)
	}
	if body["currency_from"] != "btc" || body["currency_to"] != "xmr" ||
		body["amount_from"] != "0.5" || body["api_key"] != "test-key" {
		t.Errorf("request body = %v", body)
	}
	if q.ToAmount != "82.30677239" {
		t.Errorf("ToAmount = %q", q.ToAmount)
	}
	if q.Pair.Rate != "164.61354478" {
		t.Errorf("Rate = %q", q.Pair.Rate)
	}
	if q.Provider != "wizardswap" {
		t.Errorf("Provider = %q", q.Provider)
	}
}

func TestQuoteBelowMin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/estimate", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(belowMinJSON))
	})
	p := newTestProvider(t, mux)
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "XMR", Amount: "0.00000001"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "minimum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteAboveMax(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/estimate", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(aboveMaxJSON))
	})
	p := newTestProvider(t, mux)
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "XMR", Amount: "100000"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "maximum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteUnsupportedPair(t *testing.T) {
	p := newTestProvider(t, http.NewServeMux())
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "USDT", To: "BTC", Amount: "100"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "pair not supported") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteTransportError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/estimate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	p := newTestProvider(t, mux)
	_, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "XMR", Amount: "1"})
	if err == nil || !strings.Contains(err.Error(), "wizardswap") {
		t.Errorf("err = %v", err)
	}
}

func TestCreateSwap(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/exchange", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Write([]byte(exchangeJSON))
	})
	p := newTestProvider(t, mux)
	s, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "XMR", Amount: "1.5", DestinationAddress: "moneroaddr",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["currency_from"] != "eth" || body["currency_to"] != "xmr" ||
		body["amount_from"] != "1.5" || body["address_to"] != "moneroaddr" ||
		body["api_key"] != "test-key" {
		t.Errorf("request body = %v", body)
	}
	if _, ok := body["refund_address"]; ok {
		t.Error("refund_address should be omitted")
	}
	if s.ID != "abc123" || s.DepositAddress != "0xdeposit" ||
		s.FromAmount != "1.5" || s.ToAmountEstimated != "12.34" ||
		s.Status != "pending" || s.DestinationAddress != "moneroaddr" ||
		s.From != "ETH" || s.To != "XMR" || s.CreatedAt != "2026-08-01T20:00:00.000Z" {
		t.Errorf("swap = %+v", s)
	}
}

func TestCreateSwapSendsRefundAddress(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/exchange", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(exchangeJSON))
	})
	p := newTestProvider(t, mux)
	_, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "XMR", Amount: "1.5", DestinationAddress: "moneroaddr", RefundAddress: "0xrefund",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["refund_address"] != "0xrefund" {
		t.Errorf("refund_address = %v", body["refund_address"])
	}
}

func TestStatusMapping(t *testing.T) {
	cases := map[string]string{
		"waiting":    "pending",
		"confirming": "awaiting_confirmation",
		"verifying":  "deposited",
		"exchanging": "deposited",
		"sending":    "deposited",
		"finished":   "completed",
		"failed":     "failed",
		"refunded":   "refunded",
		"mystery":    "mystery",
	}
	var current string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/exchange/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "abc123" {
			w.Write([]byte(`"Invalid exchange ID"`))
			return
		}
		w.Write([]byte(strings.Replace(exchangeJSON, `"status":"waiting"`, `"status":"`+current+`"`, 1)))
	})
	p := newTestProvider(t, mux)
	for wizStatus, want := range cases {
		current = wizStatus
		s, err := p.Status(context.Background(), "abc123")
		if err != nil {
			t.Fatal(err)
		}
		if s.Status != want {
			t.Errorf("status %q mapped to %q, want %q", wizStatus, s.Status, want)
		}
	}
}

func TestErrorPayloadSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/exchange/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`"Invalid exchange ID"`))
	})
	p := newTestProvider(t, mux)
	_, err := p.Status(context.Background(), "nope")
	if err == nil || !strings.Contains(err.Error(), "Invalid exchange ID") {
		t.Errorf("err = %v", err)
	}
}

func TestPairsExcludesSameToken(t *testing.T) {
	p := newTestProvider(t, http.NewServeMux())
	pairs, err := p.Pairs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 20 {
		t.Fatalf("got %d pairs, want 20", len(pairs))
	}
	for _, pr := range pairs {
		if pr.From == pr.To {
			t.Errorf("pair %s/%s has same from and to", pr.From, pr.To)
		}
		if pr.Unavailable {
			t.Errorf("pair %s/%s marked unavailable", pr.From, pr.To)
		}
	}
}

func TestGatingAndSwapLink(t *testing.T) {
	keyless, _ := New(config.Provider{Name: "wizardswap", URL: "https://www.wizardswap.io", AffiliateCode: "ref1"})
	if !keyless.(provider.Gated).Brokered() {
		t.Error("api key is optional: expected brokered without key")
	}
	link := keyless.(provider.Linker).SwapLink(provider.QuoteRequest{From: "BTC", To: "XMR", Amount: "0.5"})
	for _, want := range []string{"wizardswap.io", "from=btc", "to=xmr", "amount=0.5", "ref=ref1"} {
		if !strings.Contains(link, want) {
			t.Errorf("link %q missing %q", link, want)
		}
	}
	keyed, _ := New(config.Provider{Name: "wizardswap", URL: "https://www.wizardswap.io", APIKey: "k"})
	if !keyed.(provider.Gated).Brokered() {
		t.Error("expected brokered with key")
	}
}
