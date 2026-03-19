package skill

import "embed"

//go:embed SKILL.md references/architecture.md references/heuristics.md references/onboarding.md references/cc-compatibility.md
var FS embed.FS
