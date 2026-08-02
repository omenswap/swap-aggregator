package server

import (
	"strings"
	"testing"
)

func TestQRSVG(t *testing.T) {
	svg, err := qrSVG("bc1q4r04hxptugjtl6cmvkggz5t0f7plqj4e7napft")
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if !strings.HasPrefix(s, "<svg ") || !strings.Contains(s, "viewBox=") {
		t.Fatalf("not an svg: %s", s[:min(80, len(s))])
	}
	if !strings.Contains(s, "<rect") {
		t.Error("svg has no modules")
	}
}

func TestQRSVGEscapesNothingExecutable(t *testing.T) {
	svg, err := qrSVG(`"><script>alert(1)</script>`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(svg), "<script>") {
		t.Errorf("svg contains raw input: %s", svg)
	}
}

func TestQRSVGEmptyAddress(t *testing.T) {
	if _, err := qrSVG(""); err == nil {
		t.Error("expected an error for an empty address")
	}
}
