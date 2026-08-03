package swapuz

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	provider.Register("swapuz", New)
}

type coin struct {
	code    string
	network string
}

var coins = map[string]coin{
	"BTC":      {"BTC", "BTC"},
	"DOGE":     {"DOGE", "DOGE"},
	"ETH":      {"ETH", "ETH"},
	"LTC":      {"LTC", "LTC"},
	"SOL":      {"SOL", "SOL"},
	"USDC":     {"USDC", "ETH"},
	"USDT":     {"USDT", "ETH"},
	"USDT_TRX": {"USDT", "TRX"},
	"XMR":      {"XMR", "XMR"},
}

// The published table only covers 0, 1-5, 6 and 10. The rest are read off the
// production frontend, so treat anything unknown below 6 as still in flight.
var statusMap = map[int]string{
	0:  "pending",
	1:  "awaiting_confirmation",
	2:  "exchanging",
	5:  "sending",
	6:  "completed",
	10: "expired",
	11: "refunded",
	12: "on_hold",
	13: "completed",
}

func mapStatus(code int) string {
	if s, ok := statusMap[code]; ok {
		return s
	}
	if code < 6 {
		return "exchanging"
	}
	return fmt.Sprintf("status_%d", code)
}

type client struct {
	name      string
	baseURL   string
	apiKey    string
	affiliate string
	http      *http.Client
}

func New(cfg config.Provider) (provider.Provider, error) {
	key := cfg.APIKey
	if key == "" {
		key = cfg.AffiliateCode
	}
	return &client{
		name:      cfg.Name,
		baseURL:   strings.TrimRight(cfg.URL, "/"),
		apiKey:    key,
		affiliate: cfg.AffiliateCode,
		http:      provider.HTTPClient(cfg, 10*time.Second),
	}, nil
}

func (c *client) Name() string { return c.name }

// Quotes and orders both work unauthenticated; the key only attributes the
// swap to a partner.
func (c *client) Brokered() bool { return true }

// The receive-side amount is documented but rejected by the live API, so only
// send-side quoting is offered.
func (c *client) SupportsRateMode(t provider.RateType, d provider.Direction) bool {
	return d == provider.FromSide
}

func (c *client) Pairs(ctx context.Context) ([]provider.Pair, error) {
	symbols := make([]string, 0, len(coins))
	for s := range coins {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)
	var pairs []provider.Pair
	for _, from := range symbols {
		for _, to := range symbols {
			if from != to {
				pairs = append(pairs, provider.Pair{From: from, To: to})
			}
		}
	}
	return pairs, nil
}

func mode(t provider.RateType) string {
	if t == provider.Fixed {
		return "fix"
	}
	return "float"
}

type apiRate struct {
	Result    json.Number `json:"result"`
	Amount    json.Number `json:"amount"`
	Rate      json.Number `json:"rate"`
	MinAmount json.Number `json:"minAmount"`
	MaxAmount json.Number `json:"maxAmount"`
}

func (c *client) Quote(ctx context.Context, req provider.QuoteRequest) (provider.Quote, error) {
	q := provider.Quote{Provider: c.name, Pair: provider.Pair{From: req.From, To: req.To},
		RateType: req.RateType}
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
	v.Set("from", from.code)
	v.Set("fromNetwork", from.network)
	v.Set("to", to.code)
	v.Set("toNetwork", to.network)
	v.Set("amount", req.Amount)
	v.Set("mode", mode(req.RateType))

	var r apiRate
	if err := c.get(ctx, "/api/home/v1/rate/?"+v.Encode(), &r); err != nil {
		if msg := errorMessage(err); msg != "" {
			q.Err = msg
			return q, nil
		}
		return provider.Quote{}, err
	}
	q.Pair.MinFrom = r.MinAmount.String()
	q.Pair.MaxFrom = r.MaxAmount.String()
	q.Pair.Rate = r.Rate.String()
	if min, ok := new(big.Rat).SetString(q.Pair.MinFrom); ok && amount.Cmp(min) < 0 {
		q.Err = fmt.Sprintf("below minimum of %s %s", q.Pair.MinFrom, req.From)
		return q, nil
	}
	if max, ok := new(big.Rat).SetString(q.Pair.MaxFrom); ok && max.Sign() > 0 && amount.Cmp(max) > 0 {
		q.Err = fmt.Sprintf("above maximum of %s %s", q.Pair.MaxFrom, req.From)
		return q, nil
	}
	q.FromAmount = req.Amount
	q.ToAmount = r.Result.String()
	if q.ToAmount == "" || q.ToAmount == "0" {
		q.Err = "pair currently unavailable"
	}
	return q, nil
}

// Unsupported pairs and bad amounts come back as free text, not a code.
func errorMessage(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not available for market"):
		return "pair not supported"
	case strings.Contains(msg, "check amount"):
		return "invalid amount"
	}
	return ""
}

type apiCoin struct {
	ShortName string `json:"shortName"`
}

type apiOrder struct {
	UID           string      `json:"uid"`
	Status        int         `json:"status"`
	From          apiCoin     `json:"from"`
	To            apiCoin     `json:"to"`
	Amount        json.Number `json:"amount"`
	AmountResult  json.Number `json:"amountResult"`
	AddressFrom   string      `json:"addressFrom"`
	AddressTo     string      `json:"addressTo"`
	DepositTxID   string      `json:"dTxId"`
	WithdrawTxID  string      `json:"wTxId"`
	CreateDate    string      `json:"createDate"`
	FinishPayment string      `json:"finishPayment"`
}

func (o apiOrder) toSwap() provider.Swap {
	return provider.Swap{
		ID:                 o.UID,
		Status:             mapStatus(o.Status),
		From:               canonical(o.From.ShortName),
		To:                 canonical(o.To.ShortName),
		FromAmount:         o.Amount.String(),
		ToAmountEstimated:  o.AmountResult.String(),
		DepositAddress:     o.AddressFrom,
		DestinationAddress: o.AddressTo,
		DepositTxHash:      o.DepositTxID,
		PayoutTxHash:       o.WithdrawTxID,
		ExpiresAt:          o.FinishPayment,
		CreatedAt:          o.CreateDate,
	}
}

func canonical(code string) string {
	for sym, c := range coins {
		if c.code == strings.ToUpper(code) {
			return sym
		}
	}
	return strings.ToUpper(code)
}

// The order id is client-supplied and is the only thing guarding lookups, so
// it has to be unguessable.
func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (c *client) CreateSwap(ctx context.Context, req provider.SwapRequest) (provider.Swap, error) {
	from, okFrom := coins[req.From]
	to, okTo := coins[req.To]
	if !okFrom || !okTo || req.From == req.To {
		return provider.Swap{}, fmt.Errorf("swapuz: pair %s/%s not supported", req.From, req.To)
	}
	uuid, err := newUUID()
	if err != nil {
		return provider.Swap{}, fmt.Errorf("swapuz: %w", err)
	}
	modeCurs := "float"
	if req.RateType == provider.Fixed {
		modeCurs = "fixed"
	}
	body := map[string]any{
		"from":        from.code,
		"fromNetwork": from.network,
		"to":          to.code,
		"toNetwork":   to.network,
		"address":     req.DestinationAddress,
		"amount":      json.Number(req.Amount),
		"uuid":        uuid,
		"modeCurs":    modeCurs,
	}
	var o apiOrder
	if err := c.post(ctx, "/api/home/v1/order", body, &o); err != nil {
		return provider.Swap{}, err
	}
	s := o.toSwap()
	if s.ID == "" {
		s.ID = uuid
	}
	s.From, s.To = req.From, req.To
	return s, nil
}

func (c *client) Status(ctx context.Context, id string) (provider.Swap, error) {
	var o apiOrder
	if err := c.get(ctx, "/api/order/uid/"+url.PathEscape(id), &o); err != nil {
		return provider.Swap{}, err
	}
	return o.toSwap(), nil
}

func (c *client) auth(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Api-key", c.apiKey)
	}
}

func (c *client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	c.auth(req)
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
	c.auth(req)
	return c.do(req, out)
}

// Failures arrive as HTTP 200 with a status field in the body, so the envelope
// decides success, not the transport.
func (c *client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.name, err)
	}
	defer resp.Body.Close()

	var envelope struct {
		Result  json.RawMessage `json:"result"`
		Status  int             `json:"status"`
		Message string          `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		if resp.StatusCode >= 300 {
			return fmt.Errorf("%s: %s", c.name, resp.Status)
		}
		return fmt.Errorf("%s: %w", c.name, err)
	}
	if envelope.Status != http.StatusOK || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		if envelope.Message != "" {
			return fmt.Errorf("%s: %s", c.name, envelope.Message)
		}
		return fmt.Errorf("%s: request failed with status %d", c.name, envelope.Status)
	}
	return json.Unmarshal(envelope.Result, out)
}
