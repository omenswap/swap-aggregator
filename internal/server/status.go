package server

import (
	"context"
	"html/template"
	"net/http"
	"net/url"

	"omenswap.com/swap-aggregator/internal/provider"
)

type swapJSON struct {
	ID                 string `json:"id"`
	Status             string `json:"status"`
	From               string `json:"from"`
	To                 string `json:"to"`
	FromAmount         string `json:"from_amount"`
	ToAmountEstimated  string `json:"to_amount_estimated"`
	ToAmountActual     string `json:"to_amount_actual,omitempty"`
	DepositAddress     string `json:"deposit_address"`
	DestinationAddress string `json:"destination_address"`
	DepositTxHash      string `json:"deposit_tx_hash,omitempty"`
	PayoutTxHash       string `json:"payout_tx_hash,omitempty"`
	ExpiresAt          string `json:"expires_at,omitempty"`
	CreatedAt          string `json:"created_at,omitempty"`
}

func toSwapJSON(s provider.Swap) swapJSON {
	return swapJSON{
		ID:                 s.ID,
		Status:             s.Status,
		From:               s.From,
		To:                 s.To,
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

func (s *Server) fetchSwap(r *http.Request) (provider.Swap, string, int) {
	name := r.PathValue("provider")
	id := r.PathValue("id")
	base, ok := s.byName[name]
	if !ok {
		return provider.Swap{}, "unknown provider", http.StatusNotFound
	}
	p, _ := s.providerForRequest(r, base)
	ctx, cancel := context.WithTimeout(r.Context(), providerTimeout)
	defer cancel()
	swap, err := p.Status(ctx, id)
	if err != nil {
		return provider.Swap{}, err.Error(), http.StatusBadGateway
	}
	return swap, "", 0
}

type statusData struct {
	Brand    brand
	Provider string
	Swap     swapJSON
	PollURL  string
	QR       template.HTML
}

func (s *Server) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	swap, msg, code := s.fetchSwap(r)
	if code != 0 {
		http.Error(w, msg, code)
		return
	}
	name := r.PathValue("provider")
	data := statusData{
		Brand:    s.branding(),
		Provider: name,
		Swap:     toSwapJSON(swap),
		PollURL:  "/api/swap/" + url.PathEscape(name) + "/" + url.PathEscape(swap.ID),
	}
	if code, err := qrSVG(swap.DepositAddress); err == nil {
		data.QR = code
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.tmpl.ExecuteTemplate(w, "status.html", data)
}

func (s *Server) handleStatusJSON(w http.ResponseWriter, r *http.Request) {
	swap, msg, code := s.fetchSwap(r)
	if code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	writeJSON(w, http.StatusOK, toSwapJSON(swap))
}
