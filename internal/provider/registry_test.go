package provider

import (
	"context"
	"strings"
	"testing"

	"omenswap.com/swap-aggregator/internal/config"
)

type fake struct{ name string }

func (f *fake) Name() string                                             { return f.name }
func (f *fake) Pairs(ctx context.Context) ([]Pair, error)                { return nil, nil }
func (f *fake) Quote(ctx context.Context, r QuoteRequest) (Quote, error) { return Quote{}, nil }
func (f *fake) CreateSwap(ctx context.Context, r SwapRequest) (Swap, error) {
	return Swap{}, nil
}
func (f *fake) Status(ctx context.Context, id string) (Swap, error) { return Swap{}, nil }

func TestBuild(t *testing.T) {
	Register("fake", func(cfg config.Provider) (Provider, error) {
		return &fake{name: cfg.Name}, nil
	})
	cfgs := []config.Provider{
		{Name: "a", Type: "fake", Enabled: true},
		{Name: "b", Type: "fake", Enabled: false},
		{Name: "c", Type: "fake", Enabled: true},
	}
	ps, err := Build(cfgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].Name() != "a" || ps[1].Name() != "c" {
		t.Errorf("got %d providers", len(ps))
	}
}

func TestBuildUnknownType(t *testing.T) {
	_, err := Build([]config.Provider{{Name: "x", Type: "nope", Enabled: true}})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("err = %v", err)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic")
		}
	}()
	Register("dup", func(config.Provider) (Provider, error) { return nil, nil })
	Register("dup", func(config.Provider) (Provider, error) { return nil, nil })
}
