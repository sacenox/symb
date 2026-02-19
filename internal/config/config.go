// Package config handles configuration loading from TOML files and environment variables.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the root configuration structure.
type Config struct {
	DefaultProvider string                    `toml:"default_provider"`
	Providers       map[string]ProviderConfig `toml:"providers"`
	MCP             MCPConfig                 `toml:"mcp"`
	Cache           CacheConfig               `toml:"cache"`
	UI              UIConfig                  `toml:"ui"`
}

// UIConfig holds user-interface settings.
type UIConfig struct {
	// SyntaxTheme is the Chroma syntax highlighting theme used across the TUI.
	// UI chrome colors are derived from this theme via highlight.ThemePalette.
	// Defaults to "vulcan" if unset.
	SyntaxTheme string `toml:"syntax_theme"`
}

// SyntaxThemeOrDefault returns the configured syntax theme or "vulcan" if unset.
func (u UIConfig) SyntaxThemeOrDefault() string {
	if u.SyntaxTheme == "" {
		return "vulcan"
	}
	return u.SyntaxTheme
}

// CacheConfig holds web cache settings.
type CacheConfig struct {
	TTLHours int `toml:"ttl_hours"`
}

// CacheTTLOrDefault returns the configured TTL or 24 hours if unset.
func (c CacheConfig) CacheTTLOrDefault() int {
	if c.TTLHours <= 0 {
		return 24
	}
	return c.TTLHours
}

// ProviderConfig holds LLM provider settings.
type ProviderConfig struct {
	Endpoint    string  `toml:"endpoint"`
	Model       string  `toml:"model"`
	Temperature float64 `toml:"temperature"`
}

// MCPConfig holds MCP proxy settings.
type MCPConfig struct {
	Upstream string `toml:"upstream"`
}

// Load reads configuration from a TOML file and applies environment variable overrides.
func Load(path string) (*Config, error) {
	cfg := &Config{
		Providers: make(map[string]ProviderConfig),
	}

	// Config file is required
	if path == "" {
		return nil, fmt.Errorf("config path is required")
	}

	// File must exist
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("config file not found: %s", path)
	}

	// Load from file
	_, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Apply environment variable overrides
	applyEnvOverrides(cfg)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate returns an error if the configuration is invalid.
func (c *Config) Validate() error {
	var errs []error

	if len(c.Providers) == 0 {
		errs = append(errs, errors.New("providers: at least one provider must be configured"))
	} else {
		for name, providerCfg := range c.Providers {
			errs = append(errs, validateProviderConfig(name, providerCfg)...)
		}
	}

	// Validate default provider if specified
	if c.DefaultProvider != "" {
		if _, ok := c.Providers[c.DefaultProvider]; !ok {
			errs = append(errs, fmt.Errorf("default_provider=%q does not exist in providers", c.DefaultProvider))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

func validateProviderConfig(name string, cfg ProviderConfig) []error {
	var errs []error
	if cfg.Endpoint == "" {
		errs = append(errs, fmt.Errorf("providers.%s.endpoint is required", name))
	} else if err := validateEndpoint(cfg.Endpoint); err != nil {
		errs = append(errs, fmt.Errorf("providers.%s.endpoint=%q is invalid: %v", name, cfg.Endpoint, err))
	}

	if cfg.Model == "" {
		errs = append(errs, fmt.Errorf("providers.%s.model is required", name))
	}

	if cfg.Temperature < 0.0 || cfg.Temperature > 2.0 {
		errs = append(errs, fmt.Errorf("providers.%s.temperature=%v must be between 0.0 and 2.0", name, cfg.Temperature))
	}

	return errs
}

func validateEndpoint(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("missing scheme or host")
	}
	return nil
}

// applyEnvOverrides applies environment variable overrides to the configuration.
func applyEnvOverrides(cfg *Config) {
	for _, setter := range []struct {
		env   string
		apply func(string)
	}{
		{"SYMB_MCP_ENDPOINT", func(v string) {
			if v != "" {
				cfg.MCP.Upstream = v
			}
		}},
	} {
		setter.apply(os.Getenv(setter.env))
	}
}

// DataDir returns the path to the Symb data directory (~/.config/symb).
func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "symb"), nil
}

// EnsureDataDir creates the data directory if it doesn't exist.
func EnsureDataDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", err
	}
	return dir, nil
}
