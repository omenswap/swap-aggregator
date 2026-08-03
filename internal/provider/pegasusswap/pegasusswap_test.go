package pegasusswap

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"omenswap.com/swap-aggregator/internal/config"
	"omenswap.com/swap-aggregator/internal/provider"
)

func newTestProvider(t *testing.T, h http.Handler) provider.Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(config.Provider{Name: "pegasusswap", Type: "pegasusswap", URL: srv.URL,
		Enabled: true, APIKey: "pub123:secret456"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func wantSignature(payload string) string {
	mac := hmac.New(sha512.New, []byte("secret456"))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestQuoteSignsWithEndpointName(t *testing.T) {
	var query, sig, pub, payload string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/private/exchange-coin", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		sig = r.Header.Get("x-api-signature")
		pub = r.Header.Get("x-api-public-key")
		payload = r.Header.Get("x-api-payload")
		w.Write([]byte(`{"amount":"0.5","receive":"85.5","exchangeRate":"171","minAmount":"0.001","maxAmount":"5"}`))
	})
	p := newTestProvider(t, mux)
	q, err := p.Quote(context.Background(), provider.QuoteRequest{
		From: "BTC", To: "XMR", Amount: "0.5",
		Direction: provider.FromSide, RateType: provider.Floating,
	})
	if err != nil {
		t.Fatal(err)
	}
	if q.ToAmount != "85.5" || q.FromAmount != "0.5" || q.Pair.Rate != "171" || q.Err != "" {
		t.Errorf("quote = %+v", q)
	}
	if pub != "pub123" || payload != "exchange-coin" || sig != wantSignature("exchange-coin") {
		t.Errorf("auth headers: key=%q payload=%q sig=%q", pub, payload, sig)
	}
	for _, want := range []string{"coinFrom=btc", "coinTo=xmr", "lastSource=deposit", "typeSwap=2"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q missing %q", query, want)
		}
	}
}

func TestQuoteFixedAndReverseParams(t *testing.T) {
	var query string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/private/exchange-coin", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Write([]byte(`{"amount":"1","receive":"2","exchangeRate":"2","minAmount":"0","maxAmount":"0"}`))
	})
	p := newTestProvider(t, mux)
	if _, err := p.Quote(context.Background(), provider.QuoteRequest{
		From: "BTC", To: "XMR", Amount: "1",
		Direction: provider.ToSide, RateType: provider.Fixed,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "typeSwap=1") || !strings.Contains(query, "lastSource=receive") {
		t.Errorf("query = %q", query)
	}
}

func TestQuoteBelowMinimum(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/private/exchange-coin", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"amount":"0.0001","receive":"0","exchangeRate":"171","minAmount":"0.01","maxAmount":"5"}`))
	})
	p := newTestProvider(t, mux)
	q, _ := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "XMR", Amount: "0.0001"})
	if q.Err == "" {
		t.Errorf("quote = %+v", q)
	}
}

func TestQuoteUnknownPair(t *testing.T) {
	p := newTestProvider(t, http.NewServeMux())
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "NOPE", Amount: "1"})
	if err != nil || q.Err != "pair not supported" {
		t.Errorf("err=%v quote=%+v", err, q)
	}
}

func TestCreateSwapAndStatus(t *testing.T) {
	var body map[string]any
	const txJSON = `{"orderNumber":"RTGTLN","status":9,"createdAt":"2026-08-03T10:00:00Z",
		"expiredAt":"2026-08-03T11:00:00Z","pairs":{
		"deposit":{"address":"bc1qdeposit","amount":"0.5","coin":"btc"},
		"receive":{"address":"4xmrdest","amount":"85.5","coin":"xmr"}}}`
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/private/create-transaction", func(w http.ResponseWriter, r *http.Request) {
		if p := r.Header.Get("x-api-payload"); p != "create-transaction" {
			t.Errorf("payload header = %q", p)
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(txJSON))
	})
	mux.HandleFunc("GET /api/private/get-transaction", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "RTGTLN" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(txJSON))
	})
	p := newTestProvider(t, mux)
	s, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "BTC", To: "XMR", Amount: "0.5", DestinationAddress: "4xmrdest",
		RefundAddress: "bc1qrefund", RateType: provider.Fixed, Direction: provider.FromSide,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "RTGTLN" || s.Status != "pending" || s.DepositAddress != "bc1qdeposit" {
		t.Errorf("swap = %+v", s)
	}
	if body["receiveAddress"] != "4xmrdest" || body["refundAddress"] != "bc1qrefund" {
		t.Errorf("body = %v", body)
	}
	if body["typeSwap"] != float64(1) {
		t.Errorf("typeSwap = %v", body["typeSwap"])
	}
	got, err := p.Status(context.Background(), "RTGTLN")
	if err != nil {
		t.Fatal(err)
	}
	if got.From != "BTC" || got.To != "XMR" || got.Status != "pending" {
		t.Errorf("status = %+v", got)
	}
}

func TestStatusCodeMapping(t *testing.T) {
	cases := map[int]string{9: "pending", 1: "awaiting_confirmation", 3: "exchanging",
		12: "sending", 4: "completed", 5: "expired", 6: "failed", 13: "refunded", 99: "status_99"}
	for code, want := range cases {
		if got := mapStatus(code); got != want {
			t.Errorf("status %d = %q, want %q", code, got, want)
		}
	}
}

func TestGatedWithoutKey(t *testing.T) {
	p, err := New(config.Provider{Name: "pegasusswap", URL: "https://api.pegasusswap.com"})
	if err != nil {
		t.Fatal(err)
	}
	g, ok := p.(provider.Gated)
	if !ok || g.Brokered() {
		t.Error("must be gated without a key")
	}
	if !provider.SupportsRateMode(p, provider.Fixed, provider.FromSide) {
		t.Error("fixed rates are supported")
	}
	l, ok := p.(provider.Linker)
	if !ok || l.SwapLink(provider.QuoteRequest{From: "BTC", To: "XMR", Amount: "1"}) == "" {
		t.Error("expected a deep link fallback")
	}
}

func TestWithAPIKeyRejectsBadFormat(t *testing.T) {
	p, _ := New(config.Provider{Name: "pegasusswap", URL: "https://api.pegasusswap.com"})
	c := p.(provider.Credentialed)
	if _, err := c.WithAPIKey("nocolon"); err == nil {
		t.Error("expected an error for a key without a secret")
	}
	keyed, err := c.WithAPIKey("pub:sec")
	if err != nil {
		t.Fatal(err)
	}
	if !keyed.(provider.Gated).Brokered() {
		t.Error("keyed clone must be brokered")
	}
}
