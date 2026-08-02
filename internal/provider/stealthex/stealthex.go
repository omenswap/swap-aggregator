package stealthex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"omenswap.com/swap-aggregator/internal/config"
	"omenswap.com/swap-aggregator/internal/provider"
)

func init() {
	provider.Register("stealthex", New)
}

type currency struct {
	Symbol  string `json:"symbol"`
	Network string `json:"network"`
}

var currencies = map[string]currency{
	"BTC":      {Symbol: "btc", Network: "mainnet"},
	"ETH":      {Symbol: "eth", Network: "mainnet"},
	"SOL":      {Symbol: "sol", Network: "mainnet"},
	"XMR":      {Symbol: "xmr", Network: "mainnet"},
	"LTC":      {Symbol: "ltc", Network: "mainnet"},
	"DOGE":     {Symbol: "doge", Network: "mainnet"},
	"USDC":     {Symbol: "usdc", Network: "eth"},
	"USDC_POL": {Symbol: "usdc", Network: "matic"},
	"USDT":     {Symbol: "usdt", Network: "eth"},
	"USDT_POL": {Symbol: "usdt", Network: "matic"},
	"USDT_TRX": {Symbol: "usdt", Network: "trx"},
}

var canonicalOrder = []string{"BTC", "ETH", "SOL", "XMR", "LTC", "DOGE", "USDC", "USDC_POL", "USDT", "USDT_POL", "USDT_TRX"}

var statusMap = map[string]string{
	"waiting":    "pending",
	"confirming": "awaiting_confirmation",
	"exchanging": "deposited",
	"sending":    "deposited",
	"finished":   "completed",
	"expired":    "expired",
	"refunded":   "refunded",
	"failed":     "failed",
}

type client struct {
	name      string
	baseURL   string
	apiKey    string
	affiliate string
	http      *http.Client
}

func New(cfg config.Provider) (provider.Provider, error) {
	return &client{
		name:      cfg.Name,
		baseURL:   strings.TrimRight(cfg.URL, "/"),
		apiKey:    cfg.APIKey,
		affiliate: cfg.AffiliateCode,
		http:      &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (c *client) Name() string { return c.name }

func (c *client) WithAPIKey(apiKey string) (provider.Provider, error) {
	clone := *c
	clone.apiKey = strings.TrimSpace(apiKey)
	if clone.apiKey == "" {
		return nil, fmt.Errorf("stealthex: api key is required")
	}
	return &clone, nil
}

func (c *client) Brokered() bool { return c.apiKey != "" }

func (c *client) APIKeyRequiredForQuote() bool { return true }
func (c *client) APIKeyRequiredForSwap() bool  { return true }

func (c *client) SwapLink(req provider.QuoteRequest) string {
	v := url.Values{}
	if f, ok := currencies[req.From]; ok {
		v.Set("from", f.Symbol)
	}
	if t, ok := currencies[req.To]; ok {
		v.Set("to", t.Symbol)
	}
	if req.Amount != "" {
		v.Set("amount", req.Amount)
	}
	if c.affiliate != "" {
		v.Set("ref", c.affiliate)
	}
	return "https://stealthex.io/?" + v.Encode()
}

func (c *client) Pairs(ctx context.Context) ([]provider.Pair, error) {
	var pairs []provider.Pair
	for _, from := range canonicalOrder {
		for _, to := range canonicalOrder {
			if from == to {
				continue
			}
			pairs = append(pairs, provider.Pair{From: from, To: to})
		}
	}
	return pairs, nil
}

func (c *client) route(from, to string) (map[string]any, bool) {
	f, ok := currencies[from]
	if !ok {
		return nil, false
	}
	t, ok := currencies[to]
	if !ok {
		return nil, false
	}
	return map[string]any{"from": f, "to": t}, true
}

func (c *client) rateBody(route map[string]any) map[string]any {
	body := map[string]any{
		"route":      route,
		"estimation": "direct",
		"rate":       "floating",
	}
	if c.affiliate != "" {
		body["additional_fee_percent"] = json.Number(c.affiliate)
	}
	return body
}

func (c *client) Quote(ctx context.Context, req provider.QuoteRequest) (provider.Quote, error) {
	q := provider.Quote{Provider: c.name, Pair: provider.Pair{From: req.From, To: req.To}}
	route, ok := c.route(req.From, req.To)
	if !ok {
		q.Err = "pair not supported"
		return q, nil
	}
	amount, ok := new(big.Rat).SetString(req.Amount)
	if !ok || amount.Sign() <= 0 {
		q.Err = "invalid amount"
		return q, nil
	}

	var rng struct {
		MinAmount json.RawMessage `json:"min_amount"`
		MaxAmount json.RawMessage `json:"max_amount"`
	}
	if err := c.post(ctx, "/v4/rates/range", c.rateBody(route), &rng); err != nil {
		return provider.Quote{}, err
	}
	q.Pair.MinFrom = numString(rng.MinAmount)
	q.Pair.MaxFrom = numString(rng.MaxAmount)
	if min, ok := new(big.Rat).SetString(q.Pair.MinFrom); ok && amount.Cmp(min) < 0 {
		q.Err = fmt.Sprintf("below minimum of %s %s", q.Pair.MinFrom, req.From)
		return q, nil
	}
	if max, ok := new(big.Rat).SetString(q.Pair.MaxFrom); ok && amount.Cmp(max) > 0 {
		q.Err = fmt.Sprintf("above maximum of %s %s", q.Pair.MaxFrom, req.From)
		return q, nil
	}

	body := c.rateBody(route)
	body["amount"] = json.Number(req.Amount)
	var est struct {
		EstimatedAmount json.RawMessage `json:"estimated_amount"`
	}
	if err := c.post(ctx, "/v4/rates/estimated-amount", body, &est); err != nil {
		return provider.Quote{}, err
	}
	q.ToAmount = numString(est.EstimatedAmount)
	if to, ok := new(big.Rat).SetString(q.ToAmount); ok {
		q.Pair.Rate = trimZeros(new(big.Rat).Quo(to, amount).FloatString(8))
	}
	return q, nil
}

type apiExchange struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	RefundAddress string `json:"refund_address"`
	Deposit       struct {
		Symbol  string          `json:"symbol"`
		Network string          `json:"network"`
		Amount  json.RawMessage `json:"amount"`
		Address string          `json:"address"`
		TxHash  string          `json:"tx_hash"`
	} `json:"deposit"`
	Withdrawal struct {
		Symbol  string          `json:"symbol"`
		Network string          `json:"network"`
		Amount  json.RawMessage `json:"amount"`
		Address string          `json:"address"`
		TxHash  string          `json:"tx_hash"`
	} `json:"withdrawal"`
}

func (e apiExchange) toSwap() provider.Swap {
	return provider.Swap{
		ID:                 e.ID,
		Status:             mapStatus(e.Status),
		From:               canonical(e.Deposit.Symbol, e.Deposit.Network),
		To:                 canonical(e.Withdrawal.Symbol, e.Withdrawal.Network),
		FromAmount:         numString(e.Deposit.Amount),
		ToAmountEstimated:  numString(e.Withdrawal.Amount),
		DepositAddress:     e.Deposit.Address,
		DestinationAddress: e.Withdrawal.Address,
		DepositTxHash:      e.Deposit.TxHash,
		PayoutTxHash:       e.Withdrawal.TxHash,
		CreatedAt:          e.CreatedAt,
	}
}

func (c *client) CreateSwap(ctx context.Context, req provider.SwapRequest) (provider.Swap, error) {
	route, ok := c.route(req.From, req.To)
	if !ok {
		return provider.Swap{}, fmt.Errorf("stealthex: pair %s/%s not supported", req.From, req.To)
	}
	body := c.rateBody(route)
	body["amount"] = json.Number(req.Amount)
	body["address"] = req.DestinationAddress
	if req.RefundAddress != "" {
		body["refund_address"] = req.RefundAddress
	}
	var e apiExchange
	if err := c.post(ctx, "/v4/exchanges", body, &e); err != nil {
		return provider.Swap{}, err
	}
	return e.toSwap(), nil
}

func (c *client) Status(ctx context.Context, id string) (provider.Swap, error) {
	var e apiExchange
	if err := c.get(ctx, "/v4/exchanges/"+id, &e); err != nil {
		return provider.Swap{}, err
	}
	return e.toSwap(), nil
}

func (c *client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *client) post(ctx context.Context, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *client) do(req *http.Request, out any) error {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("stealthex: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var apiErr struct {
			Err struct {
				Kind    string `json:"kind"`
				Details string `json:"details"`
			} `json:"err"`
		}
		if json.NewDecoder(resp.Body).Decode(&apiErr) == nil && apiErr.Err.Details != "" {
			return fmt.Errorf("stealthex: %s", apiErr.Err.Details)
		}
		return fmt.Errorf("stealthex: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func mapStatus(s string) string {
	if m, ok := statusMap[s]; ok {
		return m
	}
	return s
}

func canonical(symbol, network string) string {
	for _, name := range canonicalOrder {
		cur := currencies[name]
		if cur.Symbol == symbol && cur.Network == network {
			return name
		}
	}
	return strings.ToUpper(symbol)
}

func numString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return ""
		}
		s = v
	}
	if r, ok := new(big.Rat).SetString(s); ok {
		return trimZeros(r.FloatString(12))
	}
	return s
}

func trimZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}
