package server

import (
	"context"
	"encoding/json"
	"html/template"
	"math/big"
	"net/http"
	"sort"
	"sync"

	"omenswap.com/swap-aggregator/internal/provider"
)

type quoteJSON struct {
	Provider         string `json:"provider"`
	Rate             string `json:"rate,omitempty"`
	Fee              string `json:"fee,omitempty"`
	FromAmount       string `json:"from_amount,omitempty"`
	ToAmount         string `json:"to_amount,omitempty"`
	RateType         string `json:"rate_type,omitempty"`
	Link             string `json:"link,omitempty"`
	Err              string `json:"err,omitempty"`
	QuoteNeedsAPIKey bool   `json:"quote_requires_api_key,omitempty"`
	SwapNeedsAPIKey  bool   `json:"swap_requires_api_key,omitempty"`
	HasAPIKey        bool   `json:"has_api_key,omitempty"`
	NoFixedRate      bool   `json:"no_fixed_rate,omitempty"`
}

// Both rate types are priced from what the visitor sends. Providers whose API
// only prices a fixed rate from the receive side solve for it themselves.
func rateMode(q string) (provider.RateType, provider.Direction) {
	if q == string(provider.Fixed) {
		return provider.Fixed, provider.FromSide
	}
	return provider.Floating, provider.FromSide
}

func swapLink(p provider.Provider, req provider.QuoteRequest) string {
	if g, ok := p.(provider.Gated); ok && !g.Brokered() {
		if l, ok := p.(provider.Linker); ok {
			return l.SwapLink(req)
		}
	}
	return ""
}

type formData struct {
	Provider    string
	From        string
	To          string
	Amount      string
	Destination string
	Refund      string
	Rate        string
	Fixed       bool
}

type indexData struct {
	Tokens    []string
	Providers []string
	PairData  template.JS
	Error     string
	Form      formData
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rateType, _ := rateMode(q.Get("rate"))
	rate := string(rateType)
	s.renderIndex(w, q.Get("error"), formData{
		Provider:    q.Get("provider"),
		From:        q.Get("from"),
		To:          q.Get("to"),
		Amount:      q.Get("amount"),
		Destination: q.Get("destination_address"),
		Refund:      q.Get("refund_address"),
		Rate:        rate,
		Fixed:       rate == string(provider.Fixed),
	})
}

func (s *Server) renderIndex(w http.ResponseWriter, errMsg string, form formData) {
	seen := map[string]bool{}
	seenDir := map[[2]string]bool{}
	var tokens []string
	var dirs [][2]string
	for _, p := range s.providers {
		pairs, err := s.pairs(context.Background(), p)
		if err != nil {
			continue
		}
		for _, pair := range pairs {
			for _, t := range []string{pair.From, pair.To} {
				if !seen[t] {
					seen[t] = true
					tokens = append(tokens, t)
				}
			}
			d := [2]string{pair.From, pair.To}
			if !seenDir[d] {
				seenDir[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	sort.Strings(tokens)
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i][0] != dirs[j][0] {
			return dirs[i][0] < dirs[j][0]
		}
		return dirs[i][1] < dirs[j][1]
	})
	pairJSON, _ := json.Marshal(dirs)
	var names []string
	for _, p := range s.providers {
		names = append(names, p.Name())
	}
	data := indexData{Tokens: tokens, Providers: names, PairData: template.JS(pairJSON),
		Error: errMsg, Form: form}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.tmpl.ExecuteTemplate(w, "index.html", data)
}

func (s *Server) handleQuotes(w http.ResponseWriter, r *http.Request) {
	from, to, amount := r.URL.Query().Get("from"), r.URL.Query().Get("to"), r.URL.Query().Get("amount")
	if from == "" || to == "" || amount == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from, to and amount are required"})
		return
	}
	rateType, direction := rateMode(r.URL.Query().Get("rate"))
	req := provider.QuoteRequest{From: from, To: to, Amount: amount,
		Direction: direction, RateType: rateType}

	quotes := make([]quoteJSON, len(s.providers))
	var wg sync.WaitGroup
	for i, base := range s.providers {
		wg.Go(func() {
			if !provider.SupportsRateMode(base, rateType, direction) {
				quotes[i] = quoteJSON{Provider: base.Name(), Err: "no fixed rate",
					NoFixedRate: true, Link: swapLink(base, req)}
				return
			}
			p, hasUserKey := s.providerForRequest(r, base)
			quoteNeedsKey, swapNeedsKey := apiKeyRequirements(base)
			quoteNeedsKey = quoteNeedsKey && !hasUserKey
			swapNeedsKey = swapNeedsKey && !hasUserKey
			if quoteNeedsKey {
				quotes[i] = quoteJSON{
					Provider:         base.Name(),
					Err:              "api key required for quote",
					Link:             swapLink(base, req),
					QuoteNeedsAPIKey: true,
					SwapNeedsAPIKey:  swapNeedsKey,
				}
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), providerTimeout)
			defer cancel()
			q, err := p.Quote(ctx, req)
			if err != nil {
				quotes[i] = quoteJSON{Provider: p.Name(), Err: "provider unavailable", Link: swapLink(base, req), SwapNeedsAPIKey: swapNeedsKey, HasAPIKey: hasUserKey}
				return
			}
			link := ""
			if swapNeedsKey {
				link = swapLink(base, req)
			}
			quotes[i] = quoteJSON{Provider: p.Name(), Rate: q.Pair.Rate, Fee: q.Pair.Fee,
				FromAmount: q.FromAmount, ToAmount: q.ToAmount, RateType: string(rateType),
				Err: q.Err, Link: link, SwapNeedsAPIKey: swapNeedsKey, HasAPIKey: hasUserKey}
		})
	}
	wg.Wait()

	sort.SliceStable(quotes, func(i, j int) bool {
		a, aok := new(big.Rat).SetString(quotes[i].ToAmount)
		b, bok := new(big.Rat).SetString(quotes[j].ToAmount)
		if aok != bok {
			return aok
		}
		if !aok {
			return false
		}
		return a.Cmp(b) > 0
	})
	writeJSON(w, http.StatusOK, map[string]any{"quotes": quotes})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
