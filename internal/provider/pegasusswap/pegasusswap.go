package pegasusswap

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
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
	provider.Register("pegasusswap", New)
}

type coin struct {
	code    string
	network string
}

// Codes and networks are uppercase, read from get-all-coins. The rate endpoint
// tolerates lowercase but creating a transaction does not.
var coins = map[string]coin{
	"BTC":      {"BTC", "BTC"},
	"ETH":      {"ETH", "ETH"},
	"LTC":      {"LTC", "LTC"},
	"SOL":      {"SOL", "SOL"},
	"USDC":     {"USDC", "ETH"},
	"USDT":     {"USDT", "ETH"},
	"USDT_TRX": {"USDT", "TRX"},
	"XMR":      {"XMR", "XMR"},
}

var statusMap = map[int]string{
	9:  "pending",
	1:  "awaiting_confirmation",
	3:  "exchanging",
	12: "sending",
	4:  "completed",
	5:  "expired",
	6:  "failed",
	10: "deposited",
	11: "deposited",
	13: "refunded",
}

func mapStatus(code int) string {
	if s, ok := statusMap[code]; ok {
		return s
	}
	return fmt.Sprintf("status_%d", code)
}

type client struct {
	name      string
	baseURL   string
	publicKey string
	secretKey string
	affiliate string
	http      *http.Client
}

func New(cfg config.Provider) (provider.Provider, error) {
	pub, secret, _ := strings.Cut(cfg.APIKey, ":")
	return &client{
		name:      cfg.Name,
		baseURL:   strings.TrimRight(cfg.URL, "/"),
		publicKey: pub,
		secretKey: secret,
		affiliate: cfg.AffiliateCode,
		http:      provider.HTTPClient(cfg, 10*time.Second),
	}, nil
}

func (c *client) Name() string { return c.name }

func (c *client) Brokered() bool { return c.publicKey != "" && c.secretKey != "" }

// Every endpoint sits behind the partner key, quotes included.
func (c *client) APIKeyRequiredForQuote() bool { return true }
func (c *client) APIKeyRequiredForSwap() bool  { return true }

func (c *client) WithAPIKey(apiKey string) (provider.Provider, error) {
	pub, secret, ok := strings.Cut(apiKey, ":")
	if !ok || pub == "" || secret == "" {
		return nil, fmt.Errorf("pegasusswap api key must be \"public:secret\"")
	}
	clone := *c
	clone.publicKey, clone.secretKey = pub, secret
	return &clone, nil
}

// lastSource picks which side the amount refers to, for both rate types.
func (c *client) SupportsRateMode(provider.RateType, provider.Direction) bool { return true }

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
	return "https://pegasusswap.com/?" + v.Encode()
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

func typeSwap(t provider.RateType) string {
	if t == provider.Fixed {
		return "1"
	}
	return "2"
}

func lastSource(d provider.Direction) string {
	if d == provider.ToSide {
		return "receive"
	}
	return "deposit"
}

type apiRate struct {
	Amount       json.Number `json:"amount"`
	Receive      json.Number `json:"receive"`
	ExchangeRate json.Number `json:"exchangeRate"`
	MinAmount    json.Number `json:"minAmount"`
	MaxAmount    json.Number `json:"maxAmount"`
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
	v.Set("amount", req.Amount)
	v.Set("coinFrom", from.code)
	v.Set("coinTo", to.code)
	v.Set("networkFrom", from.network)
	v.Set("networkTo", to.network)
	v.Set("lastSource", lastSource(req.Direction))
	v.Set("typeSwap", typeSwap(req.RateType))

	var r apiRate
	if err := c.get(ctx, "exchange-coin", "/api/private/exchange-coin?"+v.Encode(), &r); err != nil {
		return provider.Quote{}, err
	}
	q.Pair.MinFrom = r.MinAmount.String()
	q.Pair.MaxFrom = r.MaxAmount.String()
	q.Pair.Rate = r.ExchangeRate.String()
	if min, ok := new(big.Rat).SetString(q.Pair.MinFrom); ok && amount.Cmp(min) < 0 {
		q.Err = fmt.Sprintf("below minimum of %s %s", q.Pair.MinFrom, req.From)
		return q, nil
	}
	if max, ok := new(big.Rat).SetString(q.Pair.MaxFrom); ok && max.Sign() > 0 && amount.Cmp(max) > 0 {
		q.Err = fmt.Sprintf("above maximum of %s %s", q.Pair.MaxFrom, req.From)
		return q, nil
	}
	q.FromAmount = r.Amount.String()
	q.ToAmount = r.Receive.String()
	if q.ToAmount == "" || q.ToAmount == "0" {
		q.Err = "pair currently unavailable"
	}
	return q, nil
}

type apiSide struct {
	Coin struct {
		Name    string      `json:"name"`
		Value   json.Number `json:"value"`
		Network string      `json:"network"`
	} `json:"coin"`
	Address string `json:"address"`
}

type apiTransaction struct {
	OrderNumber string `json:"orderNumber"`
	Status      int    `json:"status"`
	TxID        string `json:"txId"`
	CreatedAt   string `json:"createdAt"`
	ExpiredAt   string `json:"expiredAt"`
	Pairs       struct {
		Deposit apiSide `json:"deposit"`
		Receive apiSide `json:"receive"`
	} `json:"pairs"`
	ReceiveTransaction struct {
		TxID string `json:"txId"`
	} `json:"receiveTransaction"`
}

func (t apiTransaction) toSwap(from, to string) provider.Swap {
	return provider.Swap{
		ID:                 t.OrderNumber,
		Status:             mapStatus(t.Status),
		From:               from,
		To:                 to,
		FromAmount:         t.Pairs.Deposit.Coin.Value.String(),
		ToAmountEstimated:  t.Pairs.Receive.Coin.Value.String(),
		DepositAddress:     t.Pairs.Deposit.Address,
		DestinationAddress: t.Pairs.Receive.Address,
		DepositTxHash:      t.TxID,
		PayoutTxHash:       t.ReceiveTransaction.TxID,
		ExpiresAt:          t.ExpiredAt,
		CreatedAt:          t.CreatedAt,
	}
}

func (c *client) CreateSwap(ctx context.Context, req provider.SwapRequest) (provider.Swap, error) {
	from, okFrom := coins[req.From]
	to, okTo := coins[req.To]
	if !okFrom || !okTo || req.From == req.To {
		return provider.Swap{}, fmt.Errorf("pegasusswap: pair %s/%s not supported", req.From, req.To)
	}
	amount, ok := new(big.Rat).SetString(req.Amount)
	if !ok || amount.Sign() <= 0 {
		return provider.Swap{}, fmt.Errorf("pegasusswap: invalid amount %q", req.Amount)
	}
	swapType := 2
	if req.RateType == provider.Fixed {
		swapType = 1
	}
	body := map[string]any{
		"depositCoin":    from.code,
		"depositNetwork": from.network,
		"depositAmount":  json.Number(req.Amount),
		"receiveCoin":    to.code,
		"receiveNetwork": to.network,
		"receiveAddress": req.DestinationAddress,
		"typeSwap":       swapType,
		"lastSource":     lastSource(req.Direction),
	}
	if req.RefundAddress != "" {
		body["refundAddress"] = req.RefundAddress
	}
	var t apiTransaction
	if err := c.post(ctx, "create-transaction", "/api/private/create-transaction", body, &t); err != nil {
		return provider.Swap{}, err
	}
	s := t.toSwap(req.From, req.To)
	if s.ID == "" {
		return provider.Swap{}, fmt.Errorf("pegasusswap: no order number returned")
	}
	return s, nil
}

func (c *client) Status(ctx context.Context, id string) (provider.Swap, error) {
	var t apiTransaction
	if err := c.get(ctx, "get-transaction", "/api/private/get-transaction?id="+url.QueryEscape(id), &t); err != nil {
		return provider.Swap{}, err
	}
	return t.toSwap(canonical(t.Pairs.Deposit.Coin.Name), canonical(t.Pairs.Receive.Coin.Name)), nil
}

func canonical(code string) string {
	for sym, c := range coins {
		if c.code == strings.ToUpper(code) {
			return sym
		}
	}
	return strings.ToUpper(code)
}

// The signature covers the endpoint's short name, not the request body.
func (c *client) sign(req *http.Request, payload string) {
	mac := hmac.New(sha512.New, []byte(c.secretKey))
	mac.Write([]byte(payload))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-public-key", c.publicKey)
	req.Header.Set("x-api-payload", payload)
	req.Header.Set("x-api-signature", hex.EncodeToString(mac.Sum(nil)))
}

func (c *client) get(ctx context.Context, payload, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	c.sign(req, payload)
	return c.do(req, out)
}

func (c *client) post(ctx context.Context, payload, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	c.sign(req, payload)
	return c.do(req, out)
}

func (c *client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", c.name, apiError(resp))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Validation failures come back with message as an array of strings.
func apiError(resp *http.Response) string {
	var e struct {
		Message json.RawMessage `json:"message"`
	}
	if json.NewDecoder(resp.Body).Decode(&e) != nil || len(e.Message) == 0 {
		return resp.Status
	}
	var single string
	if json.Unmarshal(e.Message, &single) == nil && single != "" {
		return single
	}
	var many []string
	if json.Unmarshal(e.Message, &many) == nil && len(many) > 0 {
		return strings.Join(many, "; ")
	}
	return resp.Status
}
