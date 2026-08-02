package omenswap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"omenswap.com/swap-aggregator/internal/config"
	"omenswap.com/swap-aggregator/internal/provider"
)

func init() {
	provider.Register("omenswap", New)
}

type client struct {
	name      string
	baseURL   string
	affiliate string
	http      *http.Client
}

func New(cfg config.Provider) (provider.Provider, error) {
	return &client{
		name:      cfg.Name,
		baseURL:   strings.TrimRight(cfg.URL, "/"),
		affiliate: cfg.AffiliateCode,
		http:      &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (c *client) Name() string { return c.name }

type apiPair struct {
	Symbol            string            `json:"symbol"`
	FromToken         string            `json:"from_token"`
	ToToken           string            `json:"to_token"`
	Rate              string            `json:"rate"`
	PriceUnavailable  bool              `json:"price_unavailable"`
	PayoutUnavailable bool              `json:"payout_unavailable"`
	Fees              map[string]string `json:"fees"`
	MaxFromAmount     map[string]string `json:"max_from_amount"`
	MinFromAmount     map[string]string `json:"min_from_amount"`
}

func (c *client) Pairs(ctx context.Context) ([]provider.Pair, error) {
	var resp struct {
		Pairs []apiPair `json:"pairs"`
	}
	if err := c.get(ctx, "/api/v1/pairs", &resp); err != nil {
		return nil, err
	}
	pairs := make([]provider.Pair, 0, len(resp.Pairs))
	for _, p := range resp.Pairs {
		pairs = append(pairs, provider.Pair{
			From:        p.FromToken,
			To:          p.ToToken,
			Symbol:      p.Symbol,
			Rate:        p.Rate,
			MinFrom:     p.MinFromAmount["floating"],
			MaxFrom:     p.MaxFromAmount["floating"],
			Fee:         p.Fees["floating"],
			Unavailable: p.PriceUnavailable || p.PayoutUnavailable,
		})
	}
	return pairs, nil
}

func (c *client) Quote(ctx context.Context, req provider.QuoteRequest) (provider.Quote, error) {
	pairs, err := c.Pairs(ctx)
	if err != nil {
		return provider.Quote{}, err
	}
	q := provider.Quote{Provider: c.name}
	for _, p := range pairs {
		if p.From != req.From || p.To != req.To {
			continue
		}
		q.Pair = p
		if p.Unavailable || p.Rate == "" {
			q.Err = "pair currently unavailable"
			return q, nil
		}
		amount, ok := new(big.Rat).SetString(req.Amount)
		if !ok || amount.Sign() <= 0 {
			q.Err = "invalid amount"
			return q, nil
		}
		if min, ok := new(big.Rat).SetString(p.MinFrom); ok && amount.Cmp(min) < 0 {
			q.Err = fmt.Sprintf("below minimum of %s %s", p.MinFrom, p.From)
			return q, nil
		}
		if max, ok := new(big.Rat).SetString(p.MaxFrom); ok && amount.Cmp(max) > 0 {
			q.Err = fmt.Sprintf("above maximum of %s %s", p.MaxFrom, p.From)
			return q, nil
		}
		rate, ok := new(big.Rat).SetString(p.Rate)
		if !ok {
			q.Err = "pair currently unavailable"
			return q, nil
		}
		q.ToAmount = trimZeros(new(big.Rat).Mul(amount, rate).FloatString(8))
		return q, nil
	}
	q.Err = "pair not supported"
	return q, nil
}

type apiSwap struct {
	ID                 string `json:"id"`
	Status             string `json:"status"`
	FromToken          string `json:"from_token"`
	ToToken            string `json:"to_token"`
	FromAmount         string `json:"from_amount"`
	ToAmountEstimated  string `json:"to_amount_estimated"`
	ToAmountActual     string `json:"to_amount_actual"`
	DepositAddress     string `json:"deposit_address"`
	DestinationAddress string `json:"destination_address"`
	DepositTxHash      string `json:"deposit_tx_hash"`
	PayoutTxHash       string `json:"payout_tx_hash"`
	ExpiresAt          string `json:"expires_at"`
	CreatedAt          string `json:"created_at"`
}

func (s apiSwap) toSwap() provider.Swap {
	return provider.Swap{
		ID:                 s.ID,
		Status:             s.Status,
		From:               s.FromToken,
		To:                 s.ToToken,
		FromAmount:         s.FromAmount,
		ToAmountEstimated:  s.ToAmountEstimated,
		ToAmountActual:     s.ToAmountActual,
		DepositAddress:     s.DepositAddress,
		DestinationAddress: s.DestinationAddress,
		DepositTxHash:      s.DepositTxHash,
		PayoutTxHash:       s.PayoutTxHash,
		ExpiresAt:          s.ExpiresAt,
		CreatedAt:          s.CreatedAt,
	}
}

func (c *client) CreateSwap(ctx context.Context, req provider.SwapRequest) (provider.Swap, error) {
	pairs, err := c.Pairs(ctx)
	if err != nil {
		return provider.Swap{}, err
	}
	symbol := ""
	for _, p := range pairs {
		if p.From == req.From && p.To == req.To {
			symbol = p.Symbol
			break
		}
	}
	if symbol == "" {
		return provider.Swap{}, fmt.Errorf("%s: pair %s/%s not supported", c.name, req.From, req.To)
	}

	body := map[string]string{
		"pair_symbol":         symbol,
		"rate_type":           "floating",
		"from_amount":         req.Amount,
		"destination_address": req.DestinationAddress,
	}
	if req.RefundAddress != "" {
		body["refund_address"] = req.RefundAddress
	}
	if c.affiliate != "" {
		body["source"] = c.affiliate
	}

	var s apiSwap
	if err := c.post(ctx, "/api/v1/swaps", body, &s); err != nil {
		return provider.Swap{}, err
	}
	return s.toSwap(), nil
}

func (c *client) Status(ctx context.Context, id string) (provider.Swap, error) {
	var s apiSwap
	if err := c.get(ctx, "/api/v1/swaps/"+id, &s); err != nil {
		return provider.Swap{}, err
	}
	return s.toSwap(), nil
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
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(resp.Body).Decode(&apiErr) == nil && apiErr.Error != "" {
			return fmt.Errorf("%s: %s", c.name, apiErr.Error)
		}
		return fmt.Errorf("%s: %s", c.name, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func trimZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}
