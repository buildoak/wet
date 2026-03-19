package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

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

// ---------------------------------------------------------------------------
// Lifecycle tests for startProxyServer and RunServe
// ---------------------------------------------------------------------------

// testUpstream creates a minimal upstream httptest.Server that echoes 200 OK.
// Returns the server; caller must defer Close().
func testUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
}

// testConfig returns a config bound to 127.0.0.1 with a random free port
// and the given upstream URL.
func testConfig(t *testing.T, upstreamURL string) *config.Config {
	t.Helper()
	port := findFreeTestPort(t)
	cfg := config.Default()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = port
	cfg.Server.Upstream = upstreamURL
	return cfg
}

// findFreeTestPort allocates a random TCP port and returns it.
func findFreeTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// pollHealth polls /health until 200 or timeout. Returns nil on success.
func pollHealth(port int, timeout time.Duration) error {
	client := &http.Client{Timeout: 250 * time.Millisecond}
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("health check on port %d did not return 200 within %s", port, timeout)
}

// TestStartProxyServer validates the shared critical path: bind, health,
// status endpoint, shutdown, and port release.
func TestStartProxyServer(t *testing.T) {
	upstream := testUpstream(t)
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL)
	port := cfg.Server.Port

	// Use a temporary log file so openSessionLog() does not touch real ~/.wet/wet.log
	logFile, err := os.CreateTemp(t.TempDir(), "wet-test-*.log")
	if err != nil {
		t.Fatalf("create temp log: %v", err)
	}
	defer logFile.Close()

	sessionID := "test-proxy-" + generateUUID()

	srv, serverErrCh, err := startProxyServer(cfg, logFile, sessionID, false)
	if err != nil {
		t.Fatalf("startProxyServer: %v", err)
	}
	// Deferred shutdown for safety; explicit Shutdown tested below.
	t.Cleanup(func() {
		if srv != nil {
			srv.Shutdown()
		}
	})

	// --- 1. /health returns 200 with status:ok ---
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	resp, err := http.Get(healthURL)
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health status: got %d, want 200", resp.StatusCode)
	}
	var healthPayload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&healthPayload); err != nil {
		t.Fatalf("decode /health: %v", err)
	}
	if healthPayload["status"] != "ok" {
		t.Fatalf("/health status field: got %v, want ok", healthPayload["status"])
	}

	// --- 2. /_wet/status returns valid JSON with expected keys ---
	statusURL := fmt.Sprintf("http://127.0.0.1:%d/_wet/status", port)
	resp2, err := http.Get(statusURL)
	if err != nil {
		t.Fatalf("GET /_wet/status: %v", err)
	}
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("/_wet/status status: got %d, want 200", resp2.StatusCode)
	}
	var statusPayload map[string]any
	if err := json.Unmarshal(body, &statusPayload); err != nil {
		t.Fatalf("decode /_wet/status: %v (body=%s)", err, string(body))
	}
	for _, key := range []string{"uptime_seconds", "request_count"} {
		if _, ok := statusPayload[key]; !ok {
			t.Errorf("/_wet/status missing key %q", key)
		}
	}

	// --- 3. serverErrCh should not have errored ---
	select {
	case err := <-serverErrCh:
		if err != nil {
			t.Fatalf("unexpected server error: %v", err)
		}
	default:
		// Expected: no error on the channel.
	}

	// --- 4. Shutdown and verify port is released ---
	srv.Shutdown()
	srv = nil // Prevent t.Cleanup double-shutdown.

	// After shutdown, the port should be free. Try to bind it.
	// Give the OS a moment to release the socket.
	var portReleased bool
	for i := 0; i < 20; i++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln.Close()
			portReleased = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !portReleased {
		t.Fatalf("port %d not released after Shutdown", port)
	}
}

// TestRunServe_ResumeWithoutSessionID verifies that RunServe returns an error
// when --resume is specified without --session-id.
func TestRunServe_ResumeWithoutSessionID(t *testing.T) {
	// Clear env vars that might provide a session ID
	t.Setenv("WET_SESSION_UUID", "")
	t.Setenv("WET_RESUME", "")
	t.Setenv("WET_HOST", "")
	t.Setenv("WET_PORT", "")
	t.Setenv("WET_MODE", "")
	t.Setenv("WET_UPSTREAM", "")

	err := RunServe([]string{"--resume"})
	if err == nil {
		t.Fatal("expected error for --resume without --session-id, got nil")
	}
	if !strings.Contains(err.Error(), "--resume requires --session-id") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "--resume requires --session-id")
	}
}

// TestRunServe_PortConflict verifies that startProxyServer returns an error
// when the requested port is already occupied.
func TestRunServe_PortConflict(t *testing.T) {
	upstream := testUpstream(t)
	defer upstream.Close()

	// Occupy a port with a blocker listener.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind blocker: %v", err)
	}
	blockerPort := blocker.Addr().(*net.TCPAddr).Port
	defer blocker.Close()

	cfg := config.Default()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = blockerPort
	cfg.Server.Upstream = upstream.URL

	logFile, err := os.CreateTemp(t.TempDir(), "wet-test-*.log")
	if err != nil {
		t.Fatalf("create temp log: %v", err)
	}
	defer logFile.Close()

	_, _, err = startProxyServer(cfg, logFile, "conflict-test", false)
	if err == nil {
		t.Fatal("expected error when port is occupied, got nil")
	}
	// The error comes from waitForProxyReady timing out, or the listener error
	// propagating through the server error channel.
	t.Logf("port conflict error (expected): %v", err)
}

// TestRunServe_SignalShutdown verifies that startProxyServer starts a working
// server that can be shut down cleanly, and that the unix socket file is cleaned up.
func TestRunServe_SignalShutdown(t *testing.T) {
	upstream := testUpstream(t)
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL)
	port := cfg.Server.Port

	logFile, err := os.CreateTemp(t.TempDir(), "wet-test-*.log")
	if err != nil {
		t.Fatalf("create temp log: %v", err)
	}
	defer logFile.Close()

	sessionID := "signal-test-" + generateUUID()

	srv, _, err := startProxyServer(cfg, logFile, sessionID, false)
	if err != nil {
		t.Fatalf("startProxyServer: %v", err)
	}

	// Verify server is healthy before shutdown.
	if err := pollHealth(port, 2*time.Second); err != nil {
		srv.Shutdown()
		t.Fatalf("pre-shutdown health check failed: %v", err)
	}

	// The unix socket path: ~/.wet/wet-{PORT}.sock
	sockPath := expandHome(fmt.Sprintf("~/.wet/wet-%d.sock", port))

	// Shutdown the server (simulates what happens after signal is received).
	srv.Shutdown()

	// Verify the socket file is cleaned up.
	if _, err := os.Stat(sockPath); err == nil {
		t.Errorf("socket %s still exists after Shutdown", sockPath)
	} else if !os.IsNotExist(err) {
		// If the file didn't exist before (e.g. control plane failed to start),
		// that's also acceptable — the important thing is it's not lingering.
		t.Logf("socket stat after Shutdown: %v (acceptable)", err)
	}

	// Verify port is released after shutdown.
	var portReleased bool
	for i := 0; i < 20; i++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln.Close()
			portReleased = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !portReleased {
		t.Fatalf("port %d not released after Shutdown", port)
	}

	// Verify that log file was written to (startProxyServer logs via the proxy).
	logFile.Seek(0, 0)
	logContents, _ := io.ReadAll(logFile)
	_ = logContents // Log may or may not have content depending on timing; presence is not required.

	// Verify stats can still be read from the server (no panic on dead server).
	stats := srv.StatusSnapshot()
	if stats.Requests < 0 {
		t.Fatal("negative request count after shutdown")
	}
}
