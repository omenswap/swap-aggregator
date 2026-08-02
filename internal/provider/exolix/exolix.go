package exolix

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
	provider.Register("exolix", New)
}

type coin struct {
	code    string
	network string
}

var symbols = []string{"BTC", "DOGE", "ETH", "LTC", "SOL", "USDC", "USDC_POL", "USDT", "USDT_TRX", "XMR"}

var coins = map[string]coin{
	"BTC":      {"BTC", "BTC"},
	"DOGE":     {"DOGE", "DOGE"},
	"ETH":      {"ETH", "ETH"},
	"LTC":      {"LTC", "LTC"},
	"SOL":      {"SOL", "SOL"},
	"USDC":     {"USDC", "ETH"},
	"USDC_POL": {"USDC", "MATIC"},
	"USDT":     {"USDT", "ETH"},
	"USDT_TRX": {"USDT", "TRX"},
	"XMR":      {"XMR", "XMR"},
}

var statusMap = map[string]string{
	"wait":         "pending",
	"confirmation": "awaiting_confirmation",
	"confirmed":    "deposited",
	"exchanging":   "deposited",
	"sending":      "deposited",
	"success":      "completed",
	"overdue":      "expired",
	"refund":       "refunded",
	"refunded":     "refunded",
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
	if f, ok := coins[req.From]; ok {
		v.Set("from", f.code)
	}
	if t, ok := coins[req.To]; ok {
		v.Set("to", t.code)
	}
	if req.Amount != "" {
		v.Set("amount", req.Amount)
	}
	if c.affiliate != "" {
		v.Set("ref", c.affiliate)
	}
	return "https://exolix.com/?" + v.Encode()
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

type apiRate struct {
	FromAmount json.Number `json:"fromAmount"`
	ToAmount   json.Number `json:"toAmount"`
	Rate       json.Number `json:"rate"`
	MinAmount  json.Number `json:"minAmount"`
	MaxAmount  json.Number `json:"maxAmount"`
	Message    string      `json:"message"`
	Error      string      `json:"error"`
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

	v := url.Values{}
	v.Set("coinFrom", from.code)
	v.Set("networkFrom", from.network)
	v.Set("coinTo", to.code)
	v.Set("networkTo", to.network)
	v.Set("amount", req.Amount)
	v.Set("rateType", "float")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v2/rate?"+v.Encode(), nil)
	if err != nil {
		return provider.Quote{}, err
	}
	c.auth(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return provider.Quote{}, fmt.Errorf("exolix: %s", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return provider.Quote{}, fmt.Errorf("exolix: %s", err)
	}

	var r apiRate
	if resp.StatusCode < 300 {
		if err := json.Unmarshal(body, &r); err != nil {
			return provider.Quote{}, fmt.Errorf("exolix: %s", err)
		}
		q.Pair.MinFrom = r.MinAmount.String()
		q.Pair.MaxFrom = r.MaxAmount.String()
		toAmount, ok := new(big.Rat).SetString(r.ToAmount.String())
		if !ok || toAmount.Sign() <= 0 {
			q.Err = "pair currently unavailable"
			return q, nil
		}
		q.ToAmount = trimZeros(toAmount.FloatString(12))
		q.Pair.Rate = trimZeros(new(big.Rat).Quo(toAmount, amount).FloatString(12))
		return q, nil
	}

	if json.Unmarshal(body, &r) == nil {
		if r.Message != "" {
			q.Pair.MinFrom = r.MinAmount.String()
			q.Pair.MaxFrom = r.MaxAmount.String()
			switch {
			case strings.Contains(r.Message, "min amount"):
				q.Err = fmt.Sprintf("below minimum of %s %s", r.MinAmount.String(), req.From)
				return q, nil
			case strings.Contains(r.Message, "max amount"):
				q.Err = fmt.Sprintf("above maximum of %s %s", r.MaxAmount.String(), req.From)
				return q, nil
			}
		}
		if strings.Contains(r.Error, "pair is not available") {
			q.Err = "pair not supported"
			return q, nil
		}
		if msg := firstNonEmpty(r.Error, r.Message); msg != "" {
			return provider.Quote{}, fmt.Errorf("exolix: %s", msg)
		}
	}
	return provider.Quote{}, fmt.Errorf("exolix: %s", resp.Status)
}

type apiCoin struct {
	CoinCode string `json:"coinCode"`
	Network  string `json:"network"`
}

type apiHash struct {
	Hash string `json:"hash"`
}

type apiTx struct {
	ID                string      `json:"id"`
	Status            string      `json:"status"`
	Amount            json.Number `json:"amount"`
	AmountTo          json.Number `json:"amountTo"`
	CoinFrom          apiCoin     `json:"coinFrom"`
	CoinTo            apiCoin     `json:"coinTo"`
	DepositAddress    string      `json:"depositAddress"`
	WithdrawalAddress string      `json:"withdrawalAddress"`
	RefundAddress     string      `json:"refundAddress"`
	HashIn            apiHash     `json:"hashIn"`
	HashOut           apiHash     `json:"hashOut"`
	CreatedAt         string      `json:"createdAt"`
}

func (t apiTx) toSwap() provider.Swap {
	return provider.Swap{
		ID:                 t.ID,
		Status:             mapStatus(t.Status),
		From:               canonical(t.CoinFrom),
		To:                 canonical(t.CoinTo),
		FromAmount:         t.Amount.String(),
		ToAmountEstimated:  t.AmountTo.String(),
		DepositAddress:     t.DepositAddress,
		DestinationAddress: t.WithdrawalAddress,
		DepositTxHash:      t.HashIn.Hash,
		PayoutTxHash:       t.HashOut.Hash,
		CreatedAt:          t.CreatedAt,
	}
}

func (c *client) CreateSwap(ctx context.Context, req provider.SwapRequest) (provider.Swap, error) {
	from, okFrom := coins[req.From]
	to, okTo := coins[req.To]
	if !okFrom || !okTo || req.From == req.To {
		return provider.Swap{}, fmt.Errorf("exolix: pair %s/%s not supported", req.From, req.To)
	}
	body := map[string]any{
		"coinFrom":          from.code,
		"networkFrom":       from.network,
		"coinTo":            to.code,
		"networkTo":         to.network,
		"amount":            json.Number(req.Amount),
		"withdrawalAddress": req.DestinationAddress,
		"rateType":          "float",
	}
	if req.RefundAddress != "" {
		body["refundAddress"] = req.RefundAddress
	}
	var t apiTx
	if err := c.post(ctx, "/api/v2/transactions", body, &t); err != nil {
		return provider.Swap{}, err
	}
	s := t.toSwap()
	s.From = req.From
	s.To = req.To
	return s, nil
}

func (c *client) Status(ctx context.Context, id string) (provider.Swap, error) {
	var t apiTx
	if err := c.get(ctx, "/api/v2/transactions/"+id, &t); err != nil {
		return provider.Swap{}, err
	}
	return t.toSwap(), nil
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
		return fmt.Errorf("exolix: %s", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var apiErr struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.NewDecoder(resp.Body).Decode(&apiErr) == nil {
			if msg := firstNonEmpty(apiErr.Error, apiErr.Message); msg != "" {
				return fmt.Errorf("exolix: %s", msg)
			}
		}
		return fmt.Errorf("exolix: %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("exolix: %s", err)
	}
	return nil
}

func (c *client) auth(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("api-key", c.apiKey)
	}
}

func mapStatus(s string) string {
	if m, ok := statusMap[s]; ok {
		return m
	}
	return s
}

func canonical(ac apiCoin) string {
	for sym, cn := range coins {
		if cn.code == ac.CoinCode && cn.network == ac.Network {
			return sym
		}
	}
	return ac.CoinCode
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
