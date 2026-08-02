package config

import (
	"fmt"
	"net/url"
	"regexp"
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
}

type Config struct {
	Listen    string     `toml:"listen"`
	CacheTTL  duration   `toml:"cache_ttl"`
	Providers []Provider `toml:"providers"`
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
		if p.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return nil, fmt.Errorf("no enabled providers configured")
	}
	return &c, nil
}
