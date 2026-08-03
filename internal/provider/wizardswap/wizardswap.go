package wizardswap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"omenswap.com/swap-aggregator/internal/config"
	"omenswap.com/swap-aggregator/internal/provider"
)

func init() {
	provider.Register("wizardswap", New)
}

var symbols = []string{"BTC", "DOGE", "ETH", "LTC", "XMR"}

var coins = map[string]string{
	"ZEC":  "zec",
	"BTC":  "btc",
	"DOGE": "doge",
	"ETH":  "eth",
	"LTC":  "ltc",
	"XMR":  "xmr",
}

var statusMap = map[string]string{
	"waiting":    "pending",
	"confirming": "awaiting_confirmation",
	"verifying":  "deposited",
	"exchanging": "deposited",
	"sending":    "deposited",
	"finished":   "completed",
	"failed":     "failed",
	"refunded":   "refunded",
}

type client struct {
	name      string
	baseURL   string
	apiKey    string
	affiliate string
	http      *http.Client
}

func (c *client) Brokered() bool { return true }

// The api_key body field is what earns the referral share.
func (c *client) referralKey() string {
	if c.apiKey != "" {
		return c.apiKey
	}
	return c.affiliate
}

func (c *client) SwapLink(req provider.QuoteRequest) string {
	v := url.Values{}
	if f, ok := coins[req.From]; ok {
		v.Set("from", f)
	}
	if t, ok := coins[req.To]; ok {
		v.Set("to", t)
	}
	if req.Amount != "" {
		v.Set("amount", req.Amount)
	}
	if c.affiliate != "" {
		v.Set("ref", c.affiliate)
	}
	return "https://www.wizardswap.io/?" + v.Encode()
}

func New(cfg config.Provider) (provider.Provider, error) {
	return &client{
		name:      cfg.Name,
		baseURL:   strings.TrimRight(cfg.URL, "/"),
		apiKey:    cfg.APIKey,
		affiliate: cfg.AffiliateCode,
		http:      provider.HTTPClient(cfg, 10*time.Second),
	}, nil
}

func (c *client) Name() string { return c.name }

func (c *client) Pairs(ctx context.Context) ([]provider.Pair, error) {
	pairs := make([]provider.Pair, 0, len(symbols)*(len(symbols)-1))
	for _, from := range symbols {
		for _, to := range symbols {
			if from == to {
				continue
			}
			pairs = append(pairs, provider.Pair{From: from, To: to})
		}
	}
	return pairs, nil
}

type apiEstimate struct {
	EstimatedAmount json.RawMessage `json:"estimated_amount"`
}

func (c *client) Quote(ctx context.Context, req provider.QuoteRequest) (provider.Quote, error) {
	q := provider.Quote{Provider: c.name, Pair: provider.Pair{From: req.From, To: req.To}}
	from, okFrom := coins[req.From]
	to, okTo := coins[req.To]
	if !okFrom || !okTo || req.From == req.To {
		q.Err = "pair not supported"
		return q, nil
	}
	amount, ok := new(big.Rat).SetString(req.Amount)
	if !ok || amount.Sign() <= 0 {
		q.Err = "invalid amount"
		return q, nil
	}

	body := map[string]any{
		"currency_from": from,
		"currency_to":   to,
		"amount_from":   req.Amount,
	}
	if k := c.referralKey(); k != "" {
		body["api_key"] = k
	}
	raw, err := c.post(ctx, "/api/estimate", body)
	if err != nil {
		return provider.Quote{}, err
	}
	var e apiEstimate
	if err := json.Unmarshal(raw, &e); err != nil {
		return provider.Quote{}, fmt.Errorf("wizardswap: %s", err)
	}
	est := rawText(e.EstimatedAmount)
	if est == "false" {
		q.Err = fmt.Sprintf("below minimum for %s/%s", req.From, req.To)
		return q, nil
	}
	toAmount, ok := new(big.Rat).SetString(est)
	if !ok || toAmount.Sign() <= 0 {
		if strings.Contains(strings.ToLower(est), "insufficient liquidity") {
			q.Err = fmt.Sprintf("above maximum for %s/%s: insufficient liquidity", req.From, req.To)
			return q, nil
		}
		if est != "" {
			return provider.Quote{}, fmt.Errorf("wizardswap: %s", est)
		}
		q.Err = "pair currently unavailable"
		return q, nil
	}
	q.FromAmount = req.Amount
	q.RateType = provider.Floating
	q.ToAmount = trimZeros(toAmount.FloatString(12))
	q.Pair.Rate = trimZeros(new(big.Rat).Quo(toAmount, amount).FloatString(12))
	return q, nil
}

type apiExchange struct {
	ID             string          `json:"id"`
	Status         string          `json:"status"`
	CurrencyFrom   string          `json:"currency_from"`
	CurrencyTo     string          `json:"currency_to"`
	AmountFrom     json.RawMessage `json:"amount_from"`
	ExpectedAmount json.RawMessage `json:"expected_amount"`
	AmountTo       json.RawMessage `json:"amount_to"`
	AddressFrom    string          `json:"address_from"`
	AddressTo      string          `json:"address_to"`
	TxFrom         string          `json:"tx_from"`
	TxTo           string          `json:"tx_to"`
	Timestamp      string          `json:"timestamp"`
}

func (x apiExchange) toSwap() provider.Swap {
	s := provider.Swap{
		ID:                 x.ID,
		Status:             mapStatus(x.Status),
		From:               canonical(x.CurrencyFrom),
		To:                 canonical(x.CurrencyTo),
		FromAmount:         firstNonEmpty(rawText(x.ExpectedAmount), rawText(x.AmountFrom)),
		ToAmountEstimated:  rawText(x.AmountTo),
		DepositAddress:     x.AddressFrom,
		DestinationAddress: x.AddressTo,
		DepositTxHash:      x.TxFrom,
		PayoutTxHash:       x.TxTo,
		CreatedAt:          x.Timestamp,
	}
	if s.Status == "completed" {
		s.ToAmountActual = s.ToAmountEstimated
	}
	return s
}

func (c *client) CreateSwap(ctx context.Context, req provider.SwapRequest) (provider.Swap, error) {
	from, okFrom := coins[req.From]
	to, okTo := coins[req.To]
	if !okFrom || !okTo || req.From == req.To {
		return provider.Swap{}, fmt.Errorf("wizardswap: pair %s/%s not supported", req.From, req.To)
	}
	body := map[string]any{
		"currency_from": from,
		"currency_to":   to,
		"amount_from":   req.Amount,
		"address_to":    req.DestinationAddress,
	}
	if req.RefundAddress != "" {
		body["refund_address"] = req.RefundAddress
	}
	if k := c.referralKey(); k != "" {
		body["api_key"] = k
	}
	raw, err := c.post(ctx, "/api/exchange", body)
	if err != nil {
		return provider.Swap{}, err
	}
	x, err := decodeExchange(raw)
	if err != nil {
		return provider.Swap{}, err
	}
	s := x.toSwap()
	s.From = req.From
	s.To = req.To
	return s, nil
}

func (c *client) Status(ctx context.Context, id string) (provider.Swap, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/exchange/"+id, nil)
	if err != nil {
		return provider.Swap{}, err
	}
	raw, err := c.do(httpReq)
	if err != nil {
		return provider.Swap{}, err
	}
	x, err := decodeExchange(raw)
	if err != nil {
		return provider.Swap{}, err
	}
	return x.toSwap(), nil
}

func decodeExchange(raw []byte) (apiExchange, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return apiExchange{}, fmt.Errorf("wizardswap: empty response")
	}
	if trimmed[0] == '"' {
		var msg string
		if json.Unmarshal(trimmed, &msg) == nil && msg != "" {
			return apiExchange{}, fmt.Errorf("wizardswap: %s", msg)
		}
		return apiExchange{}, fmt.Errorf("wizardswap: unexpected response")
	}
	var x apiExchange
	if err := json.Unmarshal(trimmed, &x); err != nil {
		return apiExchange{}, fmt.Errorf("wizardswap: %s", err)
	}
	if x.ID == "" {
		return apiExchange{}, fmt.Errorf("wizardswap: exchange not created")
	}
	return x, nil
}

func (c *client) post(ctx context.Context, path string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

func (c *client) do(req *http.Request) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wizardswap: %s", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("wizardswap: %s", err)
	}
	if resp.StatusCode >= 300 {
		var msg string
		if json.Unmarshal(bytes.TrimSpace(body), &msg) == nil && msg != "" {
			return nil, fmt.Errorf("wizardswap: %s", msg)
		}
		return nil, fmt.Errorf("wizardswap: %s", resp.Status)
	}
	return body, nil
}

func mapStatus(s string) string {
	if m, ok := statusMap[s]; ok {
		return m
	}
	return s
}

func canonical(code string) string {
	for sym, cn := range coins {
		if cn == code {
			return sym
		}
	}
	return strings.ToUpper(code)
}

func rawText(m json.RawMessage) string {
	trimmed := bytes.TrimSpace(m)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if json.Unmarshal(trimmed, &s) == nil {
			return s
		}
	}
	return string(trimmed)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func trimZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}
