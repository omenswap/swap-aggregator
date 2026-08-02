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
