package provider

import "testing"

type plainProvider struct{ Provider }

type fixedProvider struct{ Provider }

func (fixedProvider) SupportsRateMode(t RateType, d Direction) bool {
	return d == ToSide || t == Floating
}

func TestSupportsRateModeDefault(t *testing.T) {
	p := plainProvider{}
	if !SupportsRateMode(p, Floating, FromSide) {
		t.Error("floating from-side must be the default capability")
	}
	if SupportsRateMode(p, Fixed, ToSide) || SupportsRateMode(p, Fixed, FromSide) {
		t.Error("a provider that says nothing must not claim fixed rates")
	}
}

func TestSupportsRateModeOverride(t *testing.T) {
	p := fixedProvider{}
	if !SupportsRateMode(p, Fixed, ToSide) {
		t.Error("declared capability ignored")
	}
	if SupportsRateMode(p, Fixed, FromSide) {
		t.Error("undeclared capability reported as supported")
	}
}
