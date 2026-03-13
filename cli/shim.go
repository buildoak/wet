package cli

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/otonashi/wet/config"
	"github.com/otonashi/wet/proxy"
)

// ChildExitError wraps a non-zero child process exit code.
type ChildExitError struct {
	Code int
}

func (e *ChildExitError) Error() string {
	return fmt.Sprintf("child process exited with code %d", e.Code)
}

func (e *ChildExitError) ExitCode() int {
	return e.Code
}

func RunShim(args []string) error {
	logFile, err := openSessionLog()
	if err != nil {
		return err
	}
	defer logFile.Close()

	cfg := config.Load("")

	port, err := findFreePort()
	if err != nil {
		return err
	}
	cfg.Server.Port = port

	// Extract session UUID from --resume flag, or generate a new one.
	resumeUUID := extractResumeUUID(args)
	sessionUUID := resumeUUID
	if sessionUUID == "" {
		sessionUUID = generateUUID()
	}

	srv := proxy.NewWithLogOutput(cfg, logFile)
	srv.SetSessionUUID(sessionUUID)
	if resumeUUID != "" {
		srv.RestoreResumeStats()
	}
	serverErrCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- err
		}
	}()

	defer func() {
		srv.Shutdown()
		stats := srv.StatusSnapshot()
		fmt.Fprintf(logFile, "[wet] session stats: requests=%d compressed=%d tokens_saved=%d\n",
			stats.Requests, stats.Compressed, stats.TokensSaved)
	}()

	if err := waitForProxyReady(port, 2*time.Second, serverErrCh); err != nil {
		return err
	}

	// Clear stale stats from previous session so statusline doesn't show old data.
	statsPath := expandHome(fmt.Sprintf("~/.wet/stats-%d.json", port))
	_ = os.WriteFile(statsPath, []byte("{}"), 0o644)

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	portStr := fmt.Sprintf("%d", port)
	cmd := exec.Command("claude", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"ANTHROPIC_BASE_URL="+baseURL,
		"WET_PORT="+portStr,
		"WET_SESSION_UUID="+sessionUUID,
	)

	if err := cmd.Start(); err != nil {
		return err
	}

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-sigCh:
				if cmd.Process != nil {
					_ = cmd.Process.Signal(sig)
				}
			case <-done:
				return
			}
		}
	}()

	if err := cmd.Wait(); err != nil {
		close(done)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &ChildExitError{Code: exitErr.ExitCode()}
		}
		return err
	}

	close(done)
	return nil
}

func openSessionLog() (*os.File, error) {
	logDir := expandHome("~/.wet")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("create log directory %s: %w", logDir, err)
	}

	logPath := filepath.Join(logDir, "wet.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open session log %s: %w", logPath, err)
	}
	return f, nil
}

func findFreePort() (int, error) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address type %T", ln.Addr())
	}

	return addr.Port, nil
}

func waitForProxyReady(port int, timeout time.Duration, serverErrCh <-chan error) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 250 * time.Millisecond}
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)

	for {
		select {
		case err := <-serverErrCh:
			return fmt.Errorf("proxy failed to start: %w", err)
		default:
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("proxy did not become healthy within %s", timeout)
		}

		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// extractResumeUUID scans args for --resume <uuid> or --resume=<uuid>
// and returns the UUID value, or "" if not found.
func extractResumeUUID(args []string) string {
	for i, arg := range args {
		if arg == "--resume" && i+1 < len(args) {
			return args[i+1]
		}
		if len(arg) > len("--resume=") && arg[:9] == "--resume=" {
			return arg[9:]
		}
	}
	return ""
}

// generateUUID returns a random UUID v4 string.
func generateUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback: use timestamp-based ID (extremely unlikely path).
		return fmt.Sprintf("wet-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
