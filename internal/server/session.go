package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func escapePath(s string) string { return url.PathEscape(s) }

const (
	sessionCookie   = "swap_session"
	maxSessionSwaps = 20
	maxSessionBytes = 3800
)

type sessionSwap struct {
	Provider string `json:"p"`
	ID       string `json:"i"`
	From     string `json:"f"`
	To       string `json:"t"`
	Amount   string `json:"a"`
	Created  int64  `json:"c,omitempty"`
}

func capSession(swaps []sessionSwap) []sessionSwap {
	if len(swaps) > maxSessionSwaps {
		return swaps[:maxSessionSwaps]
	}
	return swaps
}

func readSession(r *http.Request) []sessionSwap {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" || len(cookie.Value) > maxSessionBytes {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return nil
	}
	var swaps []sessionSwap
	if json.Unmarshal(raw, &swaps) != nil {
		return nil
	}
	var out []sessionSwap
	for _, sw := range swaps {
		if sw.Provider != "" && sw.ID != "" {
			out = append(out, sw)
		}
	}
	return capSession(out)
}

func writeSession(w http.ResponseWriter, r *http.Request, swaps []sessionSwap) {
	raw, err := json.Marshal(capSession(swaps))
	if err != nil {
		return
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	for len(encoded) > maxSessionBytes && len(swaps) > 1 {
		swaps = swaps[:len(swaps)-1]
		raw, _ = json.Marshal(swaps)
		encoded = base64.RawURLEncoding.EncodeToString(raw)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    encoded,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		SameSite: http.SameSiteLaxMode,
	})
}

func recordSwap(w http.ResponseWriter, r *http.Request, sw sessionSwap) {
	sw.Created = time.Now().Unix()
	swaps := []sessionSwap{sw}
	for _, old := range readSession(r) {
		if old.Provider == sw.Provider && old.ID == sw.ID {
			continue
		}
		swaps = append(swaps, old)
	}
	writeSession(w, r, swaps)
}

type sessionRow struct {
	Provider string
	ID       string
	From     string
	To       string
	Amount   string
	Created  string
	URL      string
	PollURL  string
}

type swapsData struct {
	Swaps []sessionRow
}

func (s *Server) handleSwapsPage(w http.ResponseWriter, r *http.Request) {
	var rows []sessionRow
	for _, sw := range readSession(r) {
		if _, ok := s.byName[sw.Provider]; !ok {
			continue
		}
		row := sessionRow{
			Provider: sw.Provider,
			ID:       sw.ID,
			From:     sw.From,
			To:       sw.To,
			Amount:   sw.Amount,
			URL:      "/swap/" + escapePath(sw.Provider) + "/" + escapePath(sw.ID),
			PollURL:  "/api/swap/" + escapePath(sw.Provider) + "/" + escapePath(sw.ID),
		}
		if sw.Created > 0 {
			row.Created = time.Unix(sw.Created, 0).UTC().Format("2006-01-02 15:04 UTC")
		}
		rows = append(rows, row)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.tmpl.ExecuteTemplate(w, "swaps.html", swapsData{Swaps: rows})
}
