package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type duration struct {
	time.Duration
}

func (d *duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

type Provider struct {
	Name          string `toml:"name"`
	Type          string `toml:"type"`
	URL           string `toml:"url"`
	Enabled       bool   `toml:"enabled"`
	AffiliateCode string `toml:"affiliate_code"`
	APIKey        string `toml:"api_key"`
	// RateLimit caps outbound requests per second; zero means no limit.
	RateLimit float64 `toml:"rate_limit"`
	RateBurst float64 `toml:"rate_burst"`
}

func (p Provider) Burst() float64 {
	if p.RateBurst < 1 {
		return 1
	}
	return p.RateBurst
}

type Theme struct {
	Background  string `toml:"background"`
	Text        string `toml:"text"`
	Muted       string `toml:"muted"`
	Faint       string `toml:"faint"`
	Border      string `toml:"border"`
	BorderHover string `toml:"border_hover"`
	RowHover    string `toml:"row_hover"`
	Font        string `toml:"font"`
	ColorScheme string `toml:"color_scheme"`
}

type Config struct {
	Listen    string     `toml:"listen"`
	CacheTTL  duration   `toml:"cache_ttl"`
	Theme     Theme      `toml:"theme"`
	Providers []Provider `toml:"providers"`
}

func (t Theme) vars() [][2]string {
	return [][2]string{
		{"--bg", t.Background},
		{"--ink", t.Text},
		{"--muted", t.Muted},
		{"--faint", t.Faint},
		{"--line", t.Border},
		{"--line-hi", t.BorderHover},
		{"--hover", t.RowHover},
		{"--mono", t.Font},
		{"color-scheme", t.ColorScheme},
	}
}

func (t Theme) CSS() string {
	var b strings.Builder
	for _, v := range t.vars() {
		if v[1] != "" {
			fmt.Fprintf(&b, "  %s: %s;\n", v[0], v[1])
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return ":root {\n" + b.String() + "}\n"
}

var (
	colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$`)
	fontRe  = regexp.MustCompile(`^[A-Za-z0-9 ,._"'-]+$`)
)

func (t Theme) validate() error {
	colors := map[string]string{"background": t.Background, "text": t.Text, "muted": t.Muted,
		"faint": t.Faint, "border": t.Border, "border_hover": t.BorderHover, "row_hover": t.RowHover}
	for key, v := range colors {
		if v != "" && !colorRe.MatchString(v) {
			return fmt.Errorf("theme %s: %q must be a hex color like #1a1a1a", key, v)
		}
	}
	if t.Font != "" && !fontRe.MatchString(t.Font) {
		return fmt.Errorf("theme font: %q has unsupported characters", t.Font)
	}
	switch t.ColorScheme {
	case "", "dark", "light":
	default:
		return fmt.Errorf("theme color_scheme: %q must be dark or light", t.ColorScheme)
	}
	return nil
}

func (c *Config) TTL() time.Duration {
	return c.CacheTTL.Duration
}

var nameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

func Load(path string) (*Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return nil, err
	}
	if c.Listen == "" {
		c.Listen = ":8000"
	}
	if c.CacheTTL.Duration == 0 {
		c.CacheTTL.Duration = 30 * time.Second
	}

	if err := c.Theme.validate(); err != nil {
		return nil, err
	}

	enabled := 0
	seen := map[string]bool{}
	for _, p := range c.Providers {
		if !nameRe.MatchString(p.Name) {
			return nil, fmt.Errorf("provider name %q: must match [a-z0-9-]+", p.Name)
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("duplicate provider name %q", p.Name)
		}
		seen[p.Name] = true
		u, err := url.Parse(p.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("provider %q: invalid url %q", p.Name, p.URL)
		}
		if p.RateLimit < 0 || p.RateBurst < 0 {
			return nil, fmt.Errorf("provider %q: rate_limit and rate_burst must not be negative", p.Name)
		}
		if p.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return nil, fmt.Errorf("no enabled providers configured")
	}
	return &c, nil
}
