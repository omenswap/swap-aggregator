package exolix

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

const rateJSON = `{"fromAmount":2,"toAmount":67.054138,"rate":33.527069,"message":null,"minAmount":0.00077769,"withdrawMin":7.91857857e-7,"maxAmount":10,"priceImpact":"0"}`

const belowMinJSON = `{"fromAmount":0.0001,"toAmount":0,"message":"Amount to exchange is below the possible min amount to exchange","minAmount":0.00077769,"maxAmount":10}`

const aboveMaxJSON = `{"fromAmount":1000,"toAmount":0,"message":"Amount to exchange is higher the possible max amount to exchange","minAmount":0.00077769,"maxAmount":10}`

const txJSON = `{
"id":"ab123CDe456","amount":1.5,"amountTo":4850.1,"rate":3233.4,"rateType":"float",
"coinFrom":{"coinCode":"ETH","coinName":"Ethereum","network":"ETH"},
"coinTo":{"coinCode":"USDC","coinName":"USD Coin","network":"ETH"},
"depositAddress":"0xdeadbeef","depositExtraId":null,
"withdrawalAddress":"0xdest","withdrawalExtraId":null,
"refundAddress":null,"refundExtraId":null,
"hashIn":{"hash":null,"link":null},"hashOut":{"hash":null,"link":null},
"status":"wait","createdAt":"2026-08-01T20:00:00.000Z"}`

func newTestProvider(t *testing.T, h http.Handler) provider.Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(config.Provider{Name: "exolix", Type: "exolix", URL: srv.URL + "/", Enabled: true, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestQuoteComputesAmount(t *testing.T) {
	var auth string
	var query map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/rate", func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		query = map[string]string{}
		for k := range r.URL.Query() {
			query[k] = r.URL.Query().Get(k)
		}
		w.Write([]byte(rateJSON))
	})
	p := newTestProvider(t, mux)
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "ETH", Amount: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Err != "" {
		t.Fatalf("quote err: %s", q.Err)
	}
	if auth != "test-key" {
		t.Errorf("Authorization = %q", auth)
	}
	if query["coinFrom"] != "BTC" || query["networkFrom"] != "BTC" ||
		query["coinTo"] != "ETH" || query["networkTo"] != "ETH" ||
		query["amount"] != "2" || query["rateType"] != "float" {
		t.Errorf("query = %v", query)
	}
	if q.ToAmount != "67.054138" {
		t.Errorf("ToAmount = %q", q.ToAmount)
	}
	if q.Pair.Rate != "33.527069" {
		t.Errorf("Rate = %q", q.Pair.Rate)
	}
	if q.Pair.MinFrom != "0.00077769" {
		t.Errorf("MinFrom = %q", q.Pair.MinFrom)
	}
	if q.Provider != "exolix" {
		t.Errorf("Provider = %q", q.Provider)
	}
}

func TestQuoteBelowMin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/rate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(belowMinJSON))
	})
	p := newTestProvider(t, mux)
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "ETH", Amount: "0.0001"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "minimum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteAboveMax(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/rate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(aboveMaxJSON))
	})
	p := newTestProvider(t, mux)
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "ETH", Amount: "1000"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "maximum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteUnsupportedPair(t *testing.T) {
	p := newTestProvider(t, http.NewServeMux())
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "USDT_POL", To: "BTC", Amount: "100"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "pair not supported") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestCreateSwap(t *testing.T) {
	var auth string
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/transactions", func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(txJSON))
	})
	p := newTestProvider(t, mux)
	s, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "test-key" {
		t.Errorf("Authorization = %q", auth)
	}
	if body["coinFrom"] != "ETH" || body["networkFrom"] != "ETH" ||
		body["coinTo"] != "USDC" || body["networkTo"] != "ETH" ||
		body["amount"] != 1.5 || body["withdrawalAddress"] != "0xdest" ||
		body["rateType"] != "float" {
		t.Errorf("request body = %v", body)
	}
	if _, ok := body["refundAddress"]; ok {
		t.Error("refundAddress should be omitted")
	}
	if s.ID != "ab123CDe456" || s.DepositAddress != "0xdeadbeef" ||
		s.FromAmount != "1.5" || s.ToAmountEstimated != "4850.1" ||
		s.Status != "pending" || s.DestinationAddress != "0xdest" {
		t.Errorf("swap = %+v", s)
	}
}

func TestCreateSwapSendsRefundAddress(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/transactions", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(txJSON))
	})
	p := newTestProvider(t, mux)
	_, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest", RefundAddress: "0xrefund",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["refundAddress"] != "0xrefund" {
		t.Errorf("refundAddress = %v", body["refundAddress"])
	}
}

func TestStatusMapping(t *testing.T) {
	cases := map[string]string{
		"wait":         "pending",
		"confirmation": "awaiting_confirmation",
		"confirmed":    "deposited",
		"exchanging":   "deposited",
		"sending":      "deposited",
		"success":      "completed",
		"overdue":      "expired",
		"refunded":     "refunded",
		"mystery":      "mystery",
	}
	var current string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "ab123CDe456" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(strings.Replace(txJSON, `"status":"wait"`, `"status":"`+current+`"`, 1)))
	})
	p := newTestProvider(t, mux)
	for exolixStatus, want := range cases {
		current = exolixStatus
		s, err := p.Status(context.Background(), "ab123CDe456")
		if err != nil {
			t.Fatal(err)
		}
		if s.Status != want {
			t.Errorf("status %q mapped to %q, want %q", exolixStatus, s.Status, want)
		}
	}
}

func TestErrorPayloadSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"Transaction not found"}`))
	})
	p := newTestProvider(t, mux)
	_, err := p.Status(context.Background(), "nope")
	if err == nil || !strings.Contains(err.Error(), "Transaction not found") {
		t.Errorf("err = %v", err)
	}
}

func TestQuoteTransportError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/rate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Invalid API key"}`))
	})
	p := newTestProvider(t, mux)
	_, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "ETH", Amount: "1"})
	if err == nil || !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("err = %v", err)
	}
}

func TestPairsExcludesSameToken(t *testing.T) {
	p := newTestProvider(t, http.NewServeMux())
	pairs, err := p.Pairs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 90 {
		t.Fatalf("got %d pairs, want 90", len(pairs))
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
	keyless, _ := New(config.Provider{Name: "exolix", URL: "https://exolix.com", AffiliateCode: "ref1"})
	if keyless.(provider.Gated).Brokered() {
		t.Error("expected not brokered without key")
	}
	link := keyless.(provider.Linker).SwapLink(provider.QuoteRequest{From: "BTC", To: "XMR", Amount: "0.5"})
	for _, want := range []string{"exolix.com", "from=BTC", "to=XMR", "amount=0.5", "ref=ref1"} {
		if !strings.Contains(link, want) {
			t.Errorf("link %q missing %q", link, want)
		}
	}
	keyed, _ := New(config.Provider{Name: "exolix", URL: "https://exolix.com", APIKey: "k"})
	if !keyed.(provider.Gated).Brokered() {
		t.Error("expected brokered with key")
	}
}
