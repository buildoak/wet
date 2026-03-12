package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := Default()

	if cfg.Server.Host != "127.0.0.1" {
		t.Fatalf("unexpected host: %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 8100 {
		t.Fatalf("unexpected port: %d", cfg.Server.Port)
	}
	if cfg.Server.Upstream != "https://api.anthropic.com" {
		t.Fatalf("unexpected upstream: %s", cfg.Server.Upstream)
	}
	if cfg.Staleness.Threshold != 2 {
		t.Fatalf("unexpected staleness threshold: %d", cfg.Staleness.Threshold)
	}
	if cfg.Compression.MinTokens != 100 {
		t.Fatalf("unexpected compression min_tokens: %d", cfg.Compression.MinTokens)
	}
	if !cfg.Compression.Tier1.Enabled {
		t.Fatal("tier1 should be enabled by default")
	}
	if cfg.Compression.Tier2.Enabled {
		t.Fatal("tier2 should be disabled by default")
	}
	if cfg.Compression.Tier2.Model != "claude-haiku-3" {
		t.Fatalf("unexpected tier2 model: %s", cfg.Compression.Tier2.Model)
	}
	if !cfg.Bypass.PreserveErrors {
		t.Fatal("preserve_errors should be true by default")
	}
	if cfg.Bypass.MinTokens != 100 {
		t.Fatalf("unexpected bypass min_tokens: %d", cfg.Bypass.MinTokens)
	}
	if len(cfg.Bypass.ContentPatterns) == 0 {
		t.Fatal("expected default bypass content patterns")
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wet.toml")

	content := `
[server]
host = "0.0.0.0"
port = 9001
upstream = "https://example.com"

[staleness]
threshold = 5
token_budget = 1200

[compression]
min_tokens = 150

[compression.tier1]
enabled = false

[compression.tier2]
enabled = true
model = "claude-sonnet-4"
min_tokens = 700
timeout_ms = 5000

[bypass]
preserve_errors = false
min_tokens = 222
content_patterns = ["fatal", "panic"]

[rules.chat]
strategy = "tier2"
stale_after = 10
keep = "bullets"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg := Load(path)
	if cfg.Server.Host != "0.0.0.0" || cfg.Server.Port != 9001 || cfg.Server.Upstream != "https://example.com" {
		t.Fatalf("unexpected server config: %+v", cfg.Server)
	}
	if cfg.Staleness.Threshold != 5 || cfg.Staleness.TokenBudget != 1200 {
		t.Fatalf("unexpected staleness config: %+v", cfg.Staleness)
	}
	if cfg.Compression.MinTokens != 150 || cfg.Compression.Tier1.Enabled {
		t.Fatalf("unexpected compression tier1 config: %+v", cfg.Compression)
	}
	if !cfg.Compression.Tier2.Enabled || cfg.Compression.Tier2.Model != "claude-sonnet-4" || cfg.Compression.Tier2.MinTokens != 700 || cfg.Compression.Tier2.TimeoutMs != 5000 {
		t.Fatalf("unexpected compression tier2 config: %+v", cfg.Compression.Tier2)
	}
	if cfg.Bypass.PreserveErrors || cfg.Bypass.MinTokens != 222 || len(cfg.Bypass.ContentPatterns) != 2 {
		t.Fatalf("unexpected bypass config: %+v", cfg.Bypass)
	}
	rule, ok := cfg.Rules["chat"]
	if !ok {
		t.Fatal("expected rules.chat entry")
	}
	if rule.Strategy != "tier2" || rule.StaleAfter != 10 || rule.Keep != "bullets" {
		t.Fatalf("unexpected chat rule: %+v", rule)
	}
}

func TestLoadFallback(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(work)

	cfg := Load("")
	def := Default()

	if cfg.Server != def.Server {
		t.Fatalf("expected default server config, got %+v", cfg.Server)
	}
	if cfg.Staleness != def.Staleness {
		t.Fatalf("expected default staleness config, got %+v", cfg.Staleness)
	}
	if cfg.Compression != def.Compression {
		t.Fatalf("expected default compression config, got %+v", cfg.Compression)
	}
	if cfg.Bypass.PreserveErrors != def.Bypass.PreserveErrors || cfg.Bypass.MinTokens != def.Bypass.MinTokens {
		t.Fatalf("expected default bypass config, got %+v", cfg.Bypass)
	}
}
