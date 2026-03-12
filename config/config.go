package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server      ServerConfig
	Staleness   StalenessConfig
	Compression CompressionConfig
	Rules       map[string]RuleConfig
	Bypass      BypassConfig
}

type ServerConfig struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	Upstream string `toml:"upstream"`
	Mode     string `toml:"mode"` // "auto" | "passthrough"
}

type StalenessConfig struct {
	Threshold   int `toml:"threshold"`
	TokenBudget int `toml:"token_budget"`
}

type CompressionConfig struct {
	MinTokens int         `toml:"min_tokens"`
	Tier1     Tier1Config `toml:"tier1"`
	Tier2     Tier2Config `toml:"tier2"`
}

type Tier1Config struct {
	Enabled bool `toml:"enabled"`
}

type Tier2Config struct {
	Enabled   bool   `toml:"enabled"`
	Model     string `toml:"model"`
	MinTokens int    `toml:"min_tokens"`
	TimeoutMs int    `toml:"timeout_ms"`
}

type RuleConfig struct {
	Strategy   string `toml:"strategy"`
	StaleAfter int    `toml:"stale_after"`
	Keep       string `toml:"keep"`
}

type BypassConfig struct {
	PreserveErrors  bool     `toml:"preserve_errors"`
	MinTokens       int      `toml:"min_tokens"`
	ContentPatterns []string `toml:"content_patterns"`
}

func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Host:     "127.0.0.1",
			Port:     8100,
			Upstream: "https://api.anthropic.com",
			Mode:     "passthrough",
		},
		Staleness: StalenessConfig{
			Threshold:   2,
			TokenBudget: 0,
		},
		Compression: CompressionConfig{
			MinTokens: 100,
			Tier1: Tier1Config{
				Enabled: true,
			},
			Tier2: Tier2Config{
				Enabled:   false,
				Model:     "claude-haiku-3",
				MinTokens: 500,
				TimeoutMs: 2000,
			},
		},
		Rules: map[string]RuleConfig{},
		Bypass: BypassConfig{
			PreserveErrors: true,
			MinTokens:      100,
			ContentPatterns: []string{
				"error",
				"exception",
				"traceback",
				"failed",
			},
		},
	}
}

func Load(path string) *Config {
	cfg := Default()

	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return cfg
		}
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "[wet] failed to decode config %s: %v\n", path, err)
			return Default()
		}
		if cfg.Rules == nil {
			cfg.Rules = map[string]RuleConfig{}
		}
		return cfg
	}

	for _, candidate := range candidatePaths() {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		if _, err := toml.DecodeFile(candidate, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "[wet] failed to decode config %s: %v\n", candidate, err)
			return Default()
		}
		if cfg.Rules == nil {
			cfg.Rules = map[string]RuleConfig{}
		}
		return cfg
	}

	return cfg
}

func candidatePaths() []string {
	paths := []string{"./wet.toml"}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".wet", "wet.toml"))
	}
	return paths
}
