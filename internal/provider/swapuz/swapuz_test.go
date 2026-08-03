package swapuz

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

func newTestProvider(t *testing.T, h http.Handler, key string) provider.Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(config.Provider{Name: "swapuz", Type: "swapuz", URL: srv.URL, Enabled: true, APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestQuoteUnwrapsEnvelope(t *testing.T) {
	var query, apiKey string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/home/v1/rate/", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		apiKey = r.Header.Get("Api-key")
		w.Write([]byte(`{"result":{"result":3.36569,"amount":0.1,"rate":33.6725,
			"minAmount":0.001436,"maxAmount":47.874744},"status":200,"message":null}`))
	})
	p := newTestProvider(t, mux, "partner-key")
	q, err := p.Quote(context.Background(), provider.QuoteRequest{
		From: "BTC", To: "ETH", Amount: "0.1",
		Direction: provider.FromSide, RateType: provider.Floating,
	})
	if err != nil {
		t.Fatal(err)
	}
	if q.ToAmount != "3.36569" || q.Pair.Rate != "33.6725" || q.Err != "" {
		t.Errorf("quote = %+v", q)
	}
	if apiKey != "partner-key" {
		t.Errorf("Api-key header = %q", apiKey)
	}
	for _, want := range []string{"from=BTC", "fromNetwork=BTC", "to=ETH", "mode=float"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q missing %q", query, want)
		}
	}
}

func TestQuoteFixedUsesFixMode(t *testing.T) {
	var query string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/home/v1/rate/", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Write([]byte(`{"result":{"result":1,"rate":1,"minAmount":0,"maxAmount":0},"status":200}`))
	})
	p := newTestProvider(t, mux, "")
	if _, err := p.Quote(context.Background(), provider.QuoteRequest{
		From: "BTC", To: "ETH", Amount: "1", RateType: provider.Fixed,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "mode=fix") {
		t.Errorf("query = %q", query)
	}
}

// A 200 carrying status 400 is still a failure.
func TestQuoteMapsBodyErrors(t *testing.T) {
	for msg, want := range map[string]string{
		"Rate - coin direction not available for market. BTC-BTC to ETH-ETH": "pair not supported",
		"Check amount(s) or coins wrong":                                     "invalid amount",
	} {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/home/v1/rate/", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"result": nil, "status": 400, "message": msg})
		})
		p := newTestProvider(t, mux, "")
		q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "ETH", Amount: "1"})
		if err != nil {
			t.Fatalf("%s: %v", msg, err)
		}
		if q.Err != want {
			t.Errorf("%q mapped to %q, want %q", msg, q.Err, want)
		}
	}
}

func TestQuoteRejectsReverseDirection(t *testing.T) {
	p := newTestProvider(t, http.NewServeMux(), "")
	if provider.SupportsRateMode(p, provider.Fixed, provider.ToSide) {
		t.Error("receive-side quoting is not functional and must not be advertised")
	}
	if !provider.SupportsRateMode(p, provider.Fixed, provider.FromSide) {
		t.Error("fixed send-side quoting is supported")
	}
}

func TestCreateSwapSendsRandomUUID(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/home/v1/order", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"result":{"uid":"order-1","status":0,"amount":0.5,"amountResult":85.5,
			"addressFrom":"bc1qdeposit","addressTo":"4xmrdest","from":{"shortName":"BTC"},
			"to":{"shortName":"XMR"},"finishPayment":"2026-08-03T11:00:00Z"},"status":200}`))
	})
	p := newTestProvider(t, mux, "")
	s, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "BTC", To: "XMR", Amount: "0.5", DestinationAddress: "4xmrdest",
		RateType: provider.Fixed, Direction: provider.FromSide,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "order-1" || s.DepositAddress != "bc1qdeposit" || s.Status != "pending" {
		t.Errorf("swap = %+v", s)
	}
	if body["address"] != "4xmrdest" || body["modeCurs"] != "fixed" {
		t.Errorf("body = %v", body)
	}
	uuid, _ := body["uuid"].(string)
	if len(uuid) != 32 {
		t.Errorf("uuid = %q, want 32 hex chars", uuid)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := map[int]string{0: "pending", 1: "awaiting_confirmation", 2: "exchanging",
		3: "exchanging", 4: "exchanging", 5: "sending", 6: "completed",
		10: "expired", 11: "refunded", 12: "on_hold", 99: "status_99"}
	for code, want := range cases {
		if got := mapStatus(code); got != want {
			t.Errorf("status %d = %q, want %q", code, got, want)
		}
	}
}

func TestStatusFetchesByUID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/order/uid/{uid}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("uid") != "abc123" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"result":{"uid":"abc123","status":6,"from":{"shortName":"BTC"},
			"to":{"shortName":"XMR"},"amountResult":85.5},"status":200}`))
	})
	p := newTestProvider(t, mux, "")
	s, err := p.Status(context.Background(), "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != "completed" || s.From != "BTC" || s.To != "XMR" {
		t.Errorf("swap = %+v", s)
	}
}
