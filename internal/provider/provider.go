package provider

import "context"

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

type QuoteRequest struct {
	From   string
	To     string
	Amount string
}

type Quote struct {
	Provider string
	Pair     Pair
	ToAmount string
	Err      string
}

type SwapRequest struct {
	From               string
	To                 string
	Amount             string
	DestinationAddress string
	RefundAddress      string
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
