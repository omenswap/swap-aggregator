package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"omenswap.com/swap-aggregator/internal/config"
	"omenswap.com/swap-aggregator/internal/provider"
	_ "omenswap.com/swap-aggregator/internal/provider/bitcoinvn"
	_ "omenswap.com/swap-aggregator/internal/provider/etzswap"
	_ "omenswap.com/swap-aggregator/internal/provider/fixedfloat"
	_ "omenswap.com/swap-aggregator/internal/provider/omenswap"
	_ "omenswap.com/swap-aggregator/internal/provider/pegasusswap"
	_ "omenswap.com/swap-aggregator/internal/provider/swapuz"
	_ "omenswap.com/swap-aggregator/internal/provider/wizardswap"
	"omenswap.com/swap-aggregator/internal/server"
)

func main() {
	configPath := flag.String("config", "config.toml", "path to config file")
	listen := flag.String("listen", "", "listen address (overrides config)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}

	providers, err := provider.Build(cfg.Providers)
	if err != nil {
		log.Fatalf("providers: %v", err)
	}
	var names []string
	for _, p := range providers {
		names = append(names, p.Name())
	}

	s := server.New(providers, cfg.TTL())
	s.SetThemeCSS(cfg.Theme.CSS())
	s.SetSiteName(cfg.SiteName)

	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: s.Handler(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("listening on %s (providers: %s)", cfg.Listen, strings.Join(names, ", "))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}
