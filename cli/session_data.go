package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/otonashi/wet/persist"
)

func findLatestSession() (string, error) {
	if sessionUUID := strings.TrimSpace(os.Getenv("WET_SESSION_UUID")); sessionUUID != "" {
		return sessionUUID, nil
	}

	pattern := expandHome("~/.wet/sessions/*/session.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("glob latest session: %w", err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no sessions found in %s", expandHome("~/.wet/sessions"))
	}

	var latestPath string
	var latestMod time.Time
	for _, path := range matches {
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		if latestPath == "" || info.ModTime().After(latestMod) {
			latestPath = path
			latestMod = info.ModTime()
		}
	}

	if latestPath == "" {
		return "", fmt.Errorf("no readable sessions found in %s", expandHome("~/.wet/sessions"))
	}

	return filepath.Base(filepath.Dir(latestPath)), nil
}

func RunSessionStatus() error {
	sessionKey, header, turns, err := loadSessionData()
	if err != nil {
		return err
	}

	totalCharsSaved := 0
	totalTokensSavedEst := 0
	totalItems := 0

	for _, rec := range turns {
		totalCharsSaved += rec.CharsSaved
		totalTokensSavedEst += rec.TokensSavedEst
		totalItems += len(rec.Items)
	}

	fmt.Printf("wet: ~%d tokens saved (est) | %d chars saved | %d items | session %s (%d turns)\n",
		totalTokensSavedEst, totalCharsSaved, totalItems, resolvedSessionUUID(sessionKey, header), len(turns))
	return nil
}

func RunSessionInspect(showAll bool) error {
	sessionKey, header, turns, err := loadSessionData()
	if err != nil {
		return err
	}

	totalTokensSavedEst := 0
	for _, rec := range turns {
		totalTokensSavedEst += rec.TokensSavedEst
	}

	fmt.Printf("Session %s | %d turns | ~%d tokens saved (est)\n\n",
		resolvedSessionUUID(sessionKey, header), len(turns), totalTokensSavedEst)

	shown := 0
	for i := len(turns) - 1; i >= 0; i-- {
		rec := turns[i]
		if len(rec.Items) == 0 {
			continue
		}
		if !showAll && shown >= 10 {
			break
		}

		fmt.Printf("Turn %d: %d items, ~%d tokens saved (est), context: %d\n",
			rec.Turn, len(rec.Items), rec.TokensSavedEst, rec.TotalContext)

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, item := range rec.Items {
			pct := 0
			if item.OrigChars > 0 {
				pct = (100 * item.CharsSaved) / item.OrigChars
			}

			tool := strings.TrimSpace(item.Tool)
			if tool == "" {
				tool = "Unknown"
			}
			cmd := compactInline(item.Cmd)
			label := tool
			if cmd != "" {
				label = fmt.Sprintf("%s(%s)", tool, cmd)
			}

			fmt.Fprintf(w, "  %s\t%d -> %d chars\t(-%d%%)\n",
				label, item.OrigChars, item.TombChars, pct)
		}
		_ = w.Flush()
		fmt.Println()
		shown++
	}

	if shown == 0 {
		fmt.Println("No compressed items found in this session.")
	}
	return nil
}

func RunSessionDiff(turnNum int) error {
	sessionKey, header, turns, err := loadSessionData()
	if err != nil {
		return err
	}

	var target *persist.TurnRecord
	for i := range turns {
		if turns[i].Turn == turnNum {
			target = &turns[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("turn %d not found in session %s", turnNum, resolvedSessionUUID(sessionKey, header))
	}

	fmt.Printf("Turn %d: %d items compressed, ~%d tokens saved (est)\n", target.Turn, len(target.Items), target.TokensSavedEst)
	fmt.Printf("Context: %d tokens (from API)\n\n", target.TotalContext)

	for _, item := range target.Items {
		itemID := strings.TrimSpace(item.ID)
		if itemID == "" {
			itemID = "<unknown-id>"
		}

		tool := strings.TrimSpace(item.Tool)
		if tool == "" {
			tool = "Unknown"
		}
		cmd := compactInline(item.Cmd)
		if cmd != "" {
			fmt.Printf("%s [%s: %s]\n", itemID, tool, cmd)
		} else {
			fmt.Printf("%s [%s]\n", itemID, tool)
		}

		fmt.Printf("  Original (%d chars):\n", item.OrigChars)
		fmt.Printf("    %s\n", compactInline(item.Preview))
		fmt.Printf("  Compressed (%d chars):\n", item.TombChars)
		fmt.Printf("    %s\n", compactInline(item.Tombstone))

		tokensSavedEst := int(float64(item.CharsSaved) / 3.3)
		fmt.Printf("  Saved: %d chars (~%d tokens est)\n\n", item.CharsSaved, tokensSavedEst)
	}

	return nil
}

func loadSessionData() (string, *persist.SessionHeader, []persist.TurnRecord, error) {
	sessionKey, err := findLatestSession()
	if err != nil {
		return "", nil, nil, err
	}

	store, err := persist.Open(sessionKey)
	if err != nil {
		return "", nil, nil, fmt.Errorf("open session %q: %w", sessionKey, err)
	}

	header, turns, err := store.ReadSession()
	if err != nil {
		return "", nil, nil, fmt.Errorf("read session %q: %w", sessionKey, err)
	}
	if header == nil {
		return "", nil, nil, fmt.Errorf("session %q has no session.jsonl data", sessionKey)
	}
	return sessionKey, header, turns, nil
}

func resolvedSessionUUID(sessionKey string, header *persist.SessionHeader) string {
	if header != nil {
		if sessionUUID := strings.TrimSpace(header.Session); sessionUUID != "" {
			return sessionUUID
		}
	}
	return sessionKey
}

func compactInline(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return "(empty)"
	}
	return s
}
