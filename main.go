package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/otonashi/wet/cli"
)

const usageText = `Usage:
  wet claude [args...]     # session wrapper (primary)
  wet status [--json] [--port PORT|PORT]  # live proxy status
  wet ps [--all]           # list all running wet proxies
  wet data status          # offline session stats from ~/.wet/sessions
  wet data inspect [--all] # recent compressed items from session.jsonl
  wet data diff <turn>     # inspect one compressed turn in detail
  wet inspect [--json] [--full] [--port PORT|PORT]  # inspect live tool results
  wet compress --ids id1,id2,id3     # queue selective compression
  wet rules list           # show active rules
  wet rules set KEY VALUE  # tune rule at runtime
  wet pause                # bypass all compression
  wet resume               # re-enable compression
  wet statusline           # one-liner for Claude Code status bar
  wet install-statusline   # add wet statusline to Claude Code settings
  wet uninstall-statusline # remove wet statusline from Claude Code settings
  wet install-skill [--dir PATH] # install wet-compress skill to Claude Code
  wet uninstall-skill [--dir PATH] # remove wet-compress skill
  wet session salt         # generate a random session salt
  wet session find <SALT>  # find session JSONL by salt
  wet session profile --jsonl <PATH> [--port PORT]  # context composition
  wet --help / wet help

Control commands use WET_PORT env var or --port flag to find the proxy.
Session commands are offline (no proxy needed).
`

type exitCoder interface {
	ExitCode() int
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		printUsage(os.Stdout)
		return
	}

	var err error

	switch args[0] {
	case "--help", "-h", "help":
		printUsage(os.Stdout)
		return
	case "claude":
		err = cli.RunShim(args[1:])
	case "status":
		jsonOutput := false
		remaining := extractPort(args[1:])
		for i := 0; i < len(remaining); i++ {
			switch remaining[i] {
			case "--json":
				jsonOutput = true
			default:
				// Try as positional port argument
				if p, perr := strconv.Atoi(remaining[i]); perr == nil {
					cli.SetPort(p)
				}
			}
		}
		err = cli.RunStatusEnhanced(jsonOutput)
	case "inspect":
		jsonOutput := false
		fullOutput := false
		remaining := extractPort(args[1:])
		for i := 0; i < len(remaining); i++ {
			switch remaining[i] {
			case "--json":
				jsonOutput = true
			case "--full":
				fullOutput = true
			case "--live":
				// backward compat, now default behavior for live inspect
			case "--format":
				if i+1 < len(remaining) {
					if remaining[i+1] == "json" {
						jsonOutput = true
					}
					i++
				}
			default:
				// Try as positional port argument
				if p, perr := strconv.Atoi(remaining[i]); perr == nil {
					cli.SetPort(p)
				}
			}
		}
		err = cli.RunInspectEnhanced(jsonOutput, fullOutput)
	case "ps":
		showAll := false
		for _, arg := range args[1:] {
			if arg == "--all" || arg == "-a" {
				showAll = true
			}
		}
		err = cli.RunPS(showAll)
	case "pause":
		extractPort(args[1:])
		err = cli.RunPause()
	case "resume":
		extractPort(args[1:])
		err = cli.RunResume()
	case "statusline":
		err = cli.RunStatusline()
	case "install-statusline":
		err = cli.RunInstallStatusline()
	case "uninstall-statusline":
		err = cli.RunUninstallStatusline()
	case "install-skill":
		err = cli.RunInstallSkill(args[1:])
	case "uninstall-skill":
		err = cli.RunUninstallSkill(args[1:])
	case "rules":
		err = runRulesCommand(args[1:])
	case "session":
		err = runSessionCommand(args[1:])
	case "data":
		err = runDataCommand(args[1:])
	case "compress":
		var ids []string
		remaining := extractPort(args[1:])
		for i := 0; i < len(remaining); i++ {
			if remaining[i] == "--ids" && i+1 < len(remaining) {
				ids = strings.Split(remaining[i+1], ",")
				i++
			}
		}
		if len(ids) == 0 {
			fmt.Fprintln(os.Stderr, "[wet] error: --ids required. Usage: wet compress --ids id1,id2,id3")
			os.Exit(1)
		}
		err = cli.RunCompress(ids)
	default:
		printUsage(os.Stderr)
		os.Exit(1)
	}

	if err == nil {
		return
	}

	if ec, ok := err.(exitCoder); ok {
		os.Exit(ec.ExitCode())
	}

	fmt.Fprintf(os.Stderr, "[wet] %v\n", err)
	os.Exit(1)
}

func runRulesCommand(args []string) error {
	args = extractPort(args)
	if len(args) == 0 {
		printUsage(os.Stderr)
		os.Exit(1)
	}

	switch args[0] {
	case "list":
		return cli.RunRulesList()
	case "set":
		if len(args) != 3 {
			printUsage(os.Stderr)
			os.Exit(1)
		}
		return cli.RunRulesSet(args[1], args[2])
	default:
		printUsage(os.Stderr)
		os.Exit(1)
	}

	return nil
}

// extractPort scans args for --port N, calls cli.SetPort, and returns remaining args.
func extractPort(args []string) []string {
	var remaining []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--port" && i+1 < len(args) {
			port, err := strconv.Atoi(args[i+1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "[wet] invalid --port value: %s\n", args[i+1])
				os.Exit(1)
			}
			cli.SetPort(port)
			i++ // skip value
		} else {
			remaining = append(remaining, args[i])
		}
	}
	return remaining
}

func runSessionCommand(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stderr)
		os.Exit(1)
	}
	switch args[0] {
	case "salt":
		return cli.RunSessionSalt()
	case "find":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "[wet] error: usage: wet session find <SALT>")
			os.Exit(1)
		}
		return cli.RunSessionFind(args[1])
	case "profile":
		remaining := extractPort(args[1:])
		var jsonlPath string
		for i := 0; i < len(remaining); i++ {
			if remaining[i] == "--jsonl" && i+1 < len(remaining) {
				jsonlPath = remaining[i+1]
				i++
			}
		}
		if jsonlPath == "" {
			fmt.Fprintln(os.Stderr, "[wet] error: usage: wet session profile --jsonl <PATH> [--port PORT]")
			os.Exit(1)
		}
		// Port is already extracted by extractPort into cli.overridePort.
		// Pass 0 if no port was set (offline mode).
		return cli.RunSessionProfile(jsonlPath, cli.GetPort())
	default:
		printUsage(os.Stderr)
		os.Exit(1)
	}
	return nil
}

func runDataCommand(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stderr)
		os.Exit(1)
	}

	switch args[0] {
	case "status":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "[wet] error: usage: wet data status")
			os.Exit(1)
		}
		return cli.RunSessionStatus()
	case "inspect":
		showAll := false
		for _, arg := range args[1:] {
			if arg == "--all" {
				showAll = true
				continue
			}
			fmt.Fprintf(os.Stderr, "[wet] error: unknown argument for data inspect: %s\n", arg)
			os.Exit(1)
		}
		return cli.RunSessionInspect(showAll)
	case "diff":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "[wet] error: usage: wet data diff <turn>")
			os.Exit(1)
		}
		turnNum, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "[wet] error: invalid turn number %q\n", args[1])
			os.Exit(1)
		}
		return cli.RunSessionDiff(turnNum)
	default:
		printUsage(os.Stderr)
		os.Exit(1)
	}

	return nil
}

func printUsage(out *os.File) {
	_, _ = fmt.Fprint(out, usageText)
}
