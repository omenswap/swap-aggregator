package simpleswap

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
	provider.Register("simpleswap", New)
}

type currency struct {
	Ticker  string
	Network string
}

var currencies = map[string]currency{
	"BTC":      {"btc", "btc"},
	"ETH":      {"eth", "eth"},
	"SOL":      {"sol", "sol"},
	"XMR":      {"xmr", "xmr"},
	"LTC":      {"ltc", "ltc"},
	"DOGE":     {"doge", "doge"},
	"USDC":     {"usdc", "eth"},
	"USDC_POL": {"usdc", "matic"},
	"USDT":     {"usdt", "eth"},
	"USDT_POL": {"usdt", "matic"},
	"USDT_TRX": {"usdt", "trx"},
}

var symbols = []string{"BTC", "ETH", "SOL", "XMR", "LTC", "DOGE", "USDC", "USDC_POL", "USDT", "USDT_POL", "USDT_TRX"}

var statuses = map[string]string{
	"waiting":    "pending",
	"confirming": "awaiting_confirmation",
	"verifying":  "deposited",
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

func (c *client) Brokered() bool { return c.apiKey != "" }

func (c *client) APIKeyRequiredForQuote() bool { return true }
func (c *client) APIKeyRequiredForSwap() bool  { return true }

func (c *client) SwapLink(req provider.QuoteRequest) string {
	v := url.Values{}
	if f, ok := currencies[req.From]; ok {
		v.Set("from", f.Ticker)
	}
	if t, ok := currencies[req.To]; ok {
		v.Set("to", t.Ticker)
	}
	if req.Amount != "" {
		v.Set("amount", req.Amount)
	}
	if c.affiliate != "" {
		v.Set("ref", c.affiliate)
	}
	return "https://simpleswap.io/?" + v.Encode()
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
		return nil, fmt.Errorf("simpleswap: api key is required")
	}
	return &clone, nil
}

func (c *client) Pairs(ctx context.Context) ([]provider.Pair, error) {
	var pairs []provider.Pair
	for _, from := range symbols {
		for _, to := range symbols {
			if from == to {
				continue
			}
			pairs = append(pairs, provider.Pair{From: from, To: to, Symbol: from + to})
		}
	}
	return pairs, nil
}

func pairQuery(from, to currency) url.Values {
	return url.Values{
		"tickerFrom":  {from.Ticker},
		"networkFrom": {from.Network},
		"tickerTo":    {to.Ticker},
		"networkTo":   {to.Network},
		"fixed":       {"false"},
		"reverse":     {"false"},
	}
}

func (c *client) Quote(ctx context.Context, req provider.QuoteRequest) (provider.Quote, error) {
	q := provider.Quote{Provider: c.name, Pair: provider.Pair{From: req.From, To: req.To, Symbol: req.From + req.To}}
	from, okFrom := currencies[req.From]
	to, okTo := currencies[req.To]
	if !okFrom || !okTo || req.From == req.To {
		q.Err = "pair not supported"
		return q, nil
	}
	amount, ok := new(big.Rat).SetString(req.Amount)
	if !ok || amount.Sign() <= 0 {
		q.Err = "invalid amount"
		return q, nil
	}

	var ranges struct {
		Result struct {
			Min json.Number `json:"min"`
			Max json.Number `json:"max"`
		} `json:"result"`
	}
	if err := c.get(ctx, "/v3/ranges", pairQuery(from, to), &ranges); err != nil {
		return provider.Quote{}, err
	}
	q.Pair.MinFrom = ranges.Result.Min.String()
	q.Pair.MaxFrom = ranges.Result.Max.String()
	if min, ok := new(big.Rat).SetString(q.Pair.MinFrom); ok && amount.Cmp(min) < 0 {
		q.Err = fmt.Sprintf("below minimum of %s %s", q.Pair.MinFrom, req.From)
		return q, nil
	}
	if max, ok := new(big.Rat).SetString(q.Pair.MaxFrom); ok && amount.Cmp(max) > 0 {
		q.Err = fmt.Sprintf("above maximum of %s %s", q.Pair.MaxFrom, req.From)
		return q, nil
	}

	query := pairQuery(from, to)
	query.Set("amount", req.Amount)
	var est struct {
		Result struct {
			EstimatedAmount json.Number `json:"estimatedAmount"`
		} `json:"result"`
	}
	if err := c.get(ctx, "/v3/estimates", query, &est); err != nil {
		return provider.Quote{}, err
	}
	q.ToAmount = est.Result.EstimatedAmount.String()
	if toAmount, ok := new(big.Rat).SetString(q.ToAmount); ok {
		q.Pair.Rate = trimZeros(new(big.Rat).Quo(toAmount, amount).FloatString(8))
	}
	return q, nil
}

type apiExchange struct {
	Result struct {
		PublicID          string      `json:"publicId"`
		Status            string      `json:"status"`
		TickerFrom        string      `json:"tickerFrom"`
		TickerTo          string      `json:"tickerTo"`
		NetworkFrom       string      `json:"networkFrom"`
		NetworkTo         string      `json:"networkTo"`
		AmountFrom        json.Number `json:"amountFrom"`
		AmountTo          json.Number `json:"amountTo"`
		AddressFrom       string      `json:"addressFrom"`
		AddressTo         string      `json:"addressTo"`
		UserRefundAddress string      `json:"userRefundAddress"`
		TxFrom            string      `json:"txFrom"`
		TxTo              string      `json:"txTo"`
		CreatedAt         string      `json:"createdAt"`
		ValidUntil        string      `json:"validUntil"`
	} `json:"result"`
}

func (e apiExchange) toSwap() provider.Swap {
	r := e.Result
	return provider.Swap{
		ID:                 r.PublicID,
		Status:             mapStatus(r.Status),
		From:               canonical(r.TickerFrom, r.NetworkFrom),
		To:                 canonical(r.TickerTo, r.NetworkTo),
		FromAmount:         r.AmountFrom.String(),
		ToAmountEstimated:  r.AmountTo.String(),
		DepositAddress:     r.AddressFrom,
		DestinationAddress: r.AddressTo,
		DepositTxHash:      r.TxFrom,
		PayoutTxHash:       r.TxTo,
		ExpiresAt:          r.ValidUntil,
		CreatedAt:          r.CreatedAt,
	}
}

func (c *client) CreateSwap(ctx context.Context, req provider.SwapRequest) (provider.Swap, error) {
	from, okFrom := currencies[req.From]
	to, okTo := currencies[req.To]
	if !okFrom || !okTo || req.From == req.To {
		return provider.Swap{}, fmt.Errorf("simpleswap: pair %s/%s not supported", req.From, req.To)
	}
	body := map[string]any{
		"tickerFrom":  from.Ticker,
		"networkFrom": from.Network,
		"tickerTo":    to.Ticker,
		"networkTo":   to.Network,
		"amount":      req.Amount,
		"addressTo":   req.DestinationAddress,
		"fixed":       false,
		"reverse":     false,
	}
	if req.RefundAddress != "" {
		body["userRefundAddress"] = req.RefundAddress
	}
	var e apiExchange
	if err := c.post(ctx, "/v3/exchanges", body, &e); err != nil {
		return provider.Swap{}, err
	}
	return e.toSwap(), nil
}

func (c *client) Status(ctx context.Context, id string) (provider.Swap, error) {
	var e apiExchange
	if err := c.get(ctx, "/v3/exchanges/"+url.PathEscape(id), nil, &e); err != nil {
		return provider.Swap{}, err
	}
	return e.toSwap(), nil
}

func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
	req.Header.Set("x-api-key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("simpleswap: %s", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var apiErr struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.NewDecoder(resp.Body).Decode(&apiErr) == nil {
			if apiErr.Message != "" {
				return fmt.Errorf("simpleswap: %s", apiErr.Message)
			}
			if apiErr.Error != "" {
				return fmt.Errorf("simpleswap: %s", apiErr.Error)
			}
		}
		return fmt.Errorf("simpleswap: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func mapStatus(s string) string {
	if mapped, ok := statuses[s]; ok {
		return mapped
	}
	return s
}

func canonical(ticker, network string) string {
	for sym, cur := range currencies {
		if cur.Ticker == ticker && cur.Network == network {
			return sym
		}
	}
	return strings.ToUpper(ticker)
}

func trimZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}
