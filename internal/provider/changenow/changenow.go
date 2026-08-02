package changenow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"omenswap.com/swap-aggregator/internal/config"
	"omenswap.com/swap-aggregator/internal/provider"
)

func init() {
	provider.Register("changenow", New)
}

type token struct {
	ticker  string
	network string
}

var tokens = map[string]token{
	"BTC":      {"btc", "btc"},
	"ETH":      {"eth", "eth"},
	"SOL":      {"sol", "sol"},
	"XMR":      {"xmr", "xmr"},
	"LTC":      {"ltc", "ltc"},
	"DOGE":     {"doge", "doge"},
	"USDC":     {"usdc", "eth"},
	"USDC_POL": {"usdcmatic", "matic"},
	"USDT":     {"usdterc20", "eth"},
	"USDT_POL": {"usdtmatic", "matic"},
	"USDT_TRX": {"usdttrc20", "trx"},
}

type client struct {
	name      string
	baseURL   string
	apiKey    string
	affiliate string
	http      *http.Client
}

func (c *client) Brokered() bool { return c.apiKey != "" }

func (c *client) SwapLink(req provider.QuoteRequest) string {
	v := url.Values{}
	if f, ok := tokens[req.From]; ok {
		v.Set("from", f.ticker)
	}
	if t, ok := tokens[req.To]; ok {
		v.Set("to", t.ticker)
	}
	if req.Amount != "" {
		v.Set("amount", req.Amount)
	}
	if c.affiliate != "" {
		v.Set("link_id", c.affiliate)
	}
	return "https://changenow.io/exchange?" + v.Encode()
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

func (c *client) Pairs(ctx context.Context) ([]provider.Pair, error) {
	symbols := make([]string, 0, len(tokens))
	for s := range tokens {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)
	var pairs []provider.Pair
	for _, from := range symbols {
		for _, to := range symbols {
			if from == to {
				continue
			}
			pairs = append(pairs, provider.Pair{
				From:   from,
				To:     to,
				Symbol: from + to,
			})
		}
	}
	return pairs, nil
}

func pairQuery(from, to token) url.Values {
	return url.Values{
		"fromCurrency": {from.ticker},
		"fromNetwork":  {from.network},
		"toCurrency":   {to.ticker},
		"toNetwork":    {to.network},
		"flow":         {"standard"},
	}
}

func (c *client) Quote(ctx context.Context, req provider.QuoteRequest) (provider.Quote, error) {
	q := provider.Quote{Provider: c.name}
	from, okFrom := tokens[req.From]
	to, okTo := tokens[req.To]
	if !okFrom || !okTo || req.From == req.To {
		q.Err = "pair not supported"
		return q, nil
	}
	q.Pair = provider.Pair{From: req.From, To: req.To, Symbol: req.From + req.To}

	amount, ok := new(big.Rat).SetString(req.Amount)
	if !ok || amount.Sign() <= 0 {
		q.Err = "invalid amount"
		return q, nil
	}

	var rng struct {
		MinAmount json.RawMessage `json:"minAmount"`
		MaxAmount json.RawMessage `json:"maxAmount"`
	}
	if err := c.get(ctx, "/v2/exchange/range", pairQuery(from, to), &rng); err != nil {
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

	query := pairQuery(from, to)
	query.Set("fromAmount", req.Amount)
	var est struct {
		FromAmount json.RawMessage `json:"fromAmount"`
		ToAmount   json.RawMessage `json:"toAmount"`
	}
	if err := c.get(ctx, "/v2/exchange/estimated-amount", query, &est); err != nil {
		return provider.Quote{}, err
	}
	toAmount := numString(est.ToAmount)
	if toAmount == "" {
		return provider.Quote{}, fmt.Errorf("%s: missing toAmount in estimate", c.name)
	}
	q.ToAmount = trimZeros(toAmount)
	if toRat, ok := new(big.Rat).SetString(toAmount); ok {
		q.Pair.Rate = trimZeros(new(big.Rat).Quo(toRat, amount).FloatString(8))
	}
	return q, nil
}

func (c *client) CreateSwap(ctx context.Context, req provider.SwapRequest) (provider.Swap, error) {
	from, okFrom := tokens[req.From]
	to, okTo := tokens[req.To]
	if !okFrom || !okTo || req.From == req.To {
		return provider.Swap{}, fmt.Errorf("%s: pair %s/%s not supported", c.name, req.From, req.To)
	}

	body := map[string]string{
		"fromCurrency": from.ticker,
		"fromNetwork":  from.network,
		"toCurrency":   to.ticker,
		"toNetwork":    to.network,
		"fromAmount":   req.Amount,
		"address":      req.DestinationAddress,
		"flow":         "standard",
	}
	if req.RefundAddress != "" {
		body["refundAddress"] = req.RefundAddress
	}

	var resp struct {
		ID            string          `json:"id"`
		FromAmount    json.RawMessage `json:"fromAmount"`
		ToAmount      json.RawMessage `json:"toAmount"`
		PayinAddress  string          `json:"payinAddress"`
		PayoutAddress string          `json:"payoutAddress"`
	}
	if err := c.post(ctx, "/v2/exchange", body, &resp); err != nil {
		return provider.Swap{}, err
	}
	return provider.Swap{
		ID:                 resp.ID,
		Status:             "pending",
		From:               req.From,
		To:                 req.To,
		FromAmount:         numString(resp.FromAmount),
		ToAmountEstimated:  numString(resp.ToAmount),
		DepositAddress:     resp.PayinAddress,
		DestinationAddress: resp.PayoutAddress,
	}, nil
}

func (c *client) Status(ctx context.Context, id string) (provider.Swap, error) {
	var resp struct {
		ID                 string          `json:"id"`
		Status             string          `json:"status"`
		FromCurrency       string          `json:"fromCurrency"`
		FromNetwork        string          `json:"fromNetwork"`
		ToCurrency         string          `json:"toCurrency"`
		ToNetwork          string          `json:"toNetwork"`
		ExpectedAmountFrom json.RawMessage `json:"expectedAmountFrom"`
		ExpectedAmountTo   json.RawMessage `json:"expectedAmountTo"`
		AmountFrom         json.RawMessage `json:"amountFrom"`
		AmountTo           json.RawMessage `json:"amountTo"`
		PayinAddress       string          `json:"payinAddress"`
		PayoutAddress      string          `json:"payoutAddress"`
		PayinHash          string          `json:"payinHash"`
		PayoutHash         string          `json:"payoutHash"`
		CreatedAt          string          `json:"createdAt"`
		ValidUntil         string          `json:"validUntil"`
	}
	if err := c.get(ctx, "/v2/exchange/by-id", url.Values{"id": {id}}, &resp); err != nil {
		return provider.Swap{}, err
	}
	fromAmount := numString(resp.AmountFrom)
	if fromAmount == "" {
		fromAmount = numString(resp.ExpectedAmountFrom)
	}
	return provider.Swap{
		ID:                 resp.ID,
		Status:             mapStatus(resp.Status),
		From:               canonical(resp.FromCurrency, resp.FromNetwork),
		To:                 canonical(resp.ToCurrency, resp.ToNetwork),
		FromAmount:         fromAmount,
		ToAmountEstimated:  numString(resp.ExpectedAmountTo),
		ToAmountActual:     numString(resp.AmountTo),
		DepositAddress:     resp.PayinAddress,
		DestinationAddress: resp.PayoutAddress,
		DepositTxHash:      resp.PayinHash,
		PayoutTxHash:       resp.PayoutHash,
		ExpiresAt:          resp.ValidUntil,
		CreatedAt:          resp.CreatedAt,
	}, nil
}

func mapStatus(s string) string {
	switch s {
	case "new", "waiting":
		return "pending"
	case "confirming", "verifying":
		return "awaiting_confirmation"
	case "exchanging", "sending":
		return "deposited"
	case "finished":
		return "completed"
	case "expired":
		return "expired"
	case "refunded":
		return "refunded"
	case "failed":
		return "failed"
	}
	return s
}

func canonical(ticker, network string) string {
	for sym, t := range tokens {
		if t.ticker == ticker && t.network == network {
			return sym
		}
	}
	return strings.ToUpper(ticker)
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
	req.Header.Set("x-changenow-api-key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var apiErr struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if json.NewDecoder(resp.Body).Decode(&apiErr) == nil {
			if apiErr.Message != "" {
				return fmt.Errorf("%s: %s", c.name, apiErr.Message)
			}
			if apiErr.Error != "" {
				return fmt.Errorf("%s: %s", c.name, apiErr.Error)
			}
		}
		return fmt.Errorf("%s: %s", c.name, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func numString(r json.RawMessage) string {
	if len(r) == 0 || string(r) == "null" {
		return ""
	}
	var n json.Number
	if err := json.Unmarshal(r, &n); err == nil {
		return trimZeros(n.String())
	}
	var s string
	if err := json.Unmarshal(r, &s); err == nil {
		return trimZeros(s)
	}
	return ""
}

func trimZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}
