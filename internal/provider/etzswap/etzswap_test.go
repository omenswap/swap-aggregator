package etzswap

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

const rateJSON = `{"data":{"amountFrom":2,"amountTo":67.054138,"minAmountFrom":0.00103089,"maxAmountFrom":7.466,"rate":33.527069},"errors":{}}`

const belowMinJSON = `{"data":null,"errors":{"amountTooSmall":{"message":"Rate amount too small","args":{"minAllowedAmount":0.00103089,"actualAmount":0.0001}}}}`

const aboveMaxJSON = `{"data":null,"errors":{"amountTooBig":{"message":"Rate amount too big","args":{"maxAllowedAmount":7.466,"actualAmount":1000}}}}`

const unsupportedJSON = `{"data":null,"errors":{"unsupportedExchangePairs":{"message":"Rate unsupported exchange pairs","args":{"coinFrom":"BTC","networkFrom":"BTC","coinTo":"ETH","networkTo":"ETH"}}}}`

const txJSON = `{"data":{
"transactionId":"trJ0VwAal5sTKImP1XDAGzz2r38USNyW","amount":1.5,"amountTo":4850.1,
"coinFrom":{"coinCode":"ETH","coinName":"Ethereum","network":"ETH"},
"coinTo":{"coinCode":"USDC","coinName":"USD Coin","network":"ETH"},
"deposit":{"address":"0xdeadbeef","memo":""},
"withdraw":{"address":"0xdest","memo":""},
"refund":{"address":"","memo":""},
"inboundTransfer":{"hash":"","link":""},
"outboundTransfer":{"hash":"","link":""},
"createdAt":"2026-08-01T20:00:00.220358200Z","rate":3233.4,"rateType":"float",
"status":"deposit","confirmations":"0/1","transactionSource":"api",
"affiliate":{"amount":0,"affiliateToken":""},
"transactionSecurity":{"type":"standard","isSecured":false}},"errors":{}}`

func newTestProvider(t *testing.T, h http.Handler) provider.Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(config.Provider{Name: "etzswap", Type: "etzswap", URL: srv.URL + "/", Enabled: true, APIKey: "pub-key:secret-key"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestQuoteComputesAmount(t *testing.T) {
	var headers map[string]string
	var query map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/deposit/public/rate", func(w http.ResponseWriter, r *http.Request) {
		headers = map[string]string{
			"X-API-KEY":         r.Header.Get("X-API-KEY"),
			"X-API-SECRET-KEY":  r.Header.Get("X-API-SECRET-KEY"),
			"X-API-KEY-VERSION": r.Header.Get("X-API-KEY-VERSION"),
		}
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
	if headers["X-API-KEY"] != "pub-key" || headers["X-API-SECRET-KEY"] != "secret-key" || headers["X-API-KEY-VERSION"] != "1" {
		t.Errorf("headers = %v", headers)
	}
	if query["coinFrom"] != "BTC" || query["networkFrom"] != "BTC" ||
		query["coinTo"] != "ETH" || query["networkTo"] != "ETH" ||
		query["amountFrom"] != "2" || query["rateType"] != "float" {
		t.Errorf("query = %v", query)
	}
	if q.ToAmount != "67.054138" {
		t.Errorf("ToAmount = %q", q.ToAmount)
	}
	if q.Pair.Rate != "33.527069" {
		t.Errorf("Rate = %q", q.Pair.Rate)
	}
	if q.Pair.MinFrom != "0.00103089" {
		t.Errorf("MinFrom = %q", q.Pair.MinFrom)
	}
	if q.Pair.MaxFrom != "7.466" {
		t.Errorf("MaxFrom = %q", q.Pair.MaxFrom)
	}
	if q.Provider != "etzswap" {
		t.Errorf("Provider = %q", q.Provider)
	}
}

func TestQuoteBelowMin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/deposit/public/rate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
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
	if q.Pair.MinFrom != "0.00103089" {
		t.Errorf("MinFrom = %q", q.Pair.MinFrom)
	}
}

func TestQuoteAboveMax(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/deposit/public/rate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
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

func TestQuoteUnsupportedPairLocal(t *testing.T) {
	p := newTestProvider(t, http.NewServeMux())
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "NOTACOIN", To: "BTC", Amount: "100"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "pair not supported") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteUnsupportedPairAPI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/deposit/public/rate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(unsupportedJSON))
	})
	p := newTestProvider(t, mux)
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "ETH", Amount: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "pair not supported") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestCreateSwap(t *testing.T) {
	var apiKey string
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/deposit/public/transaction", func(w http.ResponseWriter, r *http.Request) {
		apiKey = r.Header.Get("X-API-KEY")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Write([]byte(txJSON))
	})
	p := newTestProvider(t, mux)
	s, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if apiKey != "pub-key" {
		t.Errorf("X-API-KEY = %q", apiKey)
	}
	if body["coinFrom"] != "ETH" || body["networkFrom"] != "ETH" ||
		body["coinTo"] != "USDC" || body["networkTo"] != "ETH" ||
		body["amountFrom"] != 1.5 || body["withdrawalAddress"] != "0xdest" ||
		body["rateType"] != "float" {
		t.Errorf("request body = %v", body)
	}
	if _, ok := body["refundAddress"]; ok {
		t.Error("refundAddress should be omitted")
	}
	if s.ID != "trJ0VwAal5sTKImP1XDAGzz2r38USNyW" || s.DepositAddress != "0xdeadbeef" ||
		s.FromAmount != "1.5" || s.ToAmountEstimated != "4850.1" ||
		s.Status != "pending" || s.DestinationAddress != "0xdest" {
		t.Errorf("swap = %+v", s)
	}
	if s.From != "ETH" || s.To != "USDC" {
		t.Errorf("swap coins = %s/%s", s.From, s.To)
	}
}

func TestCreateSwapSendsRefundAddress(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/deposit/public/transaction", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
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
		"deposit":       "pending",
		"confirmations": "awaiting_confirmation",
		"confirmed":     "deposited",
		"exchanging":    "deposited",
		"sending":       "deposited",
		"success":       "completed",
		"overdue":       "expired",
		"refunded":      "refunded",
		"mystery":       "mystery",
	}
	var current string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/deposit/public/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "trJ0VwAal5sTKImP1XDAGzz2r38USNyW" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(strings.Replace(txJSON, `"status":"deposit"`, `"status":"`+current+`"`, 1)))
	})
	p := newTestProvider(t, mux)
	for etzStatus, want := range cases {
		current = etzStatus
		s, err := p.Status(context.Background(), "trJ0VwAal5sTKImP1XDAGzz2r38USNyW")
		if err != nil {
			t.Fatal(err)
		}
		if s.Status != want {
			t.Errorf("status %q mapped to %q, want %q", etzStatus, s.Status, want)
		}
	}
}

func TestErrorPayloadSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/deposit/public/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"data":null,"errors":{"transactionNotExist":{"message":"Transaction not exist","args":{"transactionId":"nope"}}}}`))
	})
	p := newTestProvider(t, mux)
	_, err := p.Status(context.Background(), "nope")
	if err == nil || !strings.Contains(err.Error(), "Transaction not exist") {
		t.Errorf("err = %v", err)
	}
}

func TestQuoteTransportError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/deposit/public/rate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"data":null,"errors":{"notAuthenticated":{"message":"Not authenticated","args":{}}}}`))
	})
	p := newTestProvider(t, mux)
	_, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "ETH", Amount: "1"})
	if err == nil || !strings.Contains(err.Error(), "Not authenticated") {
		t.Errorf("err = %v", err)
	}
}

func TestPairsExcludesSameToken(t *testing.T) {
	p := newTestProvider(t, http.NewServeMux())
	pairs, err := p.Pairs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 110 {
		t.Fatalf("got %d pairs, want 110", len(pairs))
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
	keyless, _ := New(config.Provider{Name: "etzswap", URL: "https://api.etz-swap.com", AffiliateCode: "ref1"})
	if !keyless.(provider.Gated).Brokered() {
		t.Error("api key is optional: expected brokered without key")
	}
	link := keyless.(provider.Linker).SwapLink(provider.QuoteRequest{From: "BTC", To: "XMR", Amount: "0.5"})
	for _, want := range []string{"etz-swap.com", "coinFrom=BTC", "networkFrom=BTC", "coinTo=XMR", "networkTo=XMR", "amountFrom=0.5", "rateType=float", "affiliateToken=ref1"} {
		if !strings.Contains(link, want) {
			t.Errorf("link %q missing %q", link, want)
		}
	}
	keyed, _ := New(config.Provider{Name: "etzswap", URL: "https://api.etz-swap.com", APIKey: "pub:sec"})
	if !keyed.(provider.Gated).Brokered() {
		t.Error("expected brokered with key")
	}
}
