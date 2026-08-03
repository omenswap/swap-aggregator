package fixedfloat

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"omenswap.com/swap-aggregator/internal/config"
	"omenswap.com/swap-aggregator/internal/provider"
)

func init() {
	provider.Register("fixedfloat", New)
}

var currencyCodes = map[string]string{
	"BTC":       "BTC",
	"ETH":       "ETH",
	"SOL":       "SOL",
	"XMR":       "XMR",
	"LTC":       "LTC",
	"DOGE":      "DOGE",
	"USDC":      "USDCETH",
	"USDC_POL":  "USDCMATIC",
	"USDT":      "USDTETH",
	"USDT_POL":  "USDTMATIC",
	"USDT_TRON": "USDTTRC",
}

type client struct {
	name       string
	baseURL    string
	key        string
	secret     string
	affiliate  string
	feePercent float64
	http       *http.Client
}

func New(cfg config.Provider) (provider.Provider, error) {
	key, secret, _ := strings.Cut(cfg.APIKey, ":")
	return &client{
		name:       cfg.Name,
		baseURL:    strings.TrimRight(cfg.URL, "/"),
		key:        key,
		secret:     secret,
		affiliate:  cfg.AffiliateCode,
		feePercent: cfg.AffiliateFeePercent,
		http:       provider.HTTPClient(cfg, 10*time.Second),
	}, nil
}

func (c *client) Brokered() bool { return c.key != "" && c.secret != "" }

func (c *client) APIKeyRequiredForQuote() bool { return true }
func (c *client) APIKeyRequiredForSwap() bool  { return true }

func (c *client) SwapLink(req provider.QuoteRequest) string {
	v := url.Values{}
	if f, ok := currencyCodes[req.From]; ok {
		v.Set("fromccy", f)
	}
	if t, ok := currencyCodes[req.To]; ok {
		v.Set("toccy", t)
	}
	if req.Amount != "" {
		v.Set("qty", req.Amount)
	}
	if c.affiliate != "" {
		v.Set("ref", c.affiliate)
	}
	return "https://ff.io/?" + v.Encode()
}

func (c *client) Name() string { return c.name }

func (c *client) WithAPIKey(apiKey string) (provider.Provider, error) {
	key, secret, ok := strings.Cut(strings.TrimSpace(apiKey), ":")
	if !ok || key == "" || secret == "" {
		return nil, fmt.Errorf("fixedfloat: enter the credential as key:secret")
	}
	clone := *c
	clone.key = key
	clone.secret = secret
	return &clone, nil
}

func (c *client) Pairs(ctx context.Context) ([]provider.Pair, error) {
	symbols := make([]string, 0, len(currencyCodes))
	for s := range currencyCodes {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)
	var pairs []provider.Pair
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

type numString string

func (n *numString) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*n = numString(s)
		return nil
	}
	if string(b) == "null" {
		return nil
	}
	var num json.Number
	if err := json.Unmarshal(b, &num); err != nil {
		return err
	}
	*n = numString(num.String())
	return nil
}

type apiSide struct {
	Code    string    `json:"code"`
	Amount  numString `json:"amount"`
	Rate    numString `json:"rate"`
	Min     numString `json:"min"`
	Max     numString `json:"max"`
	Address string    `json:"address"`
	Tx      struct {
		ID string `json:"id"`
	} `json:"tx"`
}

// The v2 API takes type float|fixed with direction from|to on both price and
// create.
func (c *client) SupportsRateMode(provider.RateType, provider.Direction) bool { return true }

func typeParam(t provider.RateType) string {
	if t == provider.Fixed {
		return "fixed"
	}
	return "float"
}

func directionParam(d provider.Direction) string {
	if d == provider.ToSide {
		return "to"
	}
	return "from"
}

func (c *client) Quote(ctx context.Context, req provider.QuoteRequest) (provider.Quote, error) {
	q := provider.Quote{Provider: c.name, Pair: provider.Pair{From: req.From, To: req.To}}
	fromCcy, ok := currencyCodes[req.From]
	toCcy, ok2 := currencyCodes[req.To]
	if !ok || !ok2 || req.From == req.To {
		q.Err = "pair not supported"
		return q, nil
	}

	body := map[string]string{
		"type":      typeParam(req.RateType),
		"fromCcy":   fromCcy,
		"toCcy":     toCcy,
		"direction": directionParam(req.Direction),
		"amount":    req.Amount,
	}
	c.addAffiliate(body)
	var data struct {
		From apiSide `json:"from"`
		To   apiSide `json:"to"`
	}
	if err := c.post(ctx, "/api/v2/price", body, &data); err != nil {
		msg := strings.ToLower(err.Error())
		switch {
		case strings.Contains(msg, "min"):
			q.Err = fmt.Sprintf("amount below minimum: %s", apiMessage(err, c.name))
			return q, nil
		case strings.Contains(msg, "max") || strings.Contains(msg, "exceed"):
			q.Err = fmt.Sprintf("amount above maximum: %s", apiMessage(err, c.name))
			return q, nil
		case strings.Contains(msg, "currency") || strings.Contains(msg, "pair") || strings.Contains(msg, "direction"):
			q.Err = "pair not supported"
			return q, nil
		}
		return provider.Quote{}, err
	}

	q.RateType = req.RateType
	q.Pair.MinFrom = string(data.From.Min)
	q.Pair.MaxFrom = string(data.From.Max)
	q.FromAmount = string(data.From.Amount)
	q.ToAmount = string(data.To.Amount)
	fromAmount, ok := new(big.Rat).SetString(string(data.From.Amount))
	toAmount, ok2 := new(big.Rat).SetString(string(data.To.Amount))
	if ok && ok2 && fromAmount.Sign() > 0 {
		q.Pair.Rate = trimZeros(new(big.Rat).Quo(toAmount, fromAmount).FloatString(12))
	}
	return q, nil
}

func apiMessage(err error, name string) string {
	return strings.TrimPrefix(err.Error(), name+": ")
}

var statusMap = map[string]string{
	"NEW":       "pending",
	"PENDING":   "awaiting_confirmation",
	"EXCHANGE":  "deposited",
	"WITHDRAW":  "deposited",
	"DONE":      "completed",
	"EXPIRED":   "expired",
	"EMERGENCY": "failed",
}

func mapStatus(s string) string {
	if mapped, ok := statusMap[s]; ok {
		return mapped
	}
	return s
}

type apiOrder struct {
	ID     string  `json:"id"`
	Token  string  `json:"token"`
	Status string  `json:"status"`
	From   apiSide `json:"from"`
	To     apiSide `json:"to"`
	Time   struct {
		Reg        numString `json:"reg"`
		Expiration numString `json:"expiration"`
	} `json:"time"`
}

func (o apiOrder) toSwap(from, to string) provider.Swap {
	s := provider.Swap{
		ID:                 o.ID + ":" + o.Token,
		Status:             mapStatus(o.Status),
		From:               from,
		To:                 to,
		FromAmount:         string(o.From.Amount),
		ToAmountEstimated:  string(o.To.Amount),
		DepositAddress:     o.From.Address,
		DestinationAddress: o.To.Address,
		DepositTxHash:      o.From.Tx.ID,
		PayoutTxHash:       o.To.Tx.ID,
		ExpiresAt:          string(o.Time.Expiration),
		CreatedAt:          string(o.Time.Reg),
	}
	if o.Status == "DONE" {
		s.ToAmountActual = string(o.To.Amount)
	}
	return s
}

func (c *client) canonical(code string) string {
	for symbol, ccy := range currencyCodes {
		if ccy == code {
			return symbol
		}
	}
	return code
}

func (c *client) CreateSwap(ctx context.Context, req provider.SwapRequest) (provider.Swap, error) {
	fromCcy, ok := currencyCodes[req.From]
	toCcy, ok2 := currencyCodes[req.To]
	if !ok || !ok2 || req.From == req.To {
		return provider.Swap{}, fmt.Errorf("%s: pair %s/%s not supported", c.name, req.From, req.To)
	}

	body := map[string]string{
		"type":      typeParam(req.RateType),
		"fromCcy":   fromCcy,
		"toCcy":     toCcy,
		"direction": directionParam(req.Direction),
		"amount":    req.Amount,
		"toAddress": req.DestinationAddress,
	}
	c.addAffiliate(body)

	var o apiOrder
	if err := c.post(ctx, "/api/v2/create", body, &o); err != nil {
		return provider.Swap{}, err
	}
	return o.toSwap(req.From, req.To), nil
}

func (c *client) Status(ctx context.Context, id string) (provider.Swap, error) {
	orderID, token, ok := strings.Cut(id, ":")
	if !ok {
		return provider.Swap{}, fmt.Errorf("%s: invalid swap id %q", c.name, id)
	}
	body := map[string]string{"id": orderID, "token": token}
	var o apiOrder
	if err := c.post(ctx, "/api/v2/order", body, &o); err != nil {
		return provider.Swap{}, err
	}
	return o.toSwap(c.canonical(o.From.Code), c.canonical(o.To.Code)), nil
}

// afftax is only honoured alongside a refcode from the same account.
func (c *client) addAffiliate(body map[string]string) {
	if c.affiliate == "" {
		return
	}
	body["refcode"] = c.affiliate
	if c.feePercent > 0 {
		body["afftax"] = strconv.FormatFloat(c.feePercent, 'f', -1, 64)
	}
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
	mac := hmac.New(sha256.New, []byte(c.secret))
	mac.Write(b)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-API-KEY", c.key)
	req.Header.Set("X-API-SIGN", hex.EncodeToString(mac.Sum(nil)))

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.name, err)
	}
	defer resp.Body.Close()

	var envelope struct {
		Code json.Number     `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		if resp.StatusCode >= 300 {
			return fmt.Errorf("%s: %s", c.name, resp.Status)
		}
		return fmt.Errorf("%s: %w", c.name, err)
	}
	if envelope.Code.String() != "0" {
		if envelope.Msg != "" {
			return fmt.Errorf("%s: %s", c.name, envelope.Msg)
		}
		return fmt.Errorf("%s: %s", c.name, resp.Status)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", c.name, resp.Status)
	}
	return json.Unmarshal(envelope.Data, out)
}

func trimZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}
