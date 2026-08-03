package provider

import (
	"context"
	"net/http"
	"time"

	"omenswap.com/swap-aggregator/internal/config"
	"omenswap.com/swap-aggregator/internal/ratelimit"
)

type Pair struct {
	From        string
	To          string
	Symbol      string
	Rate        string
	MinFrom     string
	MaxFrom     string
	Fee         string
	Unavailable bool
}

type RateType string

const (
	Floating RateType = "floating"
	Fixed    RateType = "fixed"
)

// Direction says which side of the trade Amount refers to: FromSide means the
// visitor named what they will send, ToSide means they named what they want to
// receive. Fixed-rate quotes are commonly priced from the receive side.
type Direction string

const (
	FromSide Direction = "from"
	ToSide   Direction = "to"
)

type QuoteRequest struct {
	From      string
	To        string
	Amount    string
	Direction Direction
	RateType  RateType
}

type Quote struct {
	Provider   string
	Pair       Pair
	FromAmount string
	ToAmount   string
	RateType   RateType
	Err        string
}

type SwapRequest struct {
	From               string
	To                 string
	Amount             string
	Direction          Direction
	RateType           RateType
	DestinationAddress string
	RefundAddress      string
}

// RateModes is implemented by providers that handle more than a floating quote
// priced from the send amount, which is all a Provider is required to support.
type RateModes interface {
	SupportsRateMode(RateType, Direction) bool
}

func SupportsRateMode(p Provider, t RateType, d Direction) bool {
	if m, ok := p.(RateModes); ok {
		return m.SupportsRateMode(t, d)
	}
	return t == Floating && d == FromSide
}

type Swap struct {
	ID                 string
	Status             string
	From               string
	To                 string
	FromAmount         string
	ToAmountEstimated  string
	ToAmountActual     string
	DepositAddress     string
	DestinationAddress string
	DepositTxHash      string
	PayoutTxHash       string
	ExpiresAt          string
	CreatedAt          string
}

type Gated interface {
	Brokered() bool
}

// Credentialed is implemented by providers that can create a request-scoped
// client from an end user's API credential. Implementations must return a new
// provider and leave the configured provider unchanged: multiple visitors may
// use different credentials concurrently.
type Credentialed interface {
	WithAPIKey(apiKey string) (Provider, error)
}

// APIKeyPolicy describes which public operations a provider gates. Keeping
// quote and swap creation separate lets the aggregator show public quotes even
// when creating the swap through the API still requires the visitor's key.
type APIKeyPolicy interface {
	APIKeyRequiredForQuote() bool
	APIKeyRequiredForSwap() bool
}

type Linker interface {
	SwapLink(req QuoteRequest) string
}

type Provider interface {
	Name() string
	Pairs(ctx context.Context) ([]Pair, error)
	Quote(ctx context.Context, req QuoteRequest) (Quote, error)
	CreateSwap(ctx context.Context, req SwapRequest) (Swap, error)
	Status(ctx context.Context, id string) (Swap, error)
}

// HTTPClient builds the client an adapter should use, wired to the rate limit
// its config declares.
func HTTPClient(cfg config.Provider, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: ratelimit.NewTransport(nil, cfg.RateLimit, cfg.Burst()),
	}
}
