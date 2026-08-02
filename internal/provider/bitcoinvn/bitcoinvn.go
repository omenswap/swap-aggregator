package bitcoinvn

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
	provider.Register("bitcoinvn", New)
}

var symbols = []string{"BTC", "DOGE", "ETH", "LTC", "SOL", "USDC", "USDC_POL", "USDT", "USDT_POL", "USDT_TRX", "XMR"}

var methods = map[string]string{
	"BTC":      "btc",
	"DOGE":     "doge",
	"ETH":      "eth",
	"LTC":      "ltc",
	"SOL":      "sol",
	"USDC":     "usdc",
	"USDC_POL": "usdcpolygon2",
	"USDT":     "usdterc20",
	"USDT_POL": "usdtpolygon",
	"USDT_TRX": "usdttrc20",
	"XMR":      "xmr",
}

var statusMap = map[string]string{
	"new":               "pending",
	"pending":           "awaiting_confirmation",
	"processing":        "deposited",
	"on_hold":           "deposited",
	"settle_data_error": "failed",
	"completed":         "completed",
	"canceled":          "expired",
}

type client struct {
	name      string
	baseURL   string
	apiKey    string
	affiliate string
	http      *http.Client
}

func (c *client) Brokered() bool { return true }

func (c *client) SwapLink(req provider.QuoteRequest) string {
	v := url.Values{}
	if f, ok := methods[req.From]; ok {
		v.Set("deposit", f)
	}
	if t, ok := methods[req.To]; ok {
		v.Set("settle", t)
	}
	if req.Amount != "" {
		v.Set("depositAmount", req.Amount)
	}
	if c.affiliate != "" {
		v.Set("ref", c.affiliate)
	}
	return "https://bitcoinvn.io/?" + v.Encode()
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

type apiQuote struct {
	ID            string      `json:"id"`
	DepositAmount json.Number `json:"depositAmount"`
	DepositMethod string      `json:"depositMethod"`
	SettleAmount  json.Number `json:"settleAmount"`
	SettleMethod  string      `json:"settleMethod"`
	Rate          json.Number `json:"rate"`
	ExpiresAt     string      `json:"expiresAt"`
}

type apiError struct {
	Message string `json:"message"`
	Error   struct {
		Message string `json:"message"`
	} `json:"error"`
	Errors struct {
		Children map[string]struct {
			Errors []string `json:"errors"`
		} `json:"children"`
	} `json:"errors"`
}

func (e apiError) messages() []string {
	var msgs []string
	for _, child := range e.Errors.Children {
		msgs = append(msgs, child.Errors...)
	}
	if e.Error.Message != "" {
		msgs = append(msgs, e.Error.Message)
	}
	if len(msgs) == 0 && e.Message != "" {
		msgs = append(msgs, e.Message)
	}
	return msgs
}

func (c *client) Quote(ctx context.Context, req provider.QuoteRequest) (provider.Quote, error) {
	q := provider.Quote{Provider: c.name, Pair: provider.Pair{From: req.From, To: req.To}}
	from, okFrom := methods[req.From]
	to, okTo := methods[req.To]
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
		"depositMethod": from,
		"settleMethod":  to,
		"depositAmount": json.Number(req.Amount),
	}
	b, err := json.Marshal(body)
	if err != nil {
		return provider.Quote{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/quotes", bytes.NewReader(b))
	if err != nil {
		return provider.Quote{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.auth(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return provider.Quote{}, fmt.Errorf("bitcoinvn: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 300 {
		var r apiQuote
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return provider.Quote{}, fmt.Errorf("bitcoinvn: %s", err)
		}
		toAmount, ok := new(big.Rat).SetString(r.SettleAmount.String())
		if !ok || toAmount.Sign() <= 0 {
			q.Err = "pair currently unavailable"
			return q, nil
		}
		q.ToAmount = trimZeros(toAmount.FloatString(12))
		q.Pair.Rate = trimZeros(new(big.Rat).Quo(toAmount, amount).FloatString(12))
		return q, nil
	}

	var apiErr apiError
	if json.NewDecoder(resp.Body).Decode(&apiErr) == nil {
		for _, msg := range apiErr.messages() {
			switch {
			case strings.Contains(msg, "Minimum order size"):
				q.Err = lowerFirst(msg)
				return q, nil
			case strings.Contains(msg, "Maximum order size"):
				q.Err = lowerFirst(msg)
				return q, nil
			}
		}
		if msgs := apiErr.messages(); len(msgs) > 0 {
			return provider.Quote{}, fmt.Errorf("bitcoinvn: %s", strings.Join(msgs, "; "))
		}
	}
	return provider.Quote{}, fmt.Errorf("bitcoinvn: %s", resp.Status)
}

type apiAddress struct {
	Address string `json:"address"`
}

type apiOrder struct {
	ID            string      `json:"id"`
	ShortID       string      `json:"shortId"`
	Status        string      `json:"status"`
	DepositMethod string      `json:"depositMethod"`
	DepositAmount json.Number `json:"depositAmount"`
	DepositData   apiAddress  `json:"depositData"`
	DepositTxns   []string    `json:"depositTxns"`
	SettleMethod  string      `json:"settleMethod"`
	SettleAmount  json.Number `json:"settleAmount"`
	SettleData    apiAddress  `json:"settleData"`
	SettleTxns    []string    `json:"settleTxns"`
	CreatedAt     string      `json:"createdAt"`
	ExpiresAt     string      `json:"expiresAt"`
}

func (o apiOrder) toSwap() provider.Swap {
	return provider.Swap{
		ID:                 o.ShortID,
		Status:             mapStatus(o.Status),
		From:               canonical(o.DepositMethod),
		To:                 canonical(o.SettleMethod),
		FromAmount:         decimal(o.DepositAmount),
		ToAmountEstimated:  decimal(o.SettleAmount),
		DepositAddress:     o.DepositData.Address,
		DestinationAddress: o.SettleData.Address,
		DepositTxHash:      first(o.DepositTxns),
		PayoutTxHash:       first(o.SettleTxns),
		ExpiresAt:          o.ExpiresAt,
		CreatedAt:          o.CreatedAt,
	}
}

func (c *client) CreateSwap(ctx context.Context, req provider.SwapRequest) (provider.Swap, error) {
	from, okFrom := methods[req.From]
	to, okTo := methods[req.To]
	if !okFrom || !okTo || req.From == req.To {
		return provider.Swap{}, fmt.Errorf("bitcoinvn: pair %s/%s not supported", req.From, req.To)
	}
	var quote apiQuote
	quoteBody := map[string]any{
		"depositMethod": from,
		"settleMethod":  to,
		"depositAmount": json.Number(req.Amount),
	}
	if err := c.post(ctx, "/api/quotes", quoteBody, &quote); err != nil {
		return provider.Swap{}, err
	}
	orderBody := map[string]any{
		"quote":      quote.ID,
		"settleData": map[string]string{"address": req.DestinationAddress},
	}
	if c.affiliate != "" {
		orderBody["referrer"] = c.affiliate
	}
	var o apiOrder
	if err := c.post(ctx, "/api/orders", orderBody, &o); err != nil {
		return provider.Swap{}, err
	}
	s := o.toSwap()
	s.From = req.From
	s.To = req.To
	return s, nil
}

func (c *client) Status(ctx context.Context, id string) (provider.Swap, error) {
	var o apiOrder
	if err := c.get(ctx, "/api/orders/"+id, &o); err != nil {
		return provider.Swap{}, err
	}
	return o.toSwap(), nil
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
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("bitcoinvn: %s", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var apiErr apiError
		if json.NewDecoder(resp.Body).Decode(&apiErr) == nil {
			if msgs := apiErr.messages(); len(msgs) > 0 {
				return fmt.Errorf("bitcoinvn: %s", strings.Join(msgs, "; "))
			}
		}
		return fmt.Errorf("bitcoinvn: %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("bitcoinvn: %s", err)
	}
	return nil
}

func (c *client) auth(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("X-API-KEY", c.apiKey)
	}
}

func mapStatus(s string) string {
	if m, ok := statusMap[s]; ok {
		return m
	}
	return s
}

func canonical(method string) string {
	for sym, m := range methods {
		if m == method {
			return sym
		}
	}
	return method
}

func decimal(n json.Number) string {
	r, ok := new(big.Rat).SetString(n.String())
	if !ok {
		return n.String()
	}
	return trimZeros(r.FloatString(12))
}

func first(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func trimZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}
