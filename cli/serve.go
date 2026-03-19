package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/buildoak/wet/config"
	"github.com/buildoak/wet/proxy"
)

const serveUsage = `Usage:
  wet serve [--host HOST] [--port PORT] [--mode passthrough|auto] [--upstream URL]
            [--session-id ID] [--resume]

Environment:
  WET_HOST           Override bind host
  WET_PORT           Override bind port
  WET_MODE           Override proxy mode (passthrough|auto)
  WET_UPSTREAM       Override upstream URL (default https://api.anthropic.com)
  WET_SESSION_UUID   Stable session ID for persistence/resume
  WET_RESUME         Restore prior stats for WET_SESSION_UUID (1/true/yes)

Note:
  One proxy process tracks one main session. For multiple concurrent conversations
  (e.g., from separate IDE windows or a bot), run separate proxy instances on separate ports.

Examples:
  wet serve --host 0.0.0.0 --mode auto
  WET_HOST=0.0.0.0 WET_MODE=auto wet serve
`

type serveOptions struct {
	Host      string
	Port      int
	Mode      string
	Upstream  string
	SessionID string
	Resume    bool
	Help      bool
}

func RunServe(args []string) error {
	opts, err := parseServeArgs(args)
	if err != nil {
		return err
	}
	if opts.Help {
		fmt.Print(serveUsage)
		return nil
	}
	if opts.Resume && opts.SessionID == "" {
		return fmt.Errorf("--resume requires --session-id or WET_SESSION_UUID")
	}
	if opts.SessionID == "" {
		opts.SessionID = generateUUID()
	}

	logFile, err := openSessionLog()
	if err != nil {
		return err
	}
	defer logFile.Close()

	cfg := config.Load("")
	applyServeOptions(cfg, opts)

	srv, serverErrCh, err := startProxyServer(cfg, logFile, opts.SessionID, opts.Resume)
	if err != nil {
		return err
	}
	defer func() {
		srv.Shutdown()
		logProxySessionStats(logFile, srv)
	}()

	fmt.Fprintf(os.Stderr, "[wet] serving on http://%s:%d (%s mode)\n",
		cfg.Server.Host, cfg.Server.Port, effectiveServeMode(cfg.Server.Mode))
	fmt.Fprintf(os.Stderr, "[wet] session id: %s\n", opts.SessionID)

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "[wet] shutting down on %s\n", sig)
		return nil
	case err := <-serverErrCh:
		return err
	}
}

func parseServeArgs(args []string) (*serveOptions, error) {
	opts := &serveOptions{
		Host:      strings.TrimSpace(os.Getenv("WET_HOST")),
		Mode:      strings.TrimSpace(os.Getenv("WET_MODE")),
		Upstream:  strings.TrimSpace(os.Getenv("WET_UPSTREAM")),
		SessionID: strings.TrimSpace(os.Getenv("WET_SESSION_UUID")),
	}

	if envPort := strings.TrimSpace(os.Getenv("WET_PORT")); envPort != "" {
		port, err := strconv.Atoi(envPort)
		if err != nil {
			return nil, fmt.Errorf("invalid WET_PORT=%q: %w", envPort, err)
		}
		opts.Port = port
	}

	if envResume := strings.TrimSpace(os.Getenv("WET_RESUME")); envResume != "" {
		resume, err := parseServeBool(envResume)
		if err != nil {
			return nil, fmt.Errorf("invalid WET_RESUME=%q: %w", envResume, err)
		}
		opts.Resume = resume
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			opts.Help = true
		case arg == "--resume":
			opts.Resume = true
		case arg == "--host":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--host requires a value")
			}
			opts.Host = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--host="):
			opts.Host = strings.TrimSpace(strings.TrimPrefix(arg, "--host="))
		case arg == "--port":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--port requires a value")
			}
			port, err := strconv.Atoi(args[i+1])
			if err != nil {
				return nil, fmt.Errorf("invalid --port value: %s", args[i+1])
			}
			opts.Port = port
			i++
		case strings.HasPrefix(arg, "--port="):
			port, err := strconv.Atoi(strings.TrimPrefix(arg, "--port="))
			if err != nil {
				return nil, fmt.Errorf("invalid --port value: %s", strings.TrimPrefix(arg, "--port="))
			}
			opts.Port = port
		case arg == "--mode":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--mode requires a value")
			}
			opts.Mode = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--mode="):
			opts.Mode = strings.TrimSpace(strings.TrimPrefix(arg, "--mode="))
		case arg == "--upstream":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--upstream requires a value")
			}
			opts.Upstream = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--upstream="):
			opts.Upstream = strings.TrimSpace(strings.TrimPrefix(arg, "--upstream="))
		case arg == "--session-id":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--session-id requires a value")
			}
			opts.SessionID = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--session-id="):
			opts.SessionID = strings.TrimSpace(strings.TrimPrefix(arg, "--session-id="))
		default:
			return nil, fmt.Errorf("unknown flag: %s", arg)
		}
	}

	if opts.Port < 0 {
		return nil, fmt.Errorf("port must be >= 0")
	}
	if opts.Mode != "" {
		opts.Mode = strings.ToLower(opts.Mode)
		if opts.Mode != "auto" && opts.Mode != "passthrough" {
			return nil, fmt.Errorf("invalid mode %q: want passthrough or auto", opts.Mode)
		}
	}

	return opts, nil
}

func parseServeBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("want one of 1/0 true/false yes/no on/off")
	}
}

func applyServeOptions(cfg *config.Config, opts *serveOptions) {
	if cfg == nil || opts == nil {
		return
	}
	if opts.Host != "" {
		cfg.Server.Host = opts.Host
	}
	if opts.Port > 0 {
		cfg.Server.Port = opts.Port
	}
	if opts.Mode != "" {
		cfg.Server.Mode = opts.Mode
	}
	if opts.Upstream != "" {
		cfg.Server.Upstream = opts.Upstream
	}
}

func startProxyServer(cfg *config.Config, logOutput io.Writer, sessionUUID string, restore bool) (*proxy.Server, <-chan error, error) {
	if cfg == nil {
		cfg = config.Load("")
	}

	srv := proxy.NewWithLogOutput(cfg, logOutput)
	if sessionUUID != "" {
		srv.SetSessionUUID(sessionUUID)
	}
	if restore {
		srv.RestoreResumeStats()
	}

	serverErrCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- err
		}
	}()

	if err := waitForProxyReady(cfg.Server.Port, 2*time.Second, serverErrCh); err != nil {
		srv.Shutdown()
		return nil, nil, err
	}

	return srv, serverErrCh, nil
}

func logProxySessionStats(logOutput io.Writer, srv *proxy.Server) {
	if logOutput == nil || srv == nil {
		return
	}
	stats := srv.StatusSnapshot()
	fmt.Fprintf(logOutput, "[wet] session stats: requests=%d compressed=%d tokens_saved=%d\n",
		stats.Requests, stats.Compressed, stats.TokensSaved)
}

func effectiveServeMode(mode string) string {
	if strings.TrimSpace(mode) == "" {
		return "passthrough"
	}
	return mode
}
