package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"omenswap.com/swap-aggregator/internal/provider"
)

func TestThemeCSSRoute(t *testing.T) {
	s := New(nil, time.Minute)
	s.SetThemeCSS(":root {\n  --bg: #101010;\n}\n")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/theme.css", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("content-type %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "--bg: #101010;") {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestThemeCSSRouteEmptyByDefault(t *testing.T) {
	s := New(nil, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/theme.css", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("status %d body %q", rec.Code, rec.Body.String())
	}
}

func TestIndexLinksThemeCSS(t *testing.T) {
	f := &fakeProvider{name: "one", pairs: []provider.Pair{ethPair}}
	s := New([]provider.Provider{f}, time.Minute)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `href="/theme.css"`) {
		t.Errorf("index missing theme link: %s", body)
	}
	if strings.Index(body, "/static/style.css") > strings.Index(body, "/theme.css") {
		t.Error("theme.css must come after style.css to override it")
	}
}
