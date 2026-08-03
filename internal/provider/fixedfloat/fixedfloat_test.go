package fixedfloat

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"omenswap.com/swap-aggregator/internal/config"
	"omenswap.com/swap-aggregator/internal/provider"
)

const priceJSON = `{"code":0,"msg":"OK","data":{
"from":{"code":"ETH","coin":"ETH","network":"ETH","amount":"2","rate":"0.05","precision":8,"min":"0.01","max":"10","usd":"6000"},
"to":{"code":"BTC","coin":"BTC","network":"BTC","amount":"0.1","rate":"20","precision":8,"min":"0.0005","max":"0.5","usd":"6000"}
}}`

const orderJSON = `{"code":0,"msg":"OK","data":{
"id":"ABC123","type":"float","status":"NEW","token":"tok0xdead",
"time":{"reg":1754000000,"start":null,"finish":null,"update":1754000000,"expiration":1754001800,"left":1800},
"from":{"code":"ETH","coin":"ETH","network":"ETH","name":"Ethereum","amount":"2","address":"0xdeposit","tag":null,
 "tx":{"id":"0xintx","amount":null,"fee":null,"ccyfee":null,"timeReg":null,"timeBlock":null,"confirmations":null}},
"to":{"code":"BTC","coin":"BTC","network":"BTC","name":"Bitcoin","amount":"0.1","address":"bc1qdest","tag":null,
 "tx":{"id":"btctx","amount":null,"fee":null,"ccyfee":null,"timeReg":null,"timeBlock":null,"confirmations":null}},
"back":{},"emergency":{"status":[],"choice":"NONE","repeat":"0"}
}}`

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func newTestProvider(t *testing.T, h http.Handler) provider.Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(config.Provider{
		Name: "fixedfloat", Type: "fixedfloat", URL: srv.URL + "/",
		Enabled: true, APIKey: "testkey:testsecret",
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestProviderWith(t *testing.T, h http.Handler, cfg config.Provider) provider.Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg.Type = "fixedfloat"
	cfg.URL = srv.URL + "/"
	cfg.Enabled = true
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func authHandler(t *testing.T, path, response string, capture *map[string]any) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if got := r.Header.Get("X-API-KEY"); got != "testkey" {
			t.Errorf("X-API-KEY = %q", got)
		}
		if got, want := r.Header.Get("X-API-SIGN"), sign("testsecret", body); got != want {
			t.Errorf("X-API-SIGN = %q, want %q", got, want)
		}
		if capture != nil {
			if err := json.Unmarshal(body, capture); err != nil {
				t.Error(err)
			}
		}
		w.Write([]byte(response))
	})
	return mux
}

func TestQuoteComputesAmount(t *testing.T) {
	var body map[string]any
	p := newTestProvider(t, authHandler(t, "/api/v2/price", priceJSON, &body))
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "BTC", Amount: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Err != "" {
		t.Fatalf("quote err: %s", q.Err)
	}
	if q.Provider != "fixedfloat" {
		t.Errorf("Provider = %q", q.Provider)
	}
	if q.ToAmount != "0.1" {
		t.Errorf("ToAmount = %q", q.ToAmount)
	}
	if q.Pair.Rate != "0.05" {
		t.Errorf("Rate = %q", q.Pair.Rate)
	}
	if q.Pair.MinFrom != "0.01" || q.Pair.MaxFrom != "10" {
		t.Errorf("MinFrom = %q MaxFrom = %q", q.Pair.MinFrom, q.Pair.MaxFrom)
	}
	if body["fromCcy"] != "ETH" || body["toCcy"] != "BTC" || body["type"] != "float" ||
		body["direction"] != "from" || body["amount"] != "2" {
		t.Errorf("request body = %v", body)
	}
}

func TestQuoteMapsCurrencyCodes(t *testing.T) {
	var body map[string]any
	p := newTestProvider(t, authHandler(t, "/api/v2/price", priceJSON, &body))
	if _, err := p.Quote(context.Background(), provider.QuoteRequest{From: "USDT_TRX", To: "USDC_POL", Amount: "2"}); err != nil {
		t.Fatal(err)
	}
	if body["fromCcy"] != "USDTTRC" || body["toCcy"] != "USDCMATIC" {
		t.Errorf("request body = %v", body)
	}
}

func TestQuoteBelowMin(t *testing.T) {
	p := newTestProvider(t, authHandler(t, "/api/v2/price", `{"code":301,"msg":"Amount is less than the min limit: 0.01 ETH","data":null}`, nil))
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "BTC", Amount: "0.001"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "minimum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteAboveMax(t *testing.T) {
	p := newTestProvider(t, authHandler(t, "/api/v2/price", `{"code":301,"msg":"Amount exceeds the max limit: 10 ETH","data":null}`, nil))
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "BTC", Amount: "100"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "maximum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteUnsupportedPair(t *testing.T) {
	p := newTestProvider(t, http.NewServeMux())
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "FOO", To: "BTC", Amount: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "pair not supported") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestCreateSwap(t *testing.T) {
	var body map[string]any
	p := newTestProvider(t, authHandler(t, "/api/v2/create", orderJSON, &body))
	s, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "BTC", Amount: "2", DestinationAddress: "bc1qdest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["fromCcy"] != "ETH" || body["toCcy"] != "BTC" || body["type"] != "float" ||
		body["direction"] != "from" || body["amount"] != "2" || body["toAddress"] != "bc1qdest" {
		t.Errorf("request body = %v", body)
	}
	if _, ok := body["refundAddress"]; ok {
		t.Error("refundAddress should be omitted")
	}
	if s.ID != "ABC123:tok0xdead" {
		t.Errorf("ID = %q", s.ID)
	}
	if s.DepositAddress != "0xdeposit" || s.Status != "pending" ||
		s.FromAmount != "2" || s.ToAmountEstimated != "0.1" {
		t.Errorf("swap = %+v", s)
	}
	if s.From != "ETH" || s.To != "BTC" || s.DestinationAddress != "bc1qdest" {
		t.Errorf("swap = %+v", s)
	}
}

// /api/v2/create takes no refund address; it is set later via /api/v2/emergency.
func TestCreateSwapOmitsRefundAddress(t *testing.T) {
	var body map[string]any
	p := newTestProvider(t, authHandler(t, "/api/v2/create", orderJSON, &body))
	_, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "BTC", Amount: "2", DestinationAddress: "bc1qdest", RefundAddress: "0xrefund",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["refundAddress"]; ok {
		t.Errorf("sent an undocumented refundAddress: %v", body)
	}
}

func TestQuoteAndCreateSendAffiliate(t *testing.T) {
	var body map[string]any
	p := newTestProviderWith(t, authHandler(t, "/api/v2/create", orderJSON, &body),
		config.Provider{Name: "fixedfloat", APIKey: "testkey:testsecret", AffiliateCode: "REF1", AffiliateFeePercent: 0.6})
	if _, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "BTC", Amount: "2", DestinationAddress: "bc1qdest",
	}); err != nil {
		t.Fatal(err)
	}
	if body["refcode"] != "REF1" || body["afftax"] != "0.6" {
		t.Errorf("affiliate params missing: %v", body)
	}
}

func TestStatusSplitsIDAndMapsStatuses(t *testing.T) {
	cases := map[string]string{
		"NEW":       "pending",
		"PENDING":   "awaiting_confirmation",
		"EXCHANGE":  "deposited",
		"WITHDRAW":  "deposited",
		"DONE":      "completed",
		"EXPIRED":   "expired",
		"EMERGENCY": "failed",
		"WEIRD":     "WEIRD",
	}
	for ffStatus, want := range cases {
		var body map[string]any
		resp := strings.Replace(orderJSON, `"status":"NEW"`, `"status":"`+ffStatus+`"`, 1)
		p := newTestProvider(t, authHandler(t, "/api/v2/order", resp, &body))
		s, err := p.Status(context.Background(), "ABC123:tok0xdead")
		if err != nil {
			t.Fatal(err)
		}
		if body["id"] != "ABC123" || body["token"] != "tok0xdead" {
			t.Errorf("request body = %v", body)
		}
		if s.Status != want {
			t.Errorf("status %s mapped to %q, want %q", ffStatus, s.Status, want)
		}
		if s.ID != "ABC123:tok0xdead" {
			t.Errorf("ID = %q", s.ID)
		}
	}
}

func TestNonzeroCodeSurfacesMsg(t *testing.T) {
	p := newTestProvider(t, authHandler(t, "/api/v2/create", `{"code":500,"msg":"Invalid address","data":null}`, nil))
	_, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "BTC", Amount: "2", DestinationAddress: "bad",
	})
	if err == nil || !strings.Contains(err.Error(), "Invalid address") {
		t.Errorf("err = %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "fixedfloat") {
		t.Errorf("err = %v", err)
	}
}

func TestPairsExcludesSameToken(t *testing.T) {
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
			t.Errorf("pair %s/%s has same from and to", pr.From, pr.To)
		}
		if pr.Unavailable {
			t.Errorf("pair %s/%s unavailable", pr.From, pr.To)
		}
		seen[pr.From+"/"+pr.To] = true
	}
	for _, want := range []string{"BTC/ETH", "XMR/BTC", "USDT/USDT_TRX", "SOL/DOGE", "LTC/USDC_POL", "USDC/BTC"} {
		if !seen[want] {
			t.Errorf("missing pair %s", want)
		}
	}
	if len(pairs) != len(seen) {
		t.Error("duplicate pairs")
	}
}

func TestNewSplitsKeySecret(t *testing.T) {
	p, err := New(config.Provider{Name: "fixedfloat", URL: "https://ff.io/", APIKey: "onlykey"})
	if err != nil {
		t.Fatal(err)
	}
	c := p.(*client)
	if c.key != "onlykey" || c.secret != "" {
		t.Errorf("key = %q secret = %q", c.key, c.secret)
	}
	if c.baseURL != "https://ff.io" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
}

func TestGatingAndSwapLink(t *testing.T) {
	keyless, _ := New(config.Provider{Name: "fixedfloat", URL: "https://ff.io", AffiliateCode: "ref1"})
	if keyless.(provider.Gated).Brokered() {
		t.Error("expected not brokered without key")
	}
	policy := keyless.(provider.APIKeyPolicy)
	if !policy.APIKeyRequiredForQuote() || !policy.APIKeyRequiredForSwap() {
		t.Error("FixedFloat requires a key and secret for both quotes and swaps")
	}
	keyOnly, _ := New(config.Provider{Name: "fixedfloat", URL: "https://ff.io", APIKey: "k"})
	if keyOnly.(provider.Gated).Brokered() {
		t.Error("expected not brokered without secret")
	}
	link := keyless.(provider.Linker).SwapLink(provider.QuoteRequest{From: "BTC", To: "USDT", Amount: "0.5"})
	for _, want := range []string{"ff.io", "fromccy=BTC", "toccy=USDTETH", "qty=0.5", "ref=ref1"} {
		if !strings.Contains(link, want) {
			t.Errorf("link %q missing %q", link, want)
		}
	}
	keyed, _ := New(config.Provider{Name: "fixedfloat", URL: "https://ff.io", APIKey: "k:s"})
	if !keyed.(provider.Gated).Brokered() {
		t.Error("expected brokered with key and secret")
	}
}

func TestWithAPIKeyValidatesAndReturnsIsolatedClient(t *testing.T) {
	base, _ := New(config.Provider{Name: "fixedfloat", URL: "https://ff.io"})
	if _, err := base.(provider.Credentialed).WithAPIKey("key-without-secret"); err == nil {
		t.Fatal("expected key:secret validation error")
	}
	keyed, err := base.(provider.Credentialed).WithAPIKey(" visitor-key:visitor-secret ")
	if err != nil {
		t.Fatal(err)
	}
	if base.(provider.Gated).Brokered() {
		t.Fatal("base provider was mutated")
	}
	c := keyed.(*client)
	if !c.Brokered() || c.key != "visitor-key" || c.secret != "visitor-secret" {
		t.Fatalf("keyed provider = %#v", keyed)
	}
}
