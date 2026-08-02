package provider

import (
	"fmt"

	"omenswap.com/swap-aggregator/internal/config"
)

type Factory func(cfg config.Provider) (Provider, error)

var factories = map[string]Factory{}

func Register(typ string, f Factory) {
	if _, ok := factories[typ]; ok {
		panic(fmt.Sprintf("provider type %q already registered", typ))
	}
	factories[typ] = f
}

func Build(cfgs []config.Provider) ([]Provider, error) {
	var ps []Provider
	for _, cfg := range cfgs {
		if !cfg.Enabled {
			continue
		}
		f, ok := factories[cfg.Type]
		if !ok {
			return nil, fmt.Errorf("provider %q: unknown type %q", cfg.Name, cfg.Type)
		}
		p, err := f(cfg)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", cfg.Name, err)
		}
		ps = append(ps, p)
	}
	return ps, nil
}
