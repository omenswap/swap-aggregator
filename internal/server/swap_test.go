package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"omenswap.com/swap-aggregator/internal/provider"
)

func postSwap(s *Server, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/swap", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func validForm() url.Values {
	return url.Values{
		"provider":            {"one"},
		"from":                {"ETH"},
		"to":                  {"USDC"},
		"amount":              {"1"},
		"destination_address": {"0xdest"},
	}
}

func TestCreateSwapRedirects(t *testing.T) {
	f := &fakeProvider{name: "one", pairs: []provider.Pair{ethPair},
		create: func(r provider.SwapRequest) (provider.Swap, error) {
			if r.From != "ETH" || r.Amount != "1" || r.DestinationAddress != "0xdest" {
				t.Errorf("req = %+v", r)
			}
			return provider.Swap{ID: "abc-123", Status: "pending"}, nil
		}}
	s := New([]provider.Provider{f}, time.Minute)
	rec := postSwap(s, validForm())
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/swap/one/abc-123" {
		t.Errorf("Location = %q", loc)
	}
}

func TestCreateSwapUnknownProvider(t *testing.T) {
	s := New([]provider.Provider{&fakeProvider{name: "one"}}, time.Minute)
	form := validForm()
	form.Set("provider", "nope")
	if rec := postSwap(s, form); rec.Code != http.StatusBadRequest {
		t.Errorf("status %d", rec.Code)
	}
}

func TestCreateSwapMissingDestination(t *testing.T) {
	s := New([]provider.Provider{&fakeProvider{name: "one"}}, time.Minute)
	form := validForm()
	form.Del("destination_address")
	if rec := postSwap(s, form); rec.Code != http.StatusBadRequest {
		t.Errorf("status %d", rec.Code)
	}
}

func TestCreateSwapRejectsUnbrokered(t *testing.T) {
	g := &gatedFake{
		fakeProvider: fakeProvider{name: "one", pairs: []provider.Pair{ethPair}},
		brokered:     false,
	}
	s := New([]provider.Provider{g}, time.Minute)
	rec := postSwap(s, validForm())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "api key") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func TestCreateSwapProviderError(t *testing.T) {
	f := &fakeProvider{name: "one", pairs: []provider.Pair{ethPair},
		create: func(provider.SwapRequest) (provider.Swap, error) {
			return provider.Swap{}, errors.New("omenswap: insufficient reserves")
		}}
	s := New([]provider.Provider{f}, time.Minute)
	rec := postSwap(s, validForm())
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status %d", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Path != "/" || loc.Query().Get("error") != "omenswap: insufficient reserves" {
		t.Errorf("Location = %q", rec.Header().Get("Location"))
	}
}

func TestIndexShowsErrorFromQuery(t *testing.T) {
	s := New([]provider.Provider{&fakeProvider{name: "one", pairs: []provider.Pair{ethPair}}}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/?error=boom+happened", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "boom happened") {
		t.Errorf("body missing error: %s", rec.Body.String())
	}
}

func TestCreateSwapErrorKeepsFormValues(t *testing.T) {
	f := &fakeProvider{name: "one", pairs: []provider.Pair{ethPair},
		create: func(provider.SwapRequest) (provider.Swap, error) {
			return provider.Swap{}, errors.New("invalid address")
		}}
	s := New([]provider.Provider{f}, time.Minute)
	form := validForm()
	form.Set("refund_address", "0xrefund")
	rec := postSwap(s, form)
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := loc.Query()
	for key, want := range map[string]string{"amount": "1", "from": "ETH", "to": "USDC",
		"destination_address": "0xdest", "refund_address": "0xrefund", "provider": "one"} {
		if q.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, q.Get(key), want)
		}
	}
}

func TestIndexPrefillsFormFromQuery(t *testing.T) {
	s := New([]provider.Provider{&fakeProvider{name: "one", pairs: []provider.Pair{ethPair}}}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET",
		"/?error=nope&amount=2.5&from=USDC&to=ETH&destination_address=0xdest&refund_address=0xback", nil))
	body := rec.Body.String()
	for _, want := range []string{`value="2.5"`, `value="0xdest"`, `value="0xback"`,
		`<option value="USDC" selected>`, `<option value="ETH" selected>`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestIndexPrefillIsEscaped(t *testing.T) {
	s := New([]provider.Provider{&fakeProvider{name: "one", pairs: []provider.Pair{ethPair}}}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET",
		`/?destination_address=%22%3E%3Cscript%3Ealert(1)%3C%2Fscript%3E`, nil))
	if strings.Contains(rec.Body.String(), "<script>alert(1)") {
		t.Errorf("unescaped prefill:\n%s", rec.Body.String())
	}
}
