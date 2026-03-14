package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otonashi/wet/config"
	"github.com/otonashi/wet/messages"
	"github.com/otonashi/wet/persist"
	"github.com/otonashi/wet/pipeline"
	"github.com/otonashi/wet/stats"
)

type contextKey string

const requestInfoKey contextKey = "wet_request_info"
const sseReqIDKey contextKey = "wet_sse_req_id"

type requestInfo struct {
	Method string
	Path   string
	Start  time.Time
}

type Server struct {
	cfg             *config.Config
	httpSrv         *http.Server
	startTime       time.Time
	requestCount    atomic.Int64
	turnCounter     atomic.Int64
	sessionStats    *stats.SessionStats
	sseInterceptors sync.Map
	ctrl            controlState
	persistMu       sync.RWMutex
	persistStore    *persist.Store
	sessionUUID     string // stable session identity from --resume UUID or generated
	statsRestored   bool
	logMu           sync.RWMutex
	logOutput       io.Writer
}

func New(cfg *config.Config) *Server {
	return NewWithLogOutput(cfg, os.Stderr)
}

func NewWithLogOutput(cfg *config.Config, logOutput io.Writer) *Server {
	if cfg == nil {
		cfg = config.Default()
	}
	if logOutput == nil {
		logOutput = os.Stderr
	}

	upstreamURL, err := url.Parse(cfg.Server.Upstream)
	if err != nil {
		panic(fmt.Sprintf("invalid upstream URL %q: %v", cfg.Server.Upstream, err))
	}

	sessionStats := stats.NewSessionStats()
	sessionStats.Port = cfg.Server.Port
	mode := cfg.Server.Mode
	if mode == "" {
		mode = "auto"
	}
	sessionStats.Mode = mode
	// Pre-seed context window with a safe default so the statusline renders
	// immediately on cold start.  The actual model-specific window size will
	// overwrite this on the first proxied request via RecordModel.
	sessionStats.ContextWindow = stats.ModelContextWindow("", cfg.Models.ContextWindows)

	s := &Server{
		cfg:          cfg,
		startTime:    time.Now(),
		logOutput:    logOutput,
		sessionStats: sessionStats,
	}

	transport := &http.Transport{
		TLSNextProto:       make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		ForceAttemptHTTP2:  false,
		DisableCompression: true,
	}

	rp := &httputil.ReverseProxy{
		Transport: transport,
		ErrorLog:  log.New(logOutput, "", log.LstdFlags),
		Director: func(req *http.Request) {
			// Claude Code sends to ANTHROPIC_BASE_URL which points to us.
			// It appends /v1/messages itself. We just swap the host to upstream.
			// No path manipulation needed — forward the exact path we receive.
			req.URL.Scheme = upstreamURL.Scheme
			req.URL.Host = upstreamURL.Host
			req.Host = upstreamURL.Host

			// Strip Accept-Encoding so upstream returns uncompressed responses.
			// The SSE interceptor must parse plain-text event streams; if the
			// upstream returns gzip-compressed SSE, the scanner sees binary and
			// silently misses every event.  Since this is a local proxy the
			// bandwidth cost of disabling compression is negligible.
			req.Header.Del("Accept-Encoding")
		},
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			info, _ := resp.Request.Context().Value(requestInfoKey).(requestInfo)
			latency := time.Since(info.Start)
			method := info.Method
			path := info.Path
			if method == "" {
				method = resp.Request.Method
			}
			if path == "" {
				path = resp.Request.URL.Path
			}
			s.logf("[wet] %s %s -> %d (%s)\n", method, path, resp.StatusCode, latency.Round(time.Millisecond))

			if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
				reqID, _ := resp.Request.Context().Value(sseReqIDKey).(string)
				if reqID != "" {
					interceptor := newSSEInterceptor(resp.Body)
					resp.Body = interceptor
					s.sseInterceptors.Store(reqID, interceptor)
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "bad_gateway",
				"message": err.Error(),
			})
		},
	}

	mux := http.NewServeMux()
	s.RegisterHTTPControl(mux)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.requestCount.Add(1)
		ctx := context.WithValue(r.Context(), requestInfoKey, requestInfo{
			Method: r.Method,
			Path:   r.URL.Path,
			Start:  time.Now(),
		})
		r = r.WithContext(ctx)

		// Intercept messages API for compression.
		if r.Method == http.MethodPost && isMessagesPath(r.URL.Path) {
			s.handleMessagesWithCompression(w, r, rp)
			return
		}

		rp.ServeHTTP(w, r)
	})

	s.httpSrv = &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler: mux,
	}

	// Start control plane (best-effort, don't fail server startup)
	if err := s.StartControlPlane(); err != nil {
		s.logf("[wet] control plane not available: %v\n", err)
	}

	// Write initial stats file so statusline shows immediately.
	// Shows "ready" for ~1s until first request seeds persistence data.
	// Each session has WET_PORT set — no cross-session bleed.
	_ = sessionStats.WriteInitialStatsFile()

	return s
}

func (s *Server) ListenAndServe() error {
	return s.httpSrv.ListenAndServe()
}

func (s *Server) Shutdown() {
	s.CleanupControlPlane()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.httpSrv.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":         "ok",
		"uptime_seconds": time.Since(s.startTime).Seconds(),
	})
}

func (s *Server) handleMessagesWithCompression(w http.ResponseWriter, r *http.Request, rp *httputil.ReverseProxy) {
	turnNum := int(s.turnCounter.Add(1))

	forward := func(forwardBody []byte) UsageData {
		usage := UsageData{}
		reqID := newSSERequestID()
		ctx := context.WithValue(r.Context(), sseReqIDKey, reqID)
		upstreamReq := r.WithContext(ctx)
		upstreamReq.Body = io.NopCloser(bytes.NewReader(forwardBody))
		upstreamReq.ContentLength = int64(len(forwardBody))
		rp.ServeHTTP(w, upstreamReq)

		val, ok := s.sseInterceptors.LoadAndDelete(reqID)
		if !ok {
			return usage
		}
		interceptor, ok := val.(*sseInterceptor)
		if !ok {
			return usage
		}
		usage = interceptor.Usage()
		if usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.CacheCreationInputTokens > 0 || usage.CacheReadInputTokens > 0 {
			s.sessionStats.RecordAPIUsage(usage.InputTokens, usage.OutputTokens, usage.CacheCreationInputTokens, usage.CacheReadInputTokens)
			_ = s.sessionStats.WriteStatsFile()
			s.logf("[wet] API usage: input=%d output=%d cache_create=%d cache_read=%d\n",
				usage.InputTokens, usage.OutputTokens,
				usage.CacheCreationInputTokens, usage.CacheReadInputTokens)
		}
		return usage
	}

	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		_ = forward(body)
		return
	}

	// If paused, forward unchanged
	if s.IsPaused() {
		_ = forward(body)
		return
	}

	req, err := messages.ParseRequest(body)
	if err != nil {
		_ = forward(body)
		return
	}
	reqCfg := s.configSnapshot()

	// Extract model name from request to determine context window size.
	if rawModel, ok := req.Rest["model"]; ok {
		var model string
		if json.Unmarshal(rawModel, &model) == nil && model != "" {
			s.sessionStats.RecordModel(model, reqCfg.Models.ContextWindows)
		}
	}

	// Determine whether this request belongs to the main session.
	// The first request through the proxy is always the main session; subsequent
	// requests with a different system-prompt fingerprint are subagents.
	systemHash := extractSystemHash(req)
	isMain := s.TagOrMatchMainSession(systemHash)
	// Lazy-init persistence store on first main-session request with valid hash.
	if isMain && systemHash != "" {
		s.ensurePersistStore(systemHash)
	}
	store := s.getPersistStore()

	// Record debug state for /_wet/debug/sessions.
	s.recordDebugState(systemHash, req, isMain)

	// Always classify staleness; only store for the main session so subagents
	// cannot overwrite the main session's tool-result state.
	infos := messages.ClassifyStaleness(req.Messages, reqCfg.Staleness.Threshold, reqCfg.Rules)
	idCounts := countToolUseIDOccurrences(infos)

	// Re-apply persisted compressions for resumed sessions.
	// This modifies req.Messages in-place but does NOT update infos (which still
	// holds the pre-compression content). We re-classify after persistence so
	// that StoreToolResults reflects the actual post-persistence state — otherwise
	// /_wet/inspect reports already-tombstoned items as uncompressed, causing the
	// profiler to overcount compressible tokens.
	persistedApplied := 0
	if isMain && store != nil {
		persistedApplied = s.applyPersistedReplacements(store, req, infos, idCounts)
		if persistedApplied > 0 {
			s.logf("[wet] persistence: re-applied %d prior compressions\n", persistedApplied)
			// Re-classify so stored tool results reflect tombstoned content.
			infos = messages.ClassifyStaleness(req.Messages, reqCfg.Staleness.Threshold, reqCfg.Rules)
		}
	}
	if isMain {
		s.StoreToolResults(infos)
	}

	var result pipeline.CompressResult
	mode := reqCfg.Server.Mode
	if mode == "" {
		mode = "auto"
	}

	switch mode {
	case "auto":
		result = pipeline.CompressRequest(req, reqCfg)
	}

	// Apply queued selective compression (from /_wet/compress) for the main
	// session.  In passthrough mode this is the only compression path; in auto
	// mode it supplements staleness-based compression so the control plane can
	// trigger mechanical (Tier 1) compressions on specific IDs.
	if isMain {
		targetIDs, replacements := s.DrainCompressState()
		if len(targetIDs) > 0 {
			sel := pipeline.CompressSelected(req, reqCfg, targetIDs, replacements)
			result.TotalToolResults += sel.TotalToolResults
			result.Compressed += sel.Compressed
			result.SkippedFresh += sel.SkippedFresh
			result.SkippedBypass += sel.SkippedBypass
			result.TokensBefore += sel.TokensBefore
			result.TokensAfter += sel.TokensAfter
			result.OverheadMs += sel.OverheadMs
			result.Items = append(result.Items, sel.Items...)
			if len(sel.Replacements) > 0 {
				if result.Replacements == nil {
					result.Replacements = make(map[string]string, len(sel.Replacements))
				}
				for id, tombstone := range sel.Replacements {
					result.Replacements[id] = tombstone
				}
			}
		}
	}

	var forwardBody []byte
	if result.Compressed > 0 || persistedApplied > 0 {
		forwardBody, err = req.Marshal()
		if err != nil {
			forwardBody = body
		} else if result.Compressed > 0 {
			saved := 0
			if result.TokensBefore > 0 {
				saved = 100 - (result.TokensAfter*100)/result.TokensBefore
			}
			s.logf("[wet] %d results, %d compressed, %d->%d tokens (%d%% saved), +%.1fms\n",
				result.TotalToolResults, result.Compressed,
				result.TokensBefore, result.TokensAfter, saved, result.OverheadMs)
			s.AddCompressedStats(result.Compressed, result.TokensBefore-result.TokensAfter)
			// Persist new compressions.
			if isMain && store != nil && len(result.Replacements) > 0 {
				toPersist := filterPersistableReplacements(result.Replacements, idCounts)
				if len(toPersist) > 0 {
					if err := store.RecordBatch(toPersist); err != nil {
						s.logf("[wet] persistence write error: %v\n", err)
					}
				} else if len(result.Replacements) > 0 {
					s.logf("[wet] persistence: skipped ambiguous duplicate tool_use_id replacements\n")
				}
			}
			// Update cumulative stats with this cycle's delta.
			if isMain && store != nil {
				if err := store.UpdateCumulative(int64(result.TokensBefore), int64(result.TokensAfter), result.Compressed); err != nil {
					s.logf("[wet] persistence: cumulative update error: %v\n", err)
				}
			}
		}
	} else {
		forwardBody = body
	}
	s.sessionStats.RecordRequest(result)
	_ = s.sessionStats.WriteStatsFile()

	usage := forward(forwardBody)

	// Record API-observed savings when compression was applied this turn
	if result.Compressed > 0 {
		prevTotal := s.sessionStats.GetPrevTotalContext()
		currentTotal := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
		if prevTotal > 0 && currentTotal > 0 {
			s.sessionStats.RecordCompressionDelta(prevTotal, currentTotal)
			_ = s.sessionStats.WriteStatsFile()
		}
	}

	if isMain && store != nil {
		var turnItems []persist.TurnItem
		totalCharsSaved := 0
		for _, item := range result.Items {
			charsSaved := item.OriginalChars - item.TombstoneChars
			totalCharsSaved += charsSaved
			turnItems = append(turnItems, persist.TurnItem{
				ID:         item.ToolUseID,
				Tool:       item.ToolName,
				Cmd:        item.Command,
				OrigChars:  item.OriginalChars,
				TombChars:  item.TombstoneChars,
				CharsSaved: charsSaved,
				Tombstone:  item.Tombstone,
				Preview:    item.Preview,
			})
		}
		if turnItems == nil {
			turnItems = []persist.TurnItem{}
		}
		totalContext := usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
		tokensSavedEst := int(float64(totalCharsSaved) / 3.3)
		rec := persist.TurnRecord{
			Type: "turn",
			Turn: turnNum,
			Ts:   time.Now().UTC().Format(time.RFC3339),
			Usage: persist.UsageRecord{
				InputTokens:              usage.InputTokens,
				OutputTokens:             usage.OutputTokens,
				CacheReadInputTokens:     usage.CacheReadInputTokens,
				CacheCreationInputTokens: usage.CacheCreationInputTokens,
			},
			TotalContext:   totalContext,
			CharsSaved:     totalCharsSaved,
			TokensSavedEst: tokensSavedEst,
			Items:          turnItems,
		}
		if err := store.AppendTurn(rec); err != nil {
			s.logf("[wet] session.jsonl write error: %v\n", err)
		}
	}
}

func (s *Server) configSnapshot() *config.Config {
	s.ctrl.mu.RLock()
	defer s.ctrl.mu.RUnlock()
	return cloneConfig(s.cfg)
}

func cloneConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return config.Default()
	}
	c := *cfg
	if cfg.Rules != nil {
		c.Rules = make(map[string]config.RuleConfig, len(cfg.Rules))
		for k, v := range cfg.Rules {
			c.Rules[k] = v
		}
	} else {
		c.Rules = nil
	}
	c.Bypass.ContentPatterns = append([]string(nil), cfg.Bypass.ContentPatterns...)
	if cfg.Models.ContextWindows != nil {
		c.Models.ContextWindows = make(map[string]int, len(cfg.Models.ContextWindows))
		for k, v := range cfg.Models.ContextWindows {
			c.Models.ContextWindows[k] = v
		}
	}
	return &c
}

func (s *Server) getPersistStore() *persist.Store {
	s.persistMu.RLock()
	defer s.persistMu.RUnlock()
	return s.persistStore
}

// SetSessionUUID sets the stable session identity (from --resume UUID or generated).
// Must be called before the first request is processed.
func (s *Server) SetSessionUUID(uuid string) {
	s.sessionUUID = uuid
}

// SessionUUID returns the session UUID, or empty if not set.
func (s *Server) SessionUUID() string {
	return s.sessionUUID
}

// RestoreResumeStats eagerly restores cumulative compression stats for resumed
// sessions so statusline data is correct before the first proxied request.
// It also hydrates the last turn's total_context so the statusline shows
// context fill immediately rather than disappearing until the first API call.
func (s *Server) RestoreResumeStats() {
	uuid := s.sessionUUID
	if uuid == "" {
		return
	}

	store, err := persist.Open(uuid)
	if err != nil || store == nil {
		return
	}

	// Hydrate last turn's total context so the statusline shows context fill
	// immediately on resume (before the first API round-trip).
	if lastCtx := store.LastTurnTotalContext(); lastCtx > 0 {
		s.sessionStats.SetLatestTotalInputTokens(lastCtx)
	}

	cumulative := store.LoadCumulative()
	if cumulative.TokensBefore == 0 && cumulative.TokensAfter == 0 && cumulative.ItemsCompressed == 0 {
		// Still re-write the initial stats file so context fill shows up.
		_ = s.sessionStats.WriteInitialStatsFile()
		return
	}

	effectiveCount := len(store.All())
	if cumulative.ItemsCompressed > effectiveCount {
		effectiveCount = cumulative.ItemsCompressed
	}

	s.logf("[wet] resume: restored %d prior compressions for session %s\n", effectiveCount, uuid)
	s.AddCompressedStats(effectiveCount, int(cumulative.TokensBefore-cumulative.TokensAfter))
	if err := s.sessionStats.SeedCumulativeStats(cumulative.TokensBefore, cumulative.TokensAfter, effectiveCount); err != nil {
		s.logf("[wet] persistence: cumulative stats seed error: %v\n", err)
	}
	s.statsRestored = true
}

func (s *Server) ensurePersistStore(systemHash string) {
	// Use session UUID as the persistence key when available (stable across
	// MEMORY.md changes, date changes, skill additions). Fall back to the
	// system-prompt hash for backwards compatibility.
	persistKey := s.sessionUUID
	if persistKey == "" {
		persistKey = systemHash
	}
	if persistKey == "" {
		return
	}
	if s.getPersistStore() != nil {
		return
	}

	store, err := persist.Open(persistKey)
	if err != nil {
		s.logf("[wet] persistence store error: %v\n", err)
		return
	}
	if store == nil {
		return
	}
	mode := "auto"
	if s.cfg != nil && s.cfg.Server.Mode != "" {
		mode = s.cfg.Server.Mode
	}
	header := persist.SessionHeader{
		V:       1,
		Type:    "header",
		Session: persistKey,
		Created: time.Now().UTC().Format(time.RFC3339),
		Model:   s.sessionStats.Model,
		Mode:    mode,
	}
	if err := store.EnsureHeader(header); err != nil {
		s.logf("[wet] session.jsonl header error: %v\n", err)
	}

	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if s.persistStore != nil {
		return
	}
	s.persistStore = store
	count := len(store.All())

	// Load cumulative stats (lifetime token totals) from disk.
	cumulative := store.LoadCumulative()

	if !s.statsRestored && (count > 0 || cumulative.ItemsCompressed > 0) {
		effectiveCount := count
		if cumulative.ItemsCompressed > count {
			effectiveCount = cumulative.ItemsCompressed
		}
		s.logf("[wet] persistence: loaded %d prior compressions for session %s\n", effectiveCount, persistKey)
		// Seed ctrl.totalCompressed so /_wet/status items_compressed reflects
		// restored compressions immediately (not just after the next live compression).
		s.AddCompressedStats(effectiveCount, int(cumulative.TokensBefore-cumulative.TokensAfter))
		// Seed stats with cumulative token totals so statusline shows real lifetime numbers.
		if err := s.sessionStats.SeedCumulativeStats(cumulative.TokensBefore, cumulative.TokensAfter, effectiveCount); err != nil {
			s.logf("[wet] persistence: cumulative stats seed error: %v\n", err)
		}
	}
}

func countToolUseIDOccurrences(infos []messages.ToolResultInfo) map[string]int {
	counts := make(map[string]int, len(infos))
	for _, info := range infos {
		if info.ToolUseID == "" {
			continue
		}
		counts[info.ToolUseID]++
	}
	return counts
}

func filterPersistableReplacements(replacements map[string]string, idCounts map[string]int) map[string]string {
	if len(replacements) == 0 {
		return nil
	}
	out := make(map[string]string, len(replacements))
	for id, tombstone := range replacements {
		if id == "" {
			continue
		}
		if idCounts[id] > 1 {
			// Ambiguous mapping (same tool_use_id appears multiple times in one request).
			continue
		}
		out[id] = tombstone
	}
	return out
}

func copyHeader(h http.Header, key, value string) {
	if value == "" {
		return
	}
	h.Set(key, value)
}

func newSSERequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("sse-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}

func joinURLPath(a, b *url.URL) (path, rawpath string) {
	if a.RawPath == "" && b.RawPath == "" {
		return singleJoiningSlash(a.Path, b.Path), ""
	}

	apath := a.EscapedPath()
	bpath := b.EscapedPath()
	aslash := len(apath) > 0 && apath[len(apath)-1] == '/'
	bslash := len(bpath) > 0 && bpath[0] == '/'
	switch {
	case aslash && bslash:
		return a.Path + b.Path[1:], apath + bpath[1:]
	case !aslash && !bslash:
		return a.Path + "/" + b.Path, apath + "/" + bpath
	}
	return a.Path + b.Path, apath + bpath
}

func singleJoiningSlash(a, b string) string {
	aslash := len(a) > 0 && a[len(a)-1] == '/'
	bslash := len(b) > 0 && b[0] == '/'
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

func normalizeAPIPath(path, rawpath string) (string, string) {
	if strings.HasPrefix(path, "/v1/") || path == "/v1" {
		return path, rawpath
	}

	if path == "" {
		path = "/"
	}
	if strings.HasPrefix(path, "/") {
		path = "/v1" + path
	} else {
		path = "/v1/" + path
	}

	if rawpath != "" && !strings.HasPrefix(rawpath, "/v1/") && rawpath != "/v1" {
		if strings.HasPrefix(rawpath, "/") {
			rawpath = "/v1" + rawpath
		} else {
			rawpath = "/v1/" + rawpath
		}
	}

	return path, rawpath
}

func isMessagesPath(path string) bool {
	trimmed := strings.TrimSuffix(path, "/")
	return trimmed == "/v1/messages" || trimmed == "/messages" ||
		strings.HasSuffix(trimmed, "/v1/messages")
}

// SetLogOutput changes where proxy operational logs are written.
// By default logs go to stderr.
func (s *Server) SetLogOutput(w io.Writer) {
	if w == nil {
		return
	}
	s.logMu.Lock()
	s.logOutput = w
	s.logMu.Unlock()
}

func (s *Server) logf(format string, args ...any) {
	s.logMu.RLock()
	out := s.logOutput
	s.logMu.RUnlock()
	if out == nil {
		return
	}
	_, _ = fmt.Fprintf(out, format, args...)
}

// applyPersistedReplacements checks each tool_result block against the persistence
// store. If a stored tombstone exists and the content is not already a tombstone,
// apply the replacement. Returns the number of replacements applied.
func (s *Server) applyPersistedReplacements(store *persist.Store, req *messages.Request, infos []messages.ToolResultInfo, idCounts map[string]int) int {
	if store == nil || req == nil {
		return 0
	}

	applied := 0
	for _, info := range infos {
		if info.ToolUseID == "" || idCounts[info.ToolUseID] > 1 {
			// Skip ambiguous duplicate IDs; a single persisted tombstone cannot
			// safely map to multiple distinct tool_result blocks.
			continue
		}
		if pipeline.IsTombstone(info.Content) {
			continue
		}

		tombstone, ok := store.Lookup(info.ToolUseID)
		if !ok {
			continue
		}

		if err := pipeline.ReplaceToolResultContent(&req.Messages[info.MsgIdx], info.BlockIdx, tombstone, info.ContentIsStr); err != nil {
			continue
		}
		applied++
	}

	return applied
}

// recordDebugState captures per-request diagnostic info for /_wet/debug/sessions.
func (s *Server) recordDebugState(hash string, req *messages.Request, isMain bool) {
	preview := extractSystemPreview(req, 500)

	s.ctrl.mu.Lock()
	s.ctrl.lastRequestHash = hash
	s.ctrl.lastRequestSystemPreview = preview
	s.ctrl.lastRequestIsMain = isMain
	s.ctrl.mu.Unlock()

	if isMain {
		s.ctrl.totalMain.Add(1)
	} else {
		s.ctrl.totalSubagent.Add(1)
	}
}

// stripBillingHeader removes the dynamic x-anthropic-billing-header line that
// Claude Code prepends to every system prompt.  The header looks like:
//
//	x-anthropic-billing-header: cc_version=2.1.72.0ed; cc_entrypoint=cli; cch=b61a0;
//
// Its values change between requests, so it must be stripped before hashing or
// displaying the system prompt.
func stripBillingHeader(s string) string {
	const prefix = "x-anthropic-billing-header:"
	if !strings.HasPrefix(s, prefix) {
		return s
	}
	// Drop everything up to and including the first newline.
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[idx+1:]
	}
	// Header occupies the entire string — nothing stable remains.
	return ""
}

// extractSystemPreview returns the first maxRunes runes of the system prompt as a plain string.
func extractSystemPreview(req *messages.Request, maxRunes int) string {
	raw, ok := req.Rest["system"]
	if !ok || len(raw) == 0 {
		return ""
	}
	trimmed := bytes.TrimSpace(raw)
	var full string
	switch {
	case len(trimmed) == 0 || string(trimmed) == "null":
		return ""
	case trimmed[0] == '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			full = s
		} else {
			full = string(trimmed)
		}
	case trimmed[0] == '[':
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(trimmed, &blocks); err == nil {
			var sb strings.Builder
			for _, b := range blocks {
				sb.WriteString(b.Text)
			}
			full = sb.String()
		} else {
			full = string(trimmed)
		}
	default:
		full = string(trimmed)
	}
	full = stripBillingHeader(full)
	runes := []rune(full)
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return string(runes)
}

// extractSystemHash derives a session fingerprint from the parsed request's
// system field.  The Anthropic API allows system to be either a plain string
// or an array of content blocks; we flatten it to a single string before
// hashing so both representations produce the same fingerprint for the same
// content.
//
// The dynamic x-anthropic-billing-header line that Claude Code prepends is
// stripped before hashing so that header drift does not cause fingerprint
// churn across requests.
//
// Returns an empty string when the system prompt (after stripping) is absent
// or shorter than 50 characters — these are not stable fingerprints and
// should not lock in the main-session hash.
func extractSystemHash(req *messages.Request) string {
	raw, ok := req.Rest["system"]
	if !ok || len(raw) == 0 {
		return ""
	}

	trimmed := bytes.TrimSpace(raw)
	var text string
	switch {
	case len(trimmed) == 0 || string(trimmed) == "null":
		return ""

	case trimmed[0] == '"':
		// Plain string
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			text = s
		} else {
			text = string(trimmed)
		}

	case trimmed[0] == '[':
		// Array of content blocks — concatenate all text values.
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(trimmed, &blocks); err == nil {
			var sb strings.Builder
			for _, b := range blocks {
				sb.WriteString(b.Text)
			}
			text = sb.String()
		} else {
			text = string(trimmed)
		}

	default:
		// Fallback: use the raw JSON bytes verbatim.
		text = string(trimmed)
	}

	// Strip the billing header prepended by Claude Code.
	text = stripBillingHeader(text)

	// Don't treat absent or empty prompts as a stable fingerprint.
	// A threshold of 10 chars filters out empty strings (including the
	// result of stripping a billing-header-only system field) without
	// rejecting genuine short system prompts.
	if len([]rune(text)) < 10 {
		return ""
	}

	return HashSystemPrompt(text)
}
