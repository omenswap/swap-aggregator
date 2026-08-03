package etzswap

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
	provider.Register("etzswap", New)
}

type coin struct {
	code    string
	network string
}

var symbols = []string{"BTC", "DOGE", "ETH", "LTC", "SOL", "USDC", "USDC_POL", "USDT", "USDT_POL", "USDT_TRON", "XMR"}

var coins = map[string]coin{
	"BNB":       {"BNB", "BSC"},
	"TRX":       {"TRX", "TRX"},
	"XRP":       {"XRP", "XRP"},
	"BTC":       {"BTC", "BTC"},
	"DOGE":      {"DOGE", "DOGE"},
	"ETH":       {"ETH", "ETH"},
	"LTC":       {"LTC", "LTC"},
	"SOL":       {"SOL", "SOL"},
	"USDC":      {"USDC", "ETH"},
	"USDC_POL":  {"USDC", "MATIC"},
	"USDT":      {"USDT", "ETH"},
	"USDT_POL":  {"USDT", "MATIC"},
	"USDT_TRON": {"USDT", "TRX"},
	"XMR":       {"XMR", "XMR"},
}

var statusMap = map[string]string{
	"deposit":       "pending",
	"confirmations": "awaiting_confirmation",
	"confirmed":     "deposited",
	"exchanging":    "deposited",
	"sending":       "deposited",
	"success":       "completed",
	"overdue":       "expired",
	"refunded":      "refunded",
}

type client struct {
	name      string
	baseURL   string
	apiKey    string
	secretKey string
	affiliate string
	http      *http.Client
}

func (c *client) Brokered() bool { return true }

// The rate endpoint accepts amountFrom or amountTo for both rate types.
func (c *client) SupportsRateMode(provider.RateType, provider.Direction) bool { return true }

func rateParam(t provider.RateType) string {
	if t == provider.Fixed {
		return "fixed"
	}
	return "float"
}

func (c *client) SwapLink(req provider.QuoteRequest) string {
	v := url.Values{}
	v.Set("rateType", "float")
	if f, ok := coins[req.From]; ok {
		v.Set("coinFrom", f.code)
		v.Set("networkFrom", f.network)
	}
	if t, ok := coins[req.To]; ok {
		v.Set("coinTo", t.code)
		v.Set("networkTo", t.network)
	}
	if req.Amount != "" {
		v.Set("amountFrom", req.Amount)
	}
	if c.affiliate != "" {
		v.Set("affiliateToken", c.affiliate)
	}
	return "https://etz-swap.com/?" + v.Encode()
}

func New(cfg config.Provider) (provider.Provider, error) {
	apiKey, secretKey, _ := strings.Cut(cfg.APIKey, ":")
	return &client{
		name:      cfg.Name,
		baseURL:   strings.TrimRight(cfg.URL, "/"),
		apiKey:    apiKey,
		secretKey: secretKey,
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

type apiError struct {
	Message string `json:"message"`
	Args    struct {
		MinAllowedAmount json.Number `json:"minAllowedAmount"`
		MaxAllowedAmount json.Number `json:"maxAllowedAmount"`
	} `json:"args"`
}

type apiEnvelope struct {
	Data   json.RawMessage     `json:"data"`
	Errors map[string]apiError `json:"errors"`
}

func (e apiEnvelope) firstMessage() string {
	for _, v := range e.Errors {
		if v.Message != "" {
			return v.Message
		}
	}
	return ""
}

type apiRate struct {
	AmountFrom    json.Number `json:"amountFrom"`
	AmountTo      json.Number `json:"amountTo"`
	MinAmountFrom json.Number `json:"minAmountFrom"`
	MaxAmountFrom json.Number `json:"maxAmountFrom"`
	Rate          json.Number `json:"rate"`
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
	if req.Direction == provider.ToSide {
		v.Set("amountTo", req.Amount)
	} else {
		v.Set("amountFrom", req.Amount)
	}
	v.Set("rateType", rateParam(req.RateType))
	if c.affiliate != "" {
		v.Set("affiliateToken", c.affiliate)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/deposit/public/rate?"+v.Encode(), nil)
	if err != nil {
		return provider.Quote{}, err
	}
	c.auth(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return provider.Quote{}, fmt.Errorf("etzswap: %s", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return provider.Quote{}, fmt.Errorf("etzswap: %s", err)
	}

	var env apiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return provider.Quote{}, fmt.Errorf("etzswap: %s", err)
	}

	if resp.StatusCode < 300 {
		var r apiRate
		if err := json.Unmarshal(env.Data, &r); err != nil {
			return provider.Quote{}, fmt.Errorf("etzswap: %s", err)
		}
		q.Pair.MinFrom = r.MinAmountFrom.String()
		q.Pair.MaxFrom = r.MaxAmountFrom.String()
		toAmount, ok := new(big.Rat).SetString(r.AmountTo.String())
		if !ok || toAmount.Sign() <= 0 {
			q.Err = "pair currently unavailable"
			return q, nil
		}
		fromAmount, ok := new(big.Rat).SetString(r.AmountFrom.String())
		if !ok || fromAmount.Sign() <= 0 {
			q.Err = "pair currently unavailable"
			return q, nil
		}
		q.RateType = req.RateType
		q.FromAmount = trimZeros(fromAmount.FloatString(12))
		q.ToAmount = trimZeros(toAmount.FloatString(12))
		q.Pair.Rate = trimZeros(new(big.Rat).Quo(toAmount, fromAmount).FloatString(12))
		return q, nil
	}

	if e, ok := env.Errors["amountTooSmall"]; ok {
		q.Pair.MinFrom = e.Args.MinAllowedAmount.String()
		q.Err = fmt.Sprintf("below minimum of %s %s", e.Args.MinAllowedAmount.String(), req.From)
		return q, nil
	}
	if e, ok := env.Errors["amountTooBig"]; ok {
		q.Pair.MaxFrom = e.Args.MaxAllowedAmount.String()
		q.Err = fmt.Sprintf("above maximum of %s %s", e.Args.MaxAllowedAmount.String(), req.From)
		return q, nil
	}
	if _, ok := env.Errors["unsupportedPair"]; ok {
		q.Err = "pair not supported"
		return q, nil
	}
	if _, ok := env.Errors["unsupportedExchangePairs"]; ok {
		q.Err = "pair not supported"
		return q, nil
	}
	if msg := env.firstMessage(); msg != "" {
		return provider.Quote{}, fmt.Errorf("etzswap: %s", msg)
	}
	return provider.Quote{}, fmt.Errorf("etzswap: %s", resp.Status)
}

type apiCoin struct {
	CoinCode string `json:"coinCode"`
	Network  string `json:"network"`
}

type apiAddress struct {
	Address string `json:"address"`
}

type apiTransfer struct {
	Hash string `json:"hash"`
}

type apiTx struct {
	TransactionID    string      `json:"transactionId"`
	Status           string      `json:"status"`
	Amount           json.Number `json:"amount"`
	AmountTo         json.Number `json:"amountTo"`
	CoinFrom         apiCoin     `json:"coinFrom"`
	CoinTo           apiCoin     `json:"coinTo"`
	Deposit          apiAddress  `json:"deposit"`
	Withdraw         apiAddress  `json:"withdraw"`
	InboundTransfer  apiTransfer `json:"inboundTransfer"`
	OutboundTransfer apiTransfer `json:"outboundTransfer"`
	CreatedAt        string      `json:"createdAt"`
}

func (t apiTx) toSwap() provider.Swap {
	return provider.Swap{
		ID:                 t.TransactionID,
		Status:             mapStatus(t.Status),
		From:               canonical(t.CoinFrom),
		To:                 canonical(t.CoinTo),
		FromAmount:         t.Amount.String(),
		ToAmountEstimated:  t.AmountTo.String(),
		DepositAddress:     t.Deposit.Address,
		DestinationAddress: t.Withdraw.Address,
		DepositTxHash:      t.InboundTransfer.Hash,
		PayoutTxHash:       t.OutboundTransfer.Hash,
		CreatedAt:          t.CreatedAt,
	}
}

func (c *client) CreateSwap(ctx context.Context, req provider.SwapRequest) (provider.Swap, error) {
	from, okFrom := coins[req.From]
	to, okTo := coins[req.To]
	if !okFrom || !okTo || req.From == req.To {
		return provider.Swap{}, fmt.Errorf("etzswap: pair %s/%s not supported", req.From, req.To)
	}
	body := map[string]any{
		"coinFrom":          from.code,
		"networkFrom":       from.network,
		"coinTo":            to.code,
		"networkTo":         to.network,
		"withdrawalAddress": req.DestinationAddress,
		"rateType":          rateParam(req.RateType),
	}
	if req.Direction == provider.ToSide {
		body["amountTo"] = json.Number(req.Amount)
	} else {
		body["amountFrom"] = json.Number(req.Amount)
	}
	if req.RefundAddress != "" {
		body["refundAddress"] = req.RefundAddress
	}
	var t apiTx
	if err := c.post(ctx, "/api/v1/deposit/public/transaction", body, &t); err != nil {
		return provider.Swap{}, err
	}
	s := t.toSwap()
	s.From = req.From
	s.To = req.To
	return s, nil
}

func (c *client) Status(ctx context.Context, id string) (provider.Swap, error) {
	var t apiTx
	if err := c.get(ctx, "/api/v1/deposit/public/transactions/"+id, &t); err != nil {
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
		return fmt.Errorf("etzswap: %s", err)
	}
	defer resp.Body.Close()
	var env apiEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		if resp.StatusCode >= 300 {
			return fmt.Errorf("etzswap: %s", resp.Status)
		}
		return fmt.Errorf("etzswap: %s", err)
	}
	if resp.StatusCode >= 300 {
		if msg := env.firstMessage(); msg != "" {
			return fmt.Errorf("etzswap: %s", msg)
		}
		return fmt.Errorf("etzswap: %s", resp.Status)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("etzswap: %s", err)
	}
	return nil
}

func (c *client) auth(req *http.Request) {
	if c.apiKey == "" {
		return
	}
	req.Header.Set("X-API-KEY", c.apiKey)
	if c.secretKey != "" {
		req.Header.Set("X-API-SECRET-KEY", c.secretKey)
		req.Header.Set("X-API-KEY-VERSION", "1")
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

func trimZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}
