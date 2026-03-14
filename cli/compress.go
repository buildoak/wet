package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

const compressUsage = `Usage:
  wet compress [PORT] [--json] [--dry-run]
  wet compress [PORT] --ids id1,id2 [--text JSON | --text-file PATH] [--dry-run] [--json]

Flags:
  --ids ID1,ID2,...   Comma-separated tool_use IDs to compress
  --text JSON         JSON object mapping IDs to replacement text
  --text-file PATH    Read replacement text JSON object from file
  --dry-run           Show what would be queued without queuing
  --json              Output JSON
  --port PORT         Proxy port override
  --help              Show this help
`

type compressOptions struct {
	IDs         []string
	TextJSON    string
	TextFile    string
	DryRun      bool
	JSONOutput  bool
	Interactive bool
	Help        bool
}

type compressStatus struct {
	LatestTotalInputTokens int64  `json:"latest_total_input_tokens"`
	ContextWindow          int64  `json:"context_window"`
	RequestCount           int64  `json:"request_count"`
	TokensSaved            int64  `json:"tokens_saved"`
	ItemsCompressed        int64  `json:"items_compressed"`
	ItemsTotal             int64  `json:"items_total"`
	Mode                   string `json:"mode"`
	Paused                 bool   `json:"paused"`
}

type compressInspectItem struct {
	ToolUseID      string `json:"tool_use_id"`
	ToolName       string `json:"tool_name"`
	Command        string `json:"command"`
	FilePath       string `json:"file_path"`
	Turn           int    `json:"turn"`
	CurrentTurn    int    `json:"current_turn"`
	Stale          bool   `json:"stale"`
	IsError        bool   `json:"is_error"`
	HasImages      bool   `json:"has_images"`
	TokenCount     int    `json:"token_count"`
	ContentPreview string `json:"content_preview"`
	MsgIdx         int    `json:"msg_idx"`
	BlockIdx       int    `json:"block_idx"`
}

type compressListRow struct {
	ID             string `json:"id"`
	ToolName       string `json:"tool_name"`
	TokenCount     int    `json:"token_count"`
	EstimatedAfter int    `json:"estimated_after"`
	Turn           int    `json:"turn"`
	Preview        string `json:"preview"`
}

func RunCompress(args []string) error {
	opts, err := parseCompressArgs(args)
	if err != nil {
		return err
	}
	if opts.Help {
		fmt.Print(compressUsage)
		return nil
	}
	if opts.Interactive {
		return fmt.Errorf("--interactive is not implemented yet")
	}
	if len(opts.IDs) == 0 && (opts.TextJSON != "" || opts.TextFile != "") {
		return fmt.Errorf("--text and --text-file require --ids")
	}

	port, err := resolvePortOrDiscover()
	if err != nil {
		return err
	}
	SetPort(port)

	if len(opts.IDs) == 0 {
		return runCompressList(port, opts)
	}
	return runCompressDirect(port, opts)
}

func parseCompressArgs(args []string) (*compressOptions, error) {
	opts := &compressOptions{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			opts.Help = true
		case arg == "--dry-run":
			opts.DryRun = true
		case arg == "--json":
			opts.JSONOutput = true
		case arg == "--interactive":
			opts.Interactive = true
		case arg == "--ids":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--ids requires a value")
			}
			opts.IDs = append(opts.IDs, parseIDList(args[i+1])...)
			i++
		case strings.HasPrefix(arg, "--ids="):
			opts.IDs = append(opts.IDs, parseIDList(strings.TrimPrefix(arg, "--ids="))...)
		case arg == "--text":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--text requires a value")
			}
			opts.TextJSON = args[i+1]
			i++
		case strings.HasPrefix(arg, "--text="):
			opts.TextJSON = strings.TrimPrefix(arg, "--text=")
		case arg == "--text-file":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--text-file requires a path")
			}
			opts.TextFile = args[i+1]
			i++
		case strings.HasPrefix(arg, "--text-file="):
			opts.TextFile = strings.TrimPrefix(arg, "--text-file=")
		case arg == "--port":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--port requires a value")
			}
			p, err := strconv.Atoi(args[i+1])
			if err != nil {
				return nil, fmt.Errorf("invalid --port value: %s", args[i+1])
			}
			SetPort(p)
			i++
		case strings.HasPrefix(arg, "--port="):
			p, err := strconv.Atoi(strings.TrimPrefix(arg, "--port="))
			if err != nil {
				return nil, fmt.Errorf("invalid --port value: %s", strings.TrimPrefix(arg, "--port="))
			}
			SetPort(p)
		case strings.HasPrefix(arg, "-"):
			return nil, fmt.Errorf("unknown flag: %s", arg)
		default:
			p, err := strconv.Atoi(arg)
			if err != nil {
				return nil, fmt.Errorf("unknown argument: %s", arg)
			}
			SetPort(p)
		}
	}

	if opts.TextJSON != "" && opts.TextFile != "" {
		return nil, fmt.Errorf("use only one of --text or --text-file")
	}

	seen := make(map[string]struct{}, len(opts.IDs))
	clean := make([]string, 0, len(opts.IDs))
	for _, id := range opts.IDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		clean = append(clean, id)
	}
	opts.IDs = clean

	return opts, nil
}

func parseIDList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func runCompressList(port int, opts *compressOptions) error {
	status, err := fetchCompressStatus()
	if err != nil {
		return err
	}
	items, err := fetchCompressInspectItems()
	if err != nil {
		return err
	}

	rows := make([]compressListRow, 0, len(items))
	totalTokens := 0
	for _, item := range items {
		if ok := isCompressible(item); !ok {
			continue
		}
		row := compressListRow{
			ID:             item.ToolUseID,
			ToolName:       item.ToolName,
			TokenCount:     item.TokenCount,
			EstimatedAfter: estimateCompressedTokens(item),
			Turn:           item.Turn,
			Preview:        previewOneLine(item.ContentPreview, 64),
		}
		rows = append(rows, row)
		totalTokens += item.TokenCount
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TokenCount == rows[j].TokenCount {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].TokenCount > rows[j].TokenCount
	})

	if opts.JSONOutput {
		out := map[string]any{
			"port":                      port,
			"latest_total_input_tokens": status.LatestTotalInputTokens,
			"context_window":            status.ContextWindow,
			"request_count":             status.RequestCount,
			"tokens_saved":              status.TokensSaved,
			"items_compressed":          status.ItemsCompressed,
			"items_total":               status.ItemsTotal,
			"mode":                      status.Mode,
			"paused":                    status.Paused,
			"compressible_count":        len(rows),
			"compressible_tokens":       totalTokens,
			"items":                     rows,
		}
		if len(rows) > 0 {
			out["suggested_command"] = "wet compress --ids " + strings.Join(suggestIDs(rows, 3), ",")
		}
		if opts.DryRun {
			out["dry_run"] = true
			out["note"] = "no compression queued"
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}

	contextText := "n/a"
	if status.ContextWindow > 0 && status.LatestTotalInputTokens > 0 {
		pct := int(float64(status.LatestTotalInputTokens) / float64(status.ContextWindow) * 100.0)
		contextText = fmt.Sprintf("%d%% (%s / %s)", pct, formatTokenCount(status.LatestTotalInputTokens), formatTokenCount(status.ContextWindow))
	}
	fmt.Printf("wet proxy on :%d  mode=%s  context=%s\n\n", port, status.Mode, contextText)

	if len(rows) == 0 {
		fmt.Println("No compressible items found.")
		if opts.DryRun {
			fmt.Println("dry-run: no compression queued.")
		}
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTOOL\tTURN\tTOKENS\tEST_AFTER\tPREVIEW")
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%s\n",
			shortID(row.ID),
			row.ToolName,
			row.Turn,
			row.TokenCount,
			row.EstimatedAfter,
			row.Preview,
		)
	}
	_ = w.Flush()

	fmt.Printf("\nCompressible: %d items (%s tokens)\n", len(rows), formatTokenCount(int64(totalTokens)))
	fmt.Printf("Suggested: wet compress --ids %s\n", strings.Join(suggestIDs(rows, 3), ","))
	if opts.DryRun {
		fmt.Println("dry-run: no compression queued.")
	}
	return nil
}

func runCompressDirect(port int, opts *compressOptions) error {
	replacementText, err := parseReplacementText(opts.TextJSON, opts.TextFile)
	if err != nil {
		return err
	}

	items, err := fetchCompressInspectItems()
	if err != nil {
		return err
	}
	toolByID := make(map[string]compressInspectItem, len(items))
	for _, item := range items {
		toolByID[item.ToolUseID] = item
	}

	selected := make([]compressInspectItem, 0, len(opts.IDs))
	missing := make([]string, 0)
	totalTokens := 0
	for _, id := range opts.IDs {
		item, ok := toolByID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		selected = append(selected, item)
		totalTokens += item.TokenCount
	}
	if len(missing) > 0 {
		return fmt.Errorf("unknown tool_use IDs: %s", strings.Join(missing, ","))
	}

	for _, item := range selected {
		if isAgentTool(item.ToolName) {
			txt := strings.TrimSpace(replacementText[item.ToolUseID])
			if txt == "" {
				return fmt.Errorf("tool result %s (%s) requires replacement text (--text or --text-file)", item.ToolUseID, item.ToolName)
			}
		}
	}

	if opts.DryRun {
		return printDirectDryRun(port, selected, replacementText, totalTokens, opts.JSONOutput)
	}

	batches := chunkIDs(opts.IDs, 100)
	results := make([]map[string]any, 0, len(batches))
	queuedTotal := 0

	for _, batch := range batches {
		payload := map[string]any{
			"ids": batch,
		}
		if len(replacementText) > 0 {
			subset := make(map[string]string)
			for _, id := range batch {
				if v, ok := replacementText[id]; ok {
					subset[id] = v
				}
			}
			if len(subset) > 0 {
				payload["replacement_text"] = subset
			}
		}

		body, err := httpPost("compress", payload)
		if err != nil {
			return formatCompressHTTPError(body, err)
		}

		var res map[string]any
		if err := json.Unmarshal(body, &res); err != nil {
			return fmt.Errorf("decode compress response: %w", err)
		}
		results = append(results, res)
		queuedTotal += int(anyInt64(res["count"]))
	}

	if opts.JSONOutput {
		out := map[string]any{
			"status":      "queued",
			"port":        port,
			"queued":      queuedTotal,
			"batch_count": len(batches),
			"responses":   results,
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("Queued %d item(s) for compression on :%d.\n", queuedTotal, port)
	if len(replacementText) > 0 {
		fmt.Printf("Replacement text provided for %d item(s).\n", len(replacementText))
	}
	fmt.Println("Use 'wet status' to track compression progress.")
	return nil
}

func printDirectDryRun(port int, selected []compressInspectItem, replacement map[string]string, totalTokens int, jsonOutput bool) error {
	if jsonOutput {
		items := make([]map[string]any, 0, len(selected))
		for _, item := range selected {
			items = append(items, map[string]any{
				"id":                      item.ToolUseID,
				"tool_name":               item.ToolName,
				"token_count":             item.TokenCount,
				"estimated_after":         estimateCompressedTokens(item),
				"has_replacement_text":    strings.TrimSpace(replacement[item.ToolUseID]) != "",
				"replacement_text_length": len(replacement[item.ToolUseID]),
			})
		}
		out := map[string]any{
			"status":       "dry_run",
			"port":         port,
			"count":        len(selected),
			"total_tokens": totalTokens,
			"items":        items,
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("dry-run on :%d\n\n", port)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTOOL\tTOKENS\tEST_AFTER\tREPLACEMENT")
	for _, item := range selected {
		rep := "no"
		if strings.TrimSpace(replacement[item.ToolUseID]) != "" {
			rep = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\n",
			shortID(item.ToolUseID),
			item.ToolName,
			item.TokenCount,
			estimateCompressedTokens(item),
			rep,
		)
	}
	_ = w.Flush()
	fmt.Printf("\nWould queue %d item(s), ~%s tokens before compression.\n", len(selected), formatTokenCount(int64(totalTokens)))
	return nil
}

func fetchCompressStatus() (*compressStatus, error) {
	data, err := httpGet("status")
	if err != nil {
		return nil, err
	}
	var payload compressStatus
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode status response: %w", err)
	}
	return &payload, nil
}

func fetchCompressInspectItems() ([]compressInspectItem, error) {
	data, err := httpGet("inspect")
	if err != nil {
		return nil, err
	}
	var items []compressInspectItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("decode inspect response: %w", err)
	}
	return items, nil
}

func parseReplacementText(textJSON, textFile string) (map[string]string, error) {
	switch {
	case textJSON == "" && textFile == "":
		return nil, nil
	case textJSON != "":
		return decodeReplacementMap([]byte(textJSON), "--text")
	default:
		data, err := os.ReadFile(textFile)
		if err != nil {
			return nil, fmt.Errorf("read --text-file: %w", err)
		}
		return decodeReplacementMap(data, "--text-file")
	}
}

func decodeReplacementMap(data []byte, source string) (map[string]string, error) {
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid JSON for %s: %w", source, err)
	}
	if m == nil {
		return nil, fmt.Errorf("%s must be a JSON object", source)
	}
	return m, nil
}

func isCompressible(item compressInspectItem) bool {
	if item.TokenCount <= 0 {
		return false
	}
	if item.IsError || item.HasImages {
		return false
	}
	return true
}

func isAgentTool(name string) bool {
	return strings.EqualFold(name, "Agent") || strings.EqualFold(name, "Task")
}

func estimateCompressedTokens(item compressInspectItem) int {
	ratio := 0.30
	switch strings.ToLower(item.ToolName) {
	case "bash", "grep", "glob":
		ratio = 0.18
	case "agent", "task":
		ratio = 0.35
	case "read", "webfetch", "websearch":
		ratio = 0.28
	}
	est := int(float64(item.TokenCount) * ratio)
	if est < 1 {
		est = 1
	}
	if est > item.TokenCount {
		est = item.TokenCount
	}
	return est
}

func suggestIDs(rows []compressListRow, n int) []string {
	if n <= 0 || len(rows) == 0 {
		return nil
	}
	if len(rows) < n {
		n = len(rows)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, rows[i].ID)
	}
	return out
}

func chunkIDs(ids []string, size int) [][]string {
	if size <= 0 {
		size = len(ids)
	}
	out := make([][]string, 0, (len(ids)+size-1)/size)
	for i := 0; i < len(ids); i += size {
		end := i + size
		if end > len(ids) {
			end = len(ids)
		}
		out = append(out, ids[i:end])
	}
	return out
}

func formatCompressHTTPError(body []byte, err error) error {
	if len(body) > 0 {
		var e struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if json.Unmarshal(body, &e) == nil {
			if e.Code != "" && e.Error != "" {
				return fmt.Errorf("%s: %s", e.Code, e.Error)
			}
			if e.Error != "" {
				return fmt.Errorf("%s", e.Error)
			}
		}
	}
	return err
}
