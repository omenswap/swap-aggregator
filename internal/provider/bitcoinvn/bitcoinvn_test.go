package bitcoinvn

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

const quoteJSON = `{"id":"bb4e1fb5-30e0-4451-b629-1c58b4180818","depositAmount":2,"depositFee":0.0001,"depositMethod":"btc","settleAmount":339.64,"settleFee":0.001,"settleMethod":"xmr","rate":169.82,"rawRate":174.22,"createdAt":"2026-08-02T04:04:35+00:00","expiresAt":"2026-08-02T04:19:35+00:00","accepted":false}`

const belowMinJSON = `{"code":400,"message":"Validation Failed","errors":{"children":{"id":{},"depositMethod":{},"depositAmount":{"errors":["Minimum order size is 0.00007890 BTC."]},"settleMethod":{},"settleAmount":{},"markupRate":{}}}}`

const aboveMaxJSON = `{"code":400,"message":"Validation Failed","errors":{"children":{"id":{},"depositMethod":{},"depositAmount":{"errors":["Maximum order size is 1.57791701 BTC."]},"settleMethod":{},"settleAmount":{},"markupRate":{}}}}`

const orderJSON = `{
"id":"8120a8a8-8dd1-4168-80fc-c4d932ce5b93","shortId":"BV2MTZAF","status":"new",
"depositMethod":"eth","depositAsset":"ETH","depositAmount":1.5,"depositFee":0.001,
"depositData":{"address":"0xdeadbeef"},"depositTxns":["txin123"],
"settleMethod":"usdc","settleAsset":"USDC","settleAmount":4850.1,"settleFee":2.5,
"settleData":{"address":"0xdest"},"settleTxns":["txout456"],
"rate":3233.4,"depositMin":0.001,"depositMax":10,
"createdAt":"2026-08-02T04:00:00+00:00","expiresAt":"2026-08-09T04:00:00+00:00"}`

func newTestProvider(t *testing.T, h http.Handler) provider.Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(config.Provider{Name: "bitcoinvn", Type: "bitcoinvn", URL: srv.URL + "/", Enabled: true, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestQuoteComputesAmount(t *testing.T) {
	var auth string
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/quotes", func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("X-API-KEY")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(quoteJSON))
	})
	p := newTestProvider(t, mux)
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "XMR", Amount: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Err != "" {
		t.Fatalf("quote err: %s", q.Err)
	}
	if auth != "test-key" {
		t.Errorf("X-API-KEY = %q", auth)
	}
	if body["depositMethod"] != "btc" || body["settleMethod"] != "xmr" || body["depositAmount"] != 2.0 {
		t.Errorf("request body = %v", body)
	}
	if q.ToAmount != "339.64" {
		t.Errorf("ToAmount = %q", q.ToAmount)
	}
	if q.Pair.Rate != "169.82" {
		t.Errorf("Rate = %q", q.Pair.Rate)
	}
	if q.Provider != "bitcoinvn" {
		t.Errorf("Provider = %q", q.Provider)
	}
}

func TestQuoteBelowMin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/quotes", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
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
	mux.HandleFunc("POST /api/quotes", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(aboveMaxJSON))
	})
	p := newTestProvider(t, mux)
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "XMR", Amount: "10000"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "maximum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteUnsupportedPair(t *testing.T) {
	p := newTestProvider(t, http.NewServeMux())
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "NOTACOIN", To: "BTC", Amount: "100"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "pair not supported") {
		t.Errorf("Err = %q", q.Err)
	}
	q, err = p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "BTC", Amount: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "pair not supported") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestCreateSwap(t *testing.T) {
	var auth string
	var quoteBody, orderBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/quotes", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&quoteBody); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"quote-uuid-1","depositAmount":1.5,"depositMethod":"eth","settleAmount":4850.1,"settleMethod":"usdc","rate":3233.4}`))
	})
	mux.HandleFunc("POST /api/orders", func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("X-API-KEY")
		if err := json.NewDecoder(r.Body).Decode(&orderBody); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(orderJSON))
	})
	p := newTestProvider(t, mux)
	s, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "test-key" {
		t.Errorf("X-API-KEY = %q", auth)
	}
	if quoteBody["depositMethod"] != "eth" || quoteBody["settleMethod"] != "usdc" || quoteBody["depositAmount"] != 1.5 {
		t.Errorf("quote body = %v", quoteBody)
	}
	if orderBody["quote"] != "quote-uuid-1" {
		t.Errorf("order body = %v", orderBody)
	}
	settleData, ok := orderBody["settleData"].(map[string]any)
	if !ok || settleData["address"] != "0xdest" {
		t.Errorf("settleData = %v", orderBody["settleData"])
	}
	if _, ok := orderBody["referrer"]; ok {
		t.Error("referrer should be omitted")
	}
	if s.ID != "BV2MTZAF" || s.DepositAddress != "0xdeadbeef" ||
		s.FromAmount != "1.5" || s.ToAmountEstimated != "4850.1" ||
		s.Status != "pending" || s.DestinationAddress != "0xdest" ||
		s.From != "ETH" || s.To != "USDC" ||
		s.DepositTxHash != "txin123" || s.PayoutTxHash != "txout456" ||
		s.ExpiresAt != "2026-08-09T04:00:00+00:00" || s.CreatedAt != "2026-08-02T04:00:00+00:00" {
		t.Errorf("swap = %+v", s)
	}
}

func TestCreateSwapSendsReferrer(t *testing.T) {
	var orderBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/quotes", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"quote-uuid-1"}`))
	})
	mux.HandleFunc("POST /api/orders", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&orderBody)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(orderJSON))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p, err := New(config.Provider{Name: "bitcoinvn", URL: srv.URL, APIKey: "k", AffiliateCode: "ref1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if orderBody["referrer"] != "ref1" {
		t.Errorf("referrer = %v", orderBody["referrer"])
	}
}

func TestStatusMapping(t *testing.T) {
	cases := map[string]string{
		"new":               "pending",
		"pending":           "awaiting_confirmation",
		"processing":        "deposited",
		"on_hold":           "deposited",
		"settle_data_error": "failed",
		"completed":         "completed",
		"canceled":          "expired",
		"mystery":           "mystery",
	}
	var current string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "BV2MTZAF" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(strings.Replace(orderJSON, `"status":"new"`, `"status":"`+current+`"`, 1)))
	})
	p := newTestProvider(t, mux)
	for apiStatus, want := range cases {
		current = apiStatus
		s, err := p.Status(context.Background(), "BV2MTZAF")
		if err != nil {
			t.Fatal(err)
		}
		if s.Status != want {
			t.Errorf("status %q mapped to %q, want %q", apiStatus, s.Status, want)
		}
		if s.From != "ETH" || s.To != "USDC" {
			t.Errorf("From/To = %q/%q", s.From, s.To)
		}
	}
}

func TestErrorPayloadSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"message":"Order not found.","code":404}}`))
	})
	p := newTestProvider(t, mux)
	_, err := p.Status(context.Background(), "nope")
	if err == nil || !strings.Contains(err.Error(), "Order not found") {
		t.Errorf("err = %v", err)
	}
}

func TestQuoteTransportError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/quotes", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"Too many requests.","code":429}}`))
	})
	p := newTestProvider(t, mux)
	_, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "XMR", Amount: "1"})
	if err == nil || !strings.Contains(err.Error(), "Too many requests") {
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
	keyless, _ := New(config.Provider{Name: "bitcoinvn", URL: "https://bitcoinvn.io", AffiliateCode: "ref1"})
	if !keyless.(provider.Gated).Brokered() {
		t.Error("expected brokered without key: API is keyless")
	}
	link := keyless.(provider.Linker).SwapLink(provider.QuoteRequest{From: "BTC", To: "XMR", Amount: "0.5"})
	for _, want := range []string{"bitcoinvn.io", "deposit=btc", "settle=xmr", "depositAmount=0.5", "ref=ref1"} {
		if !strings.Contains(link, want) {
			t.Errorf("link %q missing %q", link, want)
		}
	}
	keyed, _ := New(config.Provider{Name: "bitcoinvn", URL: "https://bitcoinvn.io", APIKey: "k"})
	if !keyed.(provider.Gated).Brokered() {
		t.Error("expected brokered with key")
	}
	link = keyed.(provider.Linker).SwapLink(provider.QuoteRequest{From: "USDT_TRON", To: "USDC_POL", Amount: "100"})
	for _, want := range []string{"deposit=usdttrc20", "settle=usdcpolygon2", "depositAmount=100"} {
		if !strings.Contains(link, want) {
			t.Errorf("link %q missing %q", link, want)
		}
	}
	if strings.Contains(link, "ref=") {
		t.Errorf("link %q should not contain ref", link)
	}
}
