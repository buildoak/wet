package pipeline

import (
	"fmt"
	"strings"
)

const TombstonePrefix = "[compressed: "

// CreateTombstone builds the tombstone string that replaces a compressed tool_result.
// Format: [compressed: {family} | {summary} | turn {originalTurn}/{currentTurn} | {originalTokens}->{compressedTokens} tokens]
func CreateTombstone(family, summary string, originalTurn, currentTurn, originalTokens, compressedTokens int) string {
	return fmt.Sprintf("[compressed: %s | %s | turn %d/%d | %d->%d tokens]",
		family, summary, originalTurn, currentTurn, originalTokens, compressedTokens)
}

// IsTombstone checks if content has already been compressed (idempotency).
func IsTombstone(content string) bool {
	return strings.HasPrefix(strings.TrimSpace(content), TombstonePrefix)
}
