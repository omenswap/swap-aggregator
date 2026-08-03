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
		http:      provider.HTTPClient(cfg, 10*time.Second),
	}, nil
}

func (c *client) Name() string { return c.name }

type apiPair struct {
	Symbol            string            `json:"symbol"`
	FromToken         string            `json:"from_token"`
	ToToken           string            `json:"to_token"`
	Rate              string            `json:"rate"`
	PriceUnavailable  bool              `json:"price_unavailable"`
	FixedUnavailable  bool              `json:"fixed_rate_unavailable"`
	PayoutUnavailable bool              `json:"payout_unavailable"`
	Fees              map[string]string `json:"fees"`
	MaxFromAmount     map[string]string `json:"max_from_amount"`
	MinFromAmount     map[string]string `json:"min_from_amount"`
}

// Limits and fees are quoted per rate type, so the same pair looks different
// depending on which one the visitor asked for.
func (c *client) pairsFor(ctx context.Context, rate provider.RateType) ([]provider.Pair, error) {
	var resp struct {
		Pairs []apiPair `json:"pairs"`
	}
	if err := c.get(ctx, "/api/v1/pairs", &resp); err != nil {
		return nil, err
	}
	key := string(rate)
	pairs := make([]provider.Pair, 0, len(resp.Pairs))
	for _, p := range resp.Pairs {
		pairs = append(pairs, provider.Pair{
			From:    p.FromToken,
			To:      p.ToToken,
			Symbol:  p.Symbol,
			Rate:    p.Rate,
			MinFrom: p.MinFromAmount[key],
			MaxFrom: p.MaxFromAmount[key],
			Fee:     p.Fees[key],
			Unavailable: p.PriceUnavailable || p.PayoutUnavailable ||
				(rate == provider.Fixed && p.FixedUnavailable),
		})
	}
	return pairs, nil
}

func (c *client) Pairs(ctx context.Context) ([]provider.Pair, error) {
	return c.pairsFor(ctx, provider.Floating)
}

func (c *client) SupportsRateMode(t provider.RateType, d provider.Direction) bool {
	return d == provider.FromSide
}

func (c *client) pairFor(pairs []provider.Pair, from, to string) (provider.Pair, bool) {
	for _, p := range pairs {
		if p.From == from && p.To == to {
			return p, true
		}
	}
	return provider.Pair{}, false
}

type apiQuote struct {
	FromAmount string `json:"from_amount"`
	ToAmount   string `json:"to_amount"`
	Rate       string `json:"rate"`
}

func (c *client) fixedQuote(ctx context.Context, req provider.QuoteRequest) (provider.Quote, error) {
	q := provider.Quote{Provider: c.name, RateType: provider.Fixed}
	pairs, err := c.pairsFor(ctx, provider.Fixed)
	if err != nil {
		return provider.Quote{}, err
	}
	raw, ok := c.pairFor(pairs, req.From, req.To)
	if !ok {
		q.Err = "pair not supported"
		return q, nil
	}
	q.Pair = raw
	if raw.Unavailable {
		q.Err = "no fixed rate for " + req.From
		return q, nil
	}

	target, ok := new(big.Rat).SetString(req.Amount)
	if !ok || target.Sign() <= 0 {
		q.Err = "invalid amount"
		return q, nil
	}
	if min, ok := new(big.Rat).SetString(raw.MinFrom); ok && target.Cmp(min) < 0 {
		q.Err = fmt.Sprintf("below minimum of %s %s", raw.MinFrom, raw.From)
		return q, nil
	}
	if max, ok := new(big.Rat).SetString(raw.MaxFrom); ok && max.Sign() > 0 && target.Cmp(max) > 0 {
		q.Err = fmt.Sprintf("above maximum of %s %s", raw.MaxFrom, raw.From)
		return q, nil
	}

	out, msg, err := c.solveFixed(ctx, raw, target)
	if err != nil {
		return provider.Quote{}, err
	}
	if msg != "" {
		q.Err = msg
		return q, nil
	}
	q.FromAmount = trimZeros(out.FromAmount)
	q.ToAmount = trimZeros(out.ToAmount)
	q.Pair.Rate = out.Rate
	return q, nil
}

const fixedSolveCalls = 3

// The API only prices a fixed rate from the receive side, so walk the receive
// amount until the deposit it implies lands just under what the visitor wants
// to send. Pricing is linear in the receive amount, so scaling converges in a
// couple of steps.
func (c *client) solveFixed(ctx context.Context, pair provider.Pair, target *big.Rat) (apiQuote, string, error) {
	rate, ok := new(big.Rat).SetString(pair.Rate)
	if !ok || rate.Sign() <= 0 {
		return apiQuote{}, "pair currently unavailable", nil
	}
	guess := new(big.Rat).Mul(target, rate)

	var best apiQuote
	var bestFrom *big.Rat
	for range fixedSolveCalls {
		body := map[string]string{
			"pair_symbol": pair.Symbol,
			"rate_type":   string(provider.Fixed),
			"to_amount":   trimZeros(guess.FloatString(12)),
		}
		var out apiQuote
		if err := c.post(ctx, "/api/v1/quote", body, &out); err != nil {
			if bestFrom != nil {
				break
			}
			return apiQuote{}, quoteError(err), nil
		}
		from, ok := new(big.Rat).SetString(out.FromAmount)
		if !ok || from.Sign() <= 0 {
			return apiQuote{}, "pair currently unavailable", nil
		}
		if from.Cmp(target) <= 0 && (bestFrom == nil || from.Cmp(bestFrom) > 0) {
			best, bestFrom = out, from
		}
		// Within 0.1% and not over: close enough to stop paying for round trips.
		if bestFrom != nil {
			gap := new(big.Rat).Sub(target, bestFrom)
			if gap.Cmp(new(big.Rat).Mul(target, big.NewRat(1, 1000))) <= 0 {
				break
			}
		}
		guess.Mul(guess, new(big.Rat).Quo(target, from))
		// Undershoot slightly so the deposit never exceeds what was asked for.
		guess.Mul(guess, big.NewRat(9995, 10000))
	}
	if bestFrom == nil {
		return apiQuote{}, "no fixed rate for this amount", nil
	}
	return best, "", nil
}

func quoteError(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "pair not found"), strings.Contains(msg, "inactive"):
		return "pair not supported"
	case strings.Contains(msg, "fixed-rate swaps are not available"):
		return "no fixed rate for this asset"
	case strings.Contains(msg, "minimum"), strings.Contains(msg, "maximum"),
		strings.Contains(msg, "amount"):
		return strings.TrimSpace(strings.SplitN(err.Error(), ":", 2)[1])
	}
	return "provider unavailable"
}

func (c *client) Quote(ctx context.Context, req provider.QuoteRequest) (provider.Quote, error) {
	if req.RateType == provider.Fixed {
		return c.fixedQuote(ctx, req)
	}
	pairs, err := c.Pairs(ctx)
	if err != nil {
		return provider.Quote{}, err
	}
	q := provider.Quote{Provider: c.name, RateType: provider.Floating}
	for _, p := range pairs {
		if p.From != req.From || p.To != req.To {
			continue
		}
		q.Pair = p
		q.FromAmount = req.Amount
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
	rate := req.RateType
	if rate == "" {
		rate = provider.Floating
	}
	pairs, err := c.pairsFor(ctx, rate)
	if err != nil {
		return provider.Swap{}, err
	}
	pair, ok := c.pairFor(pairs, req.From, req.To)
	if !ok {
		return provider.Swap{}, fmt.Errorf("%s: pair %s/%s not supported", c.name, req.From, req.To)
	}

	body := map[string]string{
		"pair_symbol":         pair.Symbol,
		"rate_type":           string(rate),
		"destination_address": req.DestinationAddress,
	}
	if rate == provider.Fixed {
		target, ok := new(big.Rat).SetString(req.Amount)
		if !ok || target.Sign() <= 0 {
			return provider.Swap{}, fmt.Errorf("%s: invalid amount %q", c.name, req.Amount)
		}
		solved, msg, err := c.solveFixed(ctx, pair, target)
		if err != nil {
			return provider.Swap{}, err
		}
		if msg != "" {
			return provider.Swap{}, fmt.Errorf("%s: %s", c.name, msg)
		}
		body["to_amount"] = solved.ToAmount
	} else {
		body["from_amount"] = req.Amount
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
