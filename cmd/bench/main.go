// Command ddx-agent-bench discovers (harness, provider, model) candidates from
// agent config and runs a corpus of small tasks to produce per-task metrics.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 2
	}

	switch args[0] {
	case "discover":
		return cmdDiscover(args[1:])
	case "run":
		return cmdRun(args[1:])
	case "report":
		return cmdReport(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "ddx-agent-bench: unknown command %q\n", args[0])
		printUsage()
		return 2
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: ddx-agent-bench <command> [flags]

Commands:
  discover   List discovered (harness, provider, model) candidates
  run        Run corpus against discovered candidates
  report     Render a results file as table, json, or markdown

Run 'ddx-agent-bench <command> -h' for command-specific flags.
`)
}

// resolveWorkDir returns the working directory: --work-dir flag or cwd.
func resolveWorkDir(wd string) string {
	if wd != "" {
		return wd
	}
	d, err := os.Getwd()
	if err != nil {
		return "."
	}
	return d
}

// flagSet creates a FlagSet that writes to stderr with ContinueOnError.
func flagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}
