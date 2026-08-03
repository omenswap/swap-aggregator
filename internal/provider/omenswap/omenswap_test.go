package omenswap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"omenswap.com/swap-aggregator/internal/config"
	"omenswap.com/swap-aggregator/internal/provider"
)

const pairsJSON = `{"pairs":[
{"symbol":"ETHUSDC","from_token":"ETH","to_token":"USDC","rate":"3250.50",
 "fees":{"floating":"0.5%","fixed":"1.0%"},
 "max_from_amount":{"floating":"10.5","fixed":"9.8"},
 "min_from_amount":{"floating":"0.01","fixed":"0.02"},
 "available_reserve":"50000"},
{"symbol":"ETHXMR","from_token":"ETH","to_token":"XMR","price_unavailable":true,
 "fees":{"floating":"0.5%","fixed":"1.0%"},
 "max_from_amount":{"floating":"5","fixed":"5"},
 "min_from_amount":{"floating":"0.1","fixed":"0.1"},
 "available_reserve":"0"}
]}`

const swapJSON = `{
"id":"11111111-2222-3333-4444-555555555555","pair_symbol":"ETHUSDC","rate_type":"floating",
"status":"pending","from_token":"ETH","to_token":"USDC",
"from_amount":"1.5","to_amount_estimated":"4850.1","fee_amount_estimated":"24.2",
"rate_at_creation":"3250.5","deposit_address":"0xdeadbeef","destination_address":"0xdest",
"expires_at":"2026-08-02T20:00:00Z","created_at":"2026-08-01T20:00:00Z"}`

func newTestProvider(t *testing.T, h http.Handler, affiliate string) provider.Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := New(config.Provider{Name: "omenswap", Type: "omenswap", URL: srv.URL, Enabled: true, AffiliateCode: affiliate})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func pairsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/pairs", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(pairsJSON))
	})
	return mux
}

func TestPairs(t *testing.T) {
	p := newTestProvider(t, pairsHandler(), "")
	pairs, err := p.Pairs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 {
		t.Fatalf("got %d pairs", len(pairs))
	}
	eth := pairs[0]
	if eth.From != "ETH" || eth.To != "USDC" || eth.Symbol != "ETHUSDC" ||
		eth.Rate != "3250.50" || eth.MinFrom != "0.01" || eth.MaxFrom != "10.5" ||
		eth.Fee != "0.5%" || eth.Unavailable {
		t.Errorf("pair = %+v", eth)
	}
	if !pairs[1].Unavailable {
		t.Error("expected ETHXMR unavailable")
	}
}

func TestQuoteComputesAmount(t *testing.T) {
	p := newTestProvider(t, pairsHandler(), "")
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
	if q.Provider != "omenswap" {
		t.Errorf("Provider = %q", q.Provider)
	}
}

func TestQuoteBelowMin(t *testing.T) {
	p := newTestProvider(t, pairsHandler(), "")
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "USDC", Amount: "0.001"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "minimum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteAboveMax(t *testing.T) {
	p := newTestProvider(t, pairsHandler(), "")
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "USDC", Amount: "100"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "maximum") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteUnknownPair(t *testing.T) {
	p := newTestProvider(t, pairsHandler(), "")
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "BTC", To: "DOGE", Amount: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "not supported") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestQuoteUnavailablePair(t *testing.T) {
	p := newTestProvider(t, pairsHandler(), "")
	q, err := p.Quote(context.Background(), provider.QuoteRequest{From: "ETH", To: "XMR", Amount: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Err, "unavailable") {
		t.Errorf("Err = %q", q.Err)
	}
}

func TestCreateSwapSendsAffiliateAsSource(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/pairs", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(pairsJSON))
	})
	mux.HandleFunc("POST /api/v1/swaps", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(swapJSON))
	})
	p := newTestProvider(t, mux, "abc")
	s, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["source"] != "abc" || body["rate_type"] != "floating" ||
		body["pair_symbol"] != "ETHUSDC" || body["from_amount"] != "1.5" {
		t.Errorf("request body = %v", body)
	}
	if _, ok := body["refund_address"]; ok {
		t.Error("refund_address should be omitted")
	}
	if s.ID != "11111111-2222-3333-4444-555555555555" || s.DepositAddress != "0xdeadbeef" ||
		s.Status != "pending" || s.ToAmountEstimated != "4850.1" {
		t.Errorf("swap = %+v", s)
	}
}

func TestCreateSwapOmitsEmptySource(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/pairs", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(pairsJSON))
	})
	mux.HandleFunc("POST /api/v1/swaps", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(swapJSON))
	})
	p := newTestProvider(t, mux, "")
	_, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["source"]; ok {
		t.Error("source should be omitted when no affiliate code")
	}
}

func TestStatusMapsFields(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/swaps/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "11111111-2222-3333-4444-555555555555" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(swapJSON))
	})
	p := newTestProvider(t, mux, "")
	s, err := p.Status(context.Background(), "11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	if s.From != "ETH" || s.To != "USDC" || s.FromAmount != "1.5" ||
		s.DestinationAddress != "0xdest" || s.ExpiresAt != "2026-08-02T20:00:00Z" {
		t.Errorf("swap = %+v", s)
	}
}

func TestErrorPayloadSurfaced(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/pairs", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(pairsJSON))
	})
	mux.HandleFunc("POST /api/v1/swaps", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"insufficient reserves for pair","code":"INSUFFICIENT_RESERVES"}`))
	})
	p := newTestProvider(t, mux, "")
	_, err := p.CreateSwap(context.Background(), provider.SwapRequest{
		From: "ETH", To: "USDC", Amount: "1.5", DestinationAddress: "0xdest",
	})
	if err == nil || !strings.Contains(err.Error(), "insufficient reserves") {
		t.Errorf("err = %v", err)
	}
}

// The API prices fixed rates as from = to/rate + networkFee, so a send-side
// request has to be solved for the receive amount that lands on it.
func fixedHandler(t *testing.T, calls *[]string) http.Handler {
	t.Helper()
	const rate, networkFee = 3250.50, 0.001
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/pairs", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(pairsJSON))
	})
	mux.HandleFunc("POST /api/v1/quote", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["from_amount"] != "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"code":"INVALID_AMOUNT","error":"from_amount must not be set for fixed rate; specify to_amount"}`))
			return
		}
		*calls = append(*calls, body["to_amount"])
		to, err := strconv.ParseFloat(body["to_amount"], 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"bad to_amount"}`))
			return
		}
		from := to/rate + networkFee
		json.NewEncoder(w).Encode(map[string]any{
			"from_amount": strconv.FormatFloat(from, 'f', 18, 64),
			"to_amount":   body["to_amount"],
			"rate":        "3250.50",
		})
	})
	return mux
}

func TestFixedQuoteSolvesFromSendAmount(t *testing.T) {
	var calls []string
	p := newTestProvider(t, fixedHandler(t, &calls), "")
	if !provider.SupportsRateMode(p, provider.Fixed, provider.FromSide) {
		t.Fatal("fixed rates must be quotable from the send amount")
	}
	q, err := p.Quote(context.Background(), provider.QuoteRequest{
		From: "ETH", To: "USDC", Amount: "1.5",
		Direction: provider.FromSide, RateType: provider.Fixed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if q.Err != "" {
		t.Fatalf("quote error: %s", q.Err)
	}
	from, err := strconv.ParseFloat(q.FromAmount, 64)
	if err != nil {
		t.Fatalf("from amount %q: %v", q.FromAmount, err)
	}
	if from > 1.5 {
		t.Errorf("deposit %v exceeds the requested 1.5", from)
	}
	if from < 1.4985 {
		t.Errorf("deposit %v is not close enough to the requested 1.5", from)
	}
	if q.RateType != provider.Fixed || q.Pair.Fee != "1.0%" {
		t.Errorf("quote = %+v", q)
	}
	if len(calls) > 3 {
		t.Errorf("solved in %d calls: %v", len(calls), calls)
	}
}

func TestFixedQuoteRejectsUnavailablePair(t *testing.T) {
	var calls []string
	p := newTestProvider(t, fixedHandler(t, &calls), "")
	q, _ := p.Quote(context.Background(), provider.QuoteRequest{
		From: "ETH", To: "XMR", Amount: "1",
		Direction: provider.FromSide, RateType: provider.Fixed,
	})
	if q.Err == "" {
		t.Error("expected an error for an unavailable pair")
	}
	if len(calls) != 0 {
		t.Errorf("called the API for an unavailable pair: %v", calls)
	}
}
