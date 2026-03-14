package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/buildoak/wet/skill"
)

const defaultSkillDir = ".claude/skills/wet-compress"

// skillFiles lists all files to install from the embedded FS.
var skillFiles = []string{
	"SKILL.md",
	"references/architecture.md",
	"references/heuristics.md",
}

// RunInstallSkill writes embedded skill files to the target directory.
func RunInstallSkill(args []string) error {
	dir, err := resolveSkillDir(args)
	if err != nil {
		return err
	}

	wrote, updated, upToDate := 0, 0, 0

	for _, relPath := range skillFiles {
		destPath := filepath.Join(dir, relPath)

		// Read embedded file
		data, err := fs.ReadFile(skill.FS, relPath)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", relPath, err)
		}

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("create directory for %s: %w", destPath, err)
		}

		// Check existing file
		existing, err := os.ReadFile(destPath)
		if err == nil {
			if bytes.Equal(existing, data) {
				fmt.Fprintf(os.Stderr, "  up to date  %s\n", relPath)
				upToDate++
				continue
			}
			// Content differs -- update
			if err := os.WriteFile(destPath, data, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", destPath, err)
			}
			fmt.Fprintf(os.Stderr, "  updated     %s\n", relPath)
			updated++
		} else if os.IsNotExist(err) {
			if err := os.WriteFile(destPath, data, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", destPath, err)
			}
			fmt.Fprintf(os.Stderr, "  wrote       %s\n", relPath)
			wrote++
		} else {
			return fmt.Errorf("check %s: %w", destPath, err)
		}
	}

	fmt.Fprintf(os.Stderr, "\n[wet] skill installed to %s\n", dir)
	fmt.Fprintf(os.Stderr, "      %d wrote, %d updated, %d up to date\n", wrote, updated, upToDate)
	return nil
}

// RunUninstallSkill removes the skill directory.
func RunUninstallSkill(args []string) error {
	dir, err := resolveSkillDir(args)
	if err != nil {
		return err
	}

	// Check if directory exists
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "[wet] %s does not exist, nothing to uninstall\n", dir)
			return nil
		}
		return fmt.Errorf("stat %s: %w", dir, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}

	fmt.Fprintf(os.Stderr, "[wet] removed %s\n", dir)
	return nil
}

// resolveSkillDir parses --dir flag or returns the default path.
func resolveSkillDir(args []string) (string, error) {
	var dir string
	for i := 0; i < len(args); i++ {
		if args[i] == "--dir" {
			if i+1 >= len(args) {
				return "", fmt.Errorf("--dir requires a path argument")
			}
			dir = args[i+1]
			i++
		}
	}

	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		dir = filepath.Join(home, defaultSkillDir)
	}

	return dir, nil
}
