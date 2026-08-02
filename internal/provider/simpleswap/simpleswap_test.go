package simpleswap

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

const rangesJSON = `{"result":{"min":"0.01","max":"10.5"},"traceId":"t"}`

const estimateJSON = `{"result":{"estimatedAmount":6501,"rateId":"r1","validUntil":"2026-08-02T00:00:00Z"},"traceId":"t"}`

const exchangeJSON = `{"result":{
"publicId":"abc123","type":"floating","status":"waiting",
"tickerFrom":"eth","tickerTo":"usdc","networkFrom":"eth","networkTo":"eth",
"amountFrom":"1.5","amountTo":"4850.1",
"addressFrom":"0xdeadbeef","addressTo":"0xdest",
"userRefundAddress":"","txFrom":"","txTo":"",
"createdAt":"2026-08-01T20:00:00Z","updatedAt":"2026-08-01T20:00:00Z",
"validUntil":"2026-08-02T20:00:00Z"},"traceId":"t"}`

func newTestProvider(t *testing.T, h http.Handler) provider.Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(config.Provider{Name: "simpleswap", Type: "simpleswap", URL: srv.URL, Enabled: true, APIKey: "k123"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func quoteHandler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v3/ranges", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "k123" {
			t.Errorf("ranges x-api-key = %q", r.Header.Get("x-api-key"))
		}
		q := r.URL.Query()
		if q.Get("tickerFrom") != "eth" || q.Get("networkFrom") != "eth" ||
			q.Get("tickerTo") != "usdc" || q.Get("networkTo") != "eth" ||
			q.Get("fixed") != "false" || q.Get("reverse") != "false" {
			t.Errorf("ranges query = %v", q)
		}
		w.Write([]byte(rangesJSON))
	})
	mux.HandleFunc("GET /v3/estimates", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "k123" {
			t.Errorf("estimates x-api-key = %q", r.Header.Get("x-api-key"))
		}
		q := r.URL.Query()
		if q.Get("tickerFrom") != "eth" || q.Get("networkFrom") != "eth" ||
			q.Get("tickerTo") != "usdc" || q.Get("networkTo") != "eth" ||
			q.Get("fixed") != "false" || q.Get("reverse") != "false" ||
			q.Get("amount") != "2" {
			t.Errorf("estimates query = %v", q)
		}
		w.Write([]byte(estimateJSON))
	})
	return mux
}

func TestQuoteComputesAmount(t *testing.T) {
	p := newTestProvider(t, quoteHandler(t))
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "USDC", Amount: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Err != "" {
		t.Fatalf("quote err: %s", q.Err)
	}
	if q.Provider != "simpleswap" {
		t.Errorf("Provider = %q", q.Provider)
	}
	if q.ToAmount != "6501" {
		t.Errorf("ToAmount = %q", q.ToAmount)
	}
	if q.Pair.From != "ETH" || q.Pair.To != "USDC" || q.Pair.Rate != "3250.5" ||
		q.Pair.MinFrom != "0.01" || q.Pair.MaxFrom != "10.5" || q.Pair.Unavailable {
		t.Errorf("pair = %+v", q.Pair)
	}
}

func TestQuoteBelowMin(t *testing.T) {
	p := newTestProvider(t, quoteHandler(t))
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "USDC", Amount: "0.001"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "minimum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteAboveMax(t *testing.T) {
	p := newTestProvider(t, quoteHandler(t))
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "USDC", Amount: "100"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "maximum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteUnsupportedPair(t *testing.T) {
	p := newTestProvider(t, quoteHandler(t))
	for _, req := range []provider.QuoteRequest{
		{From: "ETH", To: "ETH", Amount: "1"},
		{From: "ABC", To: "ETH", Amount: "1"},
		{From: "ETH", To: "XYZ", Amount: "1"},
	} {
		q, err := p.Quote(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(q.Err, "pair not supported") {
			t.Errorf("%s/%s Err = %q", req.From, req.To, q.Err)
		}
	}
}

func TestCreateSwap(t *testing.T) {
	var body map[string]any
	var gotKey string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/exchanges", func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Write([]byte(exchangeJSON))
	})
	p := newTestProvider(t, mux)
	s, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "k123" {
		t.Errorf("x-api-key = %q", gotKey)
	}
	if body["tickerFrom"] != "eth" || body["networkFrom"] != "eth" ||
		body["tickerTo"] != "usdc" || body["networkTo"] != "eth" ||
		body["amount"] != "1.5" || body["addressTo"] != "0xdest" ||
		body["fixed"] != false || body["reverse"] != false {
		t.Errorf("request body = %v", body)
	}
	if _, ok := body["userRefundAddress"]; ok {
		t.Error("userRefundAddress should be omitted")
	}
	if s.ID != "abc123" || s.DepositAddress != "0xdeadbeef" || s.FromAmount != "1.5" ||
		s.ToAmountEstimated != "4850.1" || s.Status != "pending" {
		t.Errorf("swap = %+v", s)
	}
}

func TestCreateSwapSendsRefundAddress(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/exchanges", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(exchangeJSON))
	})
	p := newTestProvider(t, mux)
	_, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest", RefundAddress: "0xrefund",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["userRefundAddress"] != "0xrefund" {
		t.Errorf("userRefundAddress = %v", body["userRefundAddress"])
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
		"expired":    "expired",
		"refunded":   "refunded",
		"failed":     "failed",
		"banana":     "banana",
	}
	status := "waiting"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v3/exchanges/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "abc123" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(strings.Replace(exchangeJSON, `"status":"waiting"`, `"status":"`+status+`"`, 1)))
	})
	p := newTestProvider(t, mux)
	for api, want := range cases {
		status = api
		s, err := p.Status(context.Background(), "abc123")
		if err != nil {
			t.Fatal(err)
		}
		if s.Status != want {
			t.Errorf("status %q mapped to %q, want %q", api, s.Status, want)
		}
	}
}

func TestStatusMapsFields(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v3/exchanges/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(exchangeJSON))
	})
	p := newTestProvider(t, mux)
	s, err := p.Status(context.Background(), "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if s.From != "ETH" || s.To != "USDC" || s.FromAmount != "1.5" ||
		s.DestinationAddress != "0xdest" || s.ExpiresAt != "2026-08-02T20:00:00Z" ||
		s.CreatedAt != "2026-08-01T20:00:00Z" {
		t.Errorf("swap = %+v", s)
	}
}

func TestErrorPayloadSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v3/ranges", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"code":401,"error":"Unauthorized","message":"Wrong api key","traceId":"t"}`))
	})
	p := newTestProvider(t, mux)
	_, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "USDC", Amount: "1"})
	if err == nil || !strings.Contains(err.Error(), "simpleswap: Wrong api key") {
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
		t.Errorf("got %d pairs", len(pairs))
	}
	for _, pr := range pairs {
		if pr.From == pr.To {
			t.Errorf("pair %s/%s has from == to", pr.From, pr.To)
		}
		if pr.Unavailable {
			t.Errorf("pair %s/%s unavailable", pr.From, pr.To)
		}
	}
}

func TestGatingAndSwapLink(t *testing.T) {
	keyless, _ := New(config.Provider{Name: "simpleswap", URL: "https://api.simpleswap.io", AffiliateCode: "ref1"})
	if keyless.(provider.Gated).Brokered() {
		t.Error("expected not brokered without key")
	}
	policy := keyless.(provider.APIKeyPolicy)
	if !policy.APIKeyRequiredForQuote() || !policy.APIKeyRequiredForSwap() {
		t.Error("SimpleSwap requires a key for both quotes and swaps")
	}
	link := keyless.(provider.Linker).SwapLink(provider.QuoteRequest{From: "BTC", To: "USDT_POL", Amount: "0.5"})
	for _, want := range []string{"simpleswap.io", "from=btc", "to=usdt", "amount=0.5", "ref=ref1"} {
		if !strings.Contains(link, want) {
			t.Errorf("link %q missing %q", link, want)
		}
	}
	keyed, _ := New(config.Provider{Name: "simpleswap", URL: "https://api.simpleswap.io", APIKey: "k"})
	if !keyed.(provider.Gated).Brokered() {
		t.Error("expected brokered with key")
	}
}

func TestWithAPIKeyReturnsIsolatedClient(t *testing.T) {
	base, _ := New(config.Provider{Name: "simpleswap", URL: "https://api.simpleswap.io"})
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
