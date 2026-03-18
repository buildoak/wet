package cli

import (
	"strings"
	"testing"

	"github.com/buildoak/wet/config"
)

func TestParseServeArgsDefaultsFromEnv(t *testing.T) {
	t.Setenv("WET_HOST", "0.0.0.0")
	t.Setenv("WET_PORT", "9100")
	t.Setenv("WET_MODE", "auto")
	t.Setenv("WET_UPSTREAM", "https://example.com")
	t.Setenv("WET_SESSION_UUID", "serve-session")
	t.Setenv("WET_RESUME", "true")

	opts, err := parseServeArgs(nil)
	if err != nil {
		t.Fatalf("parseServeArgs() error = %v", err)
	}

	if opts.Host != "0.0.0.0" {
		t.Fatalf("Host = %q, want 0.0.0.0", opts.Host)
	}
	if opts.Port != 9100 {
		t.Fatalf("Port = %d, want 9100", opts.Port)
	}
	if opts.Mode != "auto" {
		t.Fatalf("Mode = %q, want auto", opts.Mode)
	}
	if opts.Upstream != "https://example.com" {
		t.Fatalf("Upstream = %q, want https://example.com", opts.Upstream)
	}
	if opts.SessionID != "serve-session" {
		t.Fatalf("SessionID = %q, want serve-session", opts.SessionID)
	}
	if !opts.Resume {
		t.Fatal("Resume = false, want true")
	}
}

func TestParseServeArgsFlagsOverrideEnv(t *testing.T) {
	t.Setenv("WET_HOST", "127.0.0.1")
	t.Setenv("WET_PORT", "8100")
	t.Setenv("WET_MODE", "passthrough")
	t.Setenv("WET_UPSTREAM", "https://old.example.com")
	t.Setenv("WET_SESSION_UUID", "old-session")
	t.Setenv("WET_RESUME", "false")

	opts, err := parseServeArgs([]string{
		"--host", "0.0.0.0",
		"--port", "9200",
		"--mode", "auto",
		"--upstream", "https://new.example.com",
		"--session-id", "new-session",
		"--resume",
	})
	if err != nil {
		t.Fatalf("parseServeArgs() error = %v", err)
	}

	if opts.Host != "0.0.0.0" {
		t.Fatalf("Host = %q, want 0.0.0.0", opts.Host)
	}
	if opts.Port != 9200 {
		t.Fatalf("Port = %d, want 9200", opts.Port)
	}
	if opts.Mode != "auto" {
		t.Fatalf("Mode = %q, want auto", opts.Mode)
	}
	if opts.Upstream != "https://new.example.com" {
		t.Fatalf("Upstream = %q, want https://new.example.com", opts.Upstream)
	}
	if opts.SessionID != "new-session" {
		t.Fatalf("SessionID = %q, want new-session", opts.SessionID)
	}
	if !opts.Resume {
		t.Fatal("Resume = false, want true")
	}
}

func TestParseServeArgsErrors(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		args    []string
		wantErr string
	}{
		{
			name:    "invalid env port",
			env:     map[string]string{"WET_PORT": "nope"},
			wantErr: "invalid WET_PORT",
		},
		{
			name:    "invalid env resume",
			env:     map[string]string{"WET_RESUME": "maybe"},
			wantErr: "invalid WET_RESUME",
		},
		{
			name:    "invalid mode",
			args:    []string{"--mode", "bogus"},
			wantErr: "invalid mode",
		},
		{
			name:    "missing host value",
			args:    []string{"--host"},
			wantErr: "--host requires a value",
		},
		{
			name:    "missing session id",
			args:    []string{"--session-id"},
			wantErr: "--session-id requires a value",
		},
		{
			name:    "unknown flag",
			args:    []string{"--bogus"},
			wantErr: "unknown flag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WET_HOST", "")
			t.Setenv("WET_PORT", "")
			t.Setenv("WET_MODE", "")
			t.Setenv("WET_UPSTREAM", "")
			t.Setenv("WET_SESSION_UUID", "")
			t.Setenv("WET_RESUME", "")
			for key, value := range tt.env {
				t.Setenv(key, value)
			}

			_, err := parseServeArgs(tt.args)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if got := err.Error(); got == "" || !strings.Contains(got, tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", got, tt.wantErr)
			}
		})
	}
}

func TestApplyServeOptions(t *testing.T) {
	cfg := config.Default()
	applyServeOptions(cfg, &serveOptions{
		Host:     "0.0.0.0",
		Port:     9300,
		Mode:     "auto",
		Upstream: "https://proxy.example.com",
	})

	if cfg.Server.Host != "0.0.0.0" {
		t.Fatalf("Host = %q, want 0.0.0.0", cfg.Server.Host)
	}
	if cfg.Server.Port != 9300 {
		t.Fatalf("Port = %d, want 9300", cfg.Server.Port)
	}
	if cfg.Server.Mode != "auto" {
		t.Fatalf("Mode = %q, want auto", cfg.Server.Mode)
	}
	if cfg.Server.Upstream != "https://proxy.example.com" {
		t.Fatalf("Upstream = %q, want https://proxy.example.com", cfg.Server.Upstream)
	}
}
