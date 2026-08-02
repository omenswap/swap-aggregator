package server

import (
	"context"
	"embed"
	"html/template"
	"net/http"
	"time"

	"omenswap.com/swap-aggregator/internal/cache"
	"omenswap.com/swap-aggregator/internal/provider"
)

//go:embed templates static
var assets embed.FS

const providerTimeout = 10 * time.Second

type Server struct {
	providers []provider.Provider
	byName    map[string]provider.Provider
	pairCache *cache.Cache[[]provider.Pair]
	tmpl      *template.Template
	mux       *http.ServeMux
}

func New(providers []provider.Provider, ttl time.Duration) *Server {
	s := &Server{
		providers: providers,
		byName:    map[string]provider.Provider{},
		pairCache: cache.New[[]provider.Pair](ttl, nil),
		tmpl:      template.Must(template.ParseFS(assets, "templates/*.html")),
		mux:       http.NewServeMux(),
	}
	for _, p := range providers {
		s.byName[p.Name()] = p
	}

	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("GET /api/quotes", s.handleQuotes)
	s.mux.HandleFunc("POST /swap", s.handleCreateSwap)
	s.mux.HandleFunc("GET /swap/{provider}/{id}", s.handleStatusPage)
	s.mux.HandleFunc("GET /api/swap/{provider}/{id}", s.handleStatusJSON)
	s.mux.Handle("GET /static/", http.FileServerFS(assets))
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) pairs(ctx context.Context, p provider.Provider) ([]provider.Pair, error) {
	if pairs, ok := s.pairCache.Get(p.Name()); ok {
		return pairs, nil
	}
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	pairs, err := p.Pairs(ctx)
	if err != nil {
		return nil, err
	}
	s.pairCache.Set(p.Name(), pairs)
	return pairs, nil
}
