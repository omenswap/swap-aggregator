package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValid(t *testing.T) {
	p := writeTemp(t, `
listen = ":9999"
cache_ttl = "10s"

[[providers]]
name = "omenswap"
type = "omenswap"
url = "http://localhost:8080"
enabled = true
affiliate_code = "abc"
api_key = "k"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9999" {
		t.Errorf("Listen = %q", c.Listen)
	}
	if c.TTL() != 10*time.Second {
		t.Errorf("TTL = %v", c.TTL())
	}
	pr := c.Providers[0]
	if pr.Name != "omenswap" || pr.Type != "omenswap" || pr.URL != "http://localhost:8080" ||
		!pr.Enabled || pr.AffiliateCode != "abc" || pr.APIKey != "k" {
		t.Errorf("provider = %+v", pr)
	}
}

func TestLoadDefaults(t *testing.T) {
	p := writeTemp(t, `
[[providers]]
name = "omenswap"
type = "omenswap"
url = "http://localhost:8080"
enabled = true
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8000" {
		t.Errorf("Listen = %q", c.Listen)
	}
	if c.TTL() != 30*time.Second {
		t.Errorf("TTL = %v", c.TTL())
	}
}

func TestLoadNoEnabledProviders(t *testing.T) {
	p := writeTemp(t, `
[[providers]]
name = "omenswap"
type = "omenswap"
url = "http://localhost:8080"
enabled = false
`)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "enabled") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadDuplicateNames(t *testing.T) {
	p := writeTemp(t, `
[[providers]]
name = "omenswap"
type = "omenswap"
url = "http://localhost:8080"
enabled = true

[[providers]]
name = "omenswap"
type = "omenswap"
url = "http://localhost:8081"
enabled = true
`)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadBadURL(t *testing.T) {
	for _, u := range []string{"", "localhost:8080", "ftp://x"} {
		p := writeTemp(t, `
[[providers]]
name = "omenswap"
type = "omenswap"
url = "`+u+`"
enabled = true
`)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "url") {
			t.Errorf("url %q: err = %v", u, err)
		}
	}
}

func TestLoadBadName(t *testing.T) {
	p := writeTemp(t, `
[[providers]]
name = "Omen Swap!"
type = "omenswap"
url = "http://localhost:8080"
enabled = true
`)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("err = %v", err)
	}
}

const themeProvider = `
[[providers]]
name = "omenswap"
type = "omenswap"
url = "http://localhost:8080"
enabled = true
`

func TestThemeCSS(t *testing.T) {
	p := writeTemp(t, themeProvider+`
[theme]
background = "#101010"
text = "#eeeeee"
border = "#444"
font = "Inter, sans-serif"
color_scheme = "light"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	css := c.Theme.CSS()
	for _, want := range []string{"--bg: #101010;", "--ink: #eeeeee;", "--line: #444;",
		"--mono: Inter, sans-serif;", "color-scheme: light;"} {
		if !strings.Contains(css, want) {
			t.Errorf("css missing %q:\n%s", want, css)
		}
	}
	if strings.Contains(css, "--muted") {
		t.Errorf("unset key should be omitted:\n%s", css)
	}
}

func TestThemeEmptyCSS(t *testing.T) {
	c, err := Load(writeTemp(t, themeProvider))
	if err != nil {
		t.Fatal(err)
	}
	if css := c.Theme.CSS(); css != "" {
		t.Errorf("css = %q", css)
	}
}

func TestThemeRejectsInvalidColor(t *testing.T) {
	for _, bad := range []string{`background = "red; } body { display: none"`, `background = "url(x)"`, `text = "rgb(1,2,3)"`} {
		if _, err := Load(writeTemp(t, themeProvider+"\n[theme]\n"+bad)); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestThemeRejectsInvalidFontAndScheme(t *testing.T) {
	if _, err := Load(writeTemp(t, themeProvider+"\n[theme]\nfont = \"Inter; } html {\"")); err == nil {
		t.Error("expected font error")
	}
	if _, err := Load(writeTemp(t, themeProvider+"\n[theme]\ncolor_scheme = \"neon\"")); err == nil {
		t.Error("expected color_scheme error")
	}
}

func TestRateLimitDefaults(t *testing.T) {
	c, err := Load(writeTemp(t, themeProvider))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Providers[0].RateLimit; got != 0 {
		t.Errorf("RateLimit = %v, want unlimited", got)
	}
	if got := c.Providers[0].Burst(); got != 1 {
		t.Errorf("Burst = %v", got)
	}
}

func TestRateLimitParsed(t *testing.T) {
	c, err := Load(writeTemp(t, `
[[providers]]
name = "one"
type = "omenswap"
url = "http://localhost:8080"
enabled = true
rate_limit = 2.5
rate_burst = 4
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers[0].RateLimit != 2.5 || c.Providers[0].Burst() != 4 {
		t.Errorf("provider = %+v", c.Providers[0])
	}
}

func TestRateLimitRejectsNegative(t *testing.T) {
	_, err := Load(writeTemp(t, `
[[providers]]
name = "one"
type = "omenswap"
url = "http://localhost:8080"
enabled = true
rate_limit = -1
`))
	if err == nil {
		t.Error("expected an error for a negative rate limit")
	}
}

func TestAffiliateFeePercent(t *testing.T) {
	c, err := Load(writeTemp(t, `
[[providers]]
name = "one"
type = "omenswap"
url = "http://localhost:8080"
enabled = true
affiliate_code = "abc"
affiliate_fee_percent = 0.6
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers[0].AffiliateFeePercent != 0.6 {
		t.Errorf("provider = %+v", c.Providers[0])
	}
}

func TestAffiliateFeePercentRejectsOutOfRange(t *testing.T) {
	for _, bad := range []string{"-1", "25"} {
		_, err := Load(writeTemp(t, `
[[providers]]
name = "one"
type = "omenswap"
url = "http://localhost:8080"
enabled = true
affiliate_fee_percent = `+bad+`
`))
		if err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
}
