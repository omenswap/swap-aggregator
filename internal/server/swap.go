package server

import (
	"context"
	"net/http"
	"net/url"

	"omenswap.com/swap-aggregator/internal/provider"
)

func (s *Server) handleCreateSwap(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := r.PostFormValue("provider")
	rateType, direction := rateMode(r.PostFormValue("rate"))
	req := provider.SwapRequest{
		From:               r.PostFormValue("from"),
		To:                 r.PostFormValue("to"),
		Amount:             r.PostFormValue("amount"),
		Direction:          direction,
		RateType:           rateType,
		DestinationAddress: r.PostFormValue("destination_address"),
		RefundAddress:      r.PostFormValue("refund_address"),
	}
	if name == "" || req.From == "" || req.To == "" || req.Amount == "" || req.DestinationAddress == "" {
		http.Error(w, "provider, from, to, amount and destination_address are required", http.StatusBadRequest)
		return
	}
	base, ok := s.byName[name]
	if !ok {
		http.Error(w, "unknown provider", http.StatusBadRequest)
		return
	}
	if !provider.SupportsRateMode(base, rateType, direction) {
		http.Error(w, "provider does not offer a fixed rate", http.StatusBadRequest)
		return
	}
	p, _ := s.providerForRequest(r, base)
	if g, ok := p.(provider.Gated); ok && !g.Brokered() {
		http.Error(w, "provider requires an api key for swaps through this site", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), providerTimeout)
	defer cancel()
	swap, err := p.CreateSwap(ctx, req)
	if err != nil {
		q := url.Values{
			"error":               {err.Error()},
			"provider":            {name},
			"from":                {req.From},
			"to":                  {req.To},
			"amount":              {req.Amount},
			"destination_address": {req.DestinationAddress},
			"rate":                {string(rateType)},
		}
		if req.RefundAddress != "" {
			q.Set("refund_address", req.RefundAddress)
		}
		http.Redirect(w, r, "/?"+q.Encode(), http.StatusSeeOther)
		return
	}
	recordSwap(w, r, sessionSwap{Provider: name, ID: swap.ID,
		From: req.From, To: req.To, Amount: req.Amount})
	http.Redirect(w, r, "/swap/"+url.PathEscape(name)+"/"+url.PathEscape(swap.ID), http.StatusSeeOther)
}
