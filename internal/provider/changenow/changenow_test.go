package changenow

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

const rangeJSON = `{"fromCurrency":"eth","fromNetwork":"eth","toCurrency":"usdc","toNetwork":"eth",
"flow":"standard","minAmount":0.01,"maxAmount":null}`

const estimateJSON = `{"fromCurrency":"eth","fromNetwork":"eth","toCurrency":"usdc","toNetwork":"eth",
"flow":"standard","type":"direct","rateId":null,"validUntil":null,
"transactionSpeedForecast":"10-60","warningMessage":null,
"fromAmount":2,"toAmount":6501.00}`

const createJSON = `{"id":"exch123abc","fromAmount":1.5,"toAmount":4850.10,"flow":"standard","type":"direct",
"payinAddress":"0xdeadbeef","payoutAddress":"0xdest","payoutExtraId":"",
"fromCurrency":"eth","toCurrency":"usdc","fromNetwork":"eth","toNetwork":"eth","refundAddress":""}`

const statusJSON = `{"id":"exch123abc","status":"exchanging","actionsAvailable":false,
"fromCurrency":"eth","fromNetwork":"eth","toCurrency":"usdc","toNetwork":"eth",
"expectedAmountFrom":1.5,"expectedAmountTo":4850.1,"amountFrom":1.5,"amountTo":null,
"payinAddress":"0xdeadbeef","payoutAddress":"0xdest",
"payinHash":"0xaaa","payoutHash":null,"refundAddress":"0xrefund",
"createdAt":"2026-08-01T20:00:00.000Z","updatedAt":"2026-08-01T20:05:00.000Z","depositReceivedAt":null,"validUntil":"2026-08-02T20:00:00.000Z"}`

func newTestProvider(t *testing.T, h http.Handler) provider.Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(config.Provider{Name: "changenow", Type: "changenow", URL: srv.URL, Enabled: true, APIKey: "secret-key"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func quoteHandler(t *testing.T, gotKeys *[]string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/exchange/range", func(w http.ResponseWriter, r *http.Request) {
		*gotKeys = append(*gotKeys, r.Header.Get("x-changenow-api-key"))
		q := r.URL.Query()
		if q.Get("fromCurrency") != "eth" || q.Get("fromNetwork") != "eth" ||
			q.Get("toCurrency") != "usdc" || q.Get("toNetwork") != "eth" || q.Get("flow") != "standard" {
			t.Errorf("range query = %v", q)
		}
		w.Write([]byte(rangeJSON))
	})
	mux.HandleFunc("GET /v2/exchange/estimated-amount", func(w http.ResponseWriter, r *http.Request) {
		*gotKeys = append(*gotKeys, r.Header.Get("x-changenow-api-key"))
		q := r.URL.Query()
		if q.Get("fromCurrency") != "eth" || q.Get("toCurrency") != "usdc" ||
			q.Get("fromAmount") != "2" || q.Get("flow") != "standard" {
			t.Errorf("estimate query = %v", q)
		}
		w.Write([]byte(estimateJSON))
	})
	return mux
}

func TestPairs(t *testing.T) {
	p := newTestProvider(t, http.NewServeMux())
	pairs, err := p.Pairs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) == 0 {
		t.Fatal("no pairs")
	}
	seen := map[string]bool{}
	for _, pr := range pairs {
		if pr.From == pr.To {
			t.Errorf("pair with from == to: %s", pr.From)
		}
		if pr.Unavailable {
			t.Errorf("pair %s/%s marked unavailable", pr.From, pr.To)
		}
		seen[pr.From+"/"+pr.To] = true
	}
	if !seen["BTC/ETH"] || !seen["ETH/BTC"] || !seen["XMR/USDT_TRX"] {
		t.Errorf("expected combinations missing: %d pairs", len(pairs))
	}
	if len(pairs) != len(seen) {
		t.Errorf("duplicate pairs: %d vs %d", len(pairs), len(seen))
	}
}

func TestQuoteComputesAmount(t *testing.T) {
	var keys []string
	p := newTestProvider(t, quoteHandler(t, &keys))
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
	if q.Provider != "changenow" {
		t.Errorf("Provider = %q", q.Provider)
	}
	if q.Pair.Rate != "3250.5" {
		t.Errorf("Rate = %q", q.Pair.Rate)
	}
	if q.Pair.MinFrom != "0.01" {
		t.Errorf("MinFrom = %q", q.Pair.MinFrom)
	}
	if len(keys) == 0 {
		t.Fatal("no requests made")
	}
	for _, k := range keys {
		if k != "secret-key" {
			t.Errorf("api key header = %q", k)
		}
	}
}

func TestQuoteBelowMin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/exchange/range", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(rangeJSON))
	})
	p := newTestProvider(t, mux)
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "USDC", Amount: "0.001"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "minimum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteAboveMax(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/exchange/range", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"fromCurrency":"eth","fromNetwork":"eth","toCurrency":"usdc","toNetwork":"eth","flow":"standard","minAmount":0.01,"maxAmount":5}`))
	})
	p := newTestProvider(t, mux)
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "USDC", Amount: "100"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "maximum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteUnsupportedPair(t *testing.T) {
	p := newTestProvider(t, http.NewServeMux())
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "FOO", To: "ETH", Amount: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Err != "pair not supported" {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestCreateSwap(t *testing.T) {
	var body map[string]any
	var key string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/exchange", func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("x-changenow-api-key")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Write([]byte(createJSON))
	})
	p := newTestProvider(t, mux)
	s, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if key != "secret-key" {
		t.Errorf("api key header = %q", key)
	}
	if body["fromCurrency"] != "eth" || body["fromNetwork"] != "eth" ||
		body["toCurrency"] != "usdc" || body["toNetwork"] != "eth" ||
		body["fromAmount"] != "1.5" || body["address"] != "0xdest" || body["flow"] != "standard" {
		t.Errorf("request body = %v", body)
	}
	if _, ok := body["refundAddress"]; ok {
		t.Error("refundAddress should be omitted")
	}
	if s.ID != "exch123abc" || s.DepositAddress != "0xdeadbeef" ||
		s.FromAmount != "1.5" || s.ToAmountEstimated != "4850.1" || s.Status != "pending" {
		t.Errorf("swap = %+v", s)
	}
}

func TestCreateSwapSendsRefundAddress(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/exchange", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(createJSON))
	})
	p := newTestProvider(t, mux)
	_, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest", RefundAddress: "0xrefund",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["refundAddress"] != "0xrefund" {
		t.Errorf("request body = %v", body)
	}
}

func TestStatusMapsCanonical(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/exchange/by-id", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "exch123abc" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(statusJSON))
	})
	p := newTestProvider(t, mux)
	s, err := p.Status(context.Background(), "exch123abc")
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != "deposited" {
		t.Errorf("Status = %q", s.Status)
	}
	if s.ID != "exch123abc" || s.From != "ETH" || s.To != "USDC" ||
		s.FromAmount != "1.5" || s.ToAmountEstimated != "4850.1" ||
		s.DepositAddress != "0xdeadbeef" || s.DestinationAddress != "0xdest" ||
		s.DepositTxHash != "0xaaa" || s.CreatedAt != "2026-08-01T20:00:00.000Z" {
		t.Errorf("swap = %+v", s)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := map[string]string{
		"new":        "pending",
		"waiting":    "pending",
		"confirming": "awaiting_confirmation",
		"verifying":  "awaiting_confirmation",
		"exchanging": "deposited",
		"sending":    "deposited",
		"finished":   "completed",
		"expired":    "expired",
		"refunded":   "refunded",
		"failed":     "failed",
		"weird":      "weird",
	}
	for in, want := range cases {
		if got := mapStatus(in); got != want {
			t.Errorf("mapStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestErrorPayloadSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/exchange", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"not_valid_address","status":400,"message":"Invalid payout address"}`))
	})
	p := newTestProvider(t, mux)
	_, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "bad",
	})
	if err == nil || !strings.Contains(err.Error(), "Invalid payout address") ||
		!strings.Contains(err.Error(), "changenow") {
		t.Errorf("err = %v", err)
	}
}

func TestGatingAndSwapLink(t *testing.T) {
	keyless, _ := New(config.Provider{Name: "changenow", URL: "https://api.changenow.io", AffiliateCode: "ref1"})
	if keyless.(provider.Gated).Brokered() {
		t.Error("expected not brokered without key")
	}
	link := keyless.(provider.Linker).SwapLink(provider.QuoteRequest{From: "BTC", To: "USDT_TRX", Amount: "0.5"})
	for _, want := range []string{"changenow.io", "from=btc", "to=usdttrc20", "amount=0.5", "link_id=ref1"} {
		if !strings.Contains(link, want) {
			t.Errorf("link %q missing %q", link, want)
		}
	}
	keyed, _ := New(config.Provider{Name: "changenow", URL: "https://api.changenow.io", APIKey: "k"})
	if !keyed.(provider.Gated).Brokered() {
		t.Error("expected brokered with key")
	}
}
