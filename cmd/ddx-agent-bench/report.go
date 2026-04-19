package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DocumentDrivenDX/agent/internal/comparison"
)

// cmdReport implements the 'report' subcommand.
func cmdReport(args []string) int {
	fs := flagSet("report")
	resultsDir := fs.String("results-dir", "", "Directory containing result JSON files (default: bench/results relative to cwd)")
	format := fs.String("format", "table", "Output format: table|json|markdown")
	workDir := fs.String("work-dir", "", "Agent working directory (default: cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	wd := resolveWorkDir(*workDir)

	dir := *resultsDir
	if dir == "" {
		dir = filepath.Join(wd, "bench", "results")
	}

	// Collect all bench-*.json files.
	entries, err := filepath.Glob(filepath.Join(dir, "bench-*.json"))
	if err != nil || len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "ddx-agent-bench report: no result files found in %s\n", dir)
		return 1
	}

	// Sort descending — newest first.
	sort.Sort(sort.Reverse(sort.StringSlice(entries)))

	// Load the most recent result by default.
	path := entries[0]
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ddx-agent-bench report: read %s: %v\n", path, err)
		return 1
	}

	var result comparison.BenchmarkResult
	if err := json.Unmarshal(data, &result); err != nil {
		fmt.Fprintf(os.Stderr, "ddx-agent-bench report: parse %s: %v\n", path, err)
		return 1
	}

	switch *format {
	case "json":
		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))
	case "markdown":
		printMarkdownReport(&result)
	default:
		printSummaryTable(&result)
	}

	return 0
}

// printSummaryTable renders a human-readable table to stdout.
func printSummaryTable(result *comparison.BenchmarkResult) {
	fmt.Printf("Benchmark: %s  version: %s  at: %s\n\n",
		result.Suite, result.Version, result.Timestamp.Format("2006-01-02 15:04:05Z"))

	fmt.Printf("%-40s %8s %8s %12s %12s %12s\n",
		"ARM", "OK", "FAIL", "TOK", "COST_USD", "AVG_MS")
	fmt.Printf("%-40s %8s %8s %12s %12s %12s\n",
		strings.Repeat("-", 40), "-------", "-------",
		"-----------", "-----------", "-----------")
	for _, arm := range result.Summary.Arms {
		fmt.Printf("%-40s %8d %8d %12d %12.6f %12d\n",
			truncate(arm.Label, 40),
			arm.Completed, arm.Failed,
			arm.TotalTokens,
			arm.TotalCostUSD,
			arm.AvgDurationMS,
		)
	}
	fmt.Printf("\nTotal prompts: %d\n", result.Summary.TotalPrompts)

	printCostCapSkips(result)
	printParityMatrix(result)
}

// printCostCapSkips lists any tasks that were skipped due to cost cap.
func printCostCapSkips(result *comparison.BenchmarkResult) {
	type skip struct {
		prompt string
		arm    string
	}
	var skips []skip
	for _, cmp := range result.Comparisons {
		for _, arm := range cmp.Arms {
			if arm.Error == CostCapSkipReason {
				skips = append(skips, skip{prompt: cmp.ID, arm: arm.Harness})
			}
		}
	}
	if len(skips) == 0 {
		return
	}
	fmt.Printf("\n--- Cost cap skips (%d) ---\n", len(skips))
	for _, s := range skips {
		fmt.Printf("  skipped: prompt=%s  arm=%s\n", s.prompt, s.arm)
	}
}

// printParityMatrix prints a per-task parity comparison between all arm pairs.
func printParityMatrix(result *comparison.BenchmarkResult) {
	if len(result.Arms) < 2 || len(result.Comparisons) == 0 {
		return
	}

	// Build arm labels slice.
	labels := make([]string, len(result.Arms))
	for i, a := range result.Arms {
		labels[i] = a.Label
	}

	fmt.Printf("\n--- Parity matrix (tool-call sequence equality / output similarity) ---\n")
	fmt.Printf("%-20s  %-30s  %-30s  %8s  %10s\n", "PROMPT", "ARM_A", "ARM_B", "TC_EQUAL", "OUT_SIM")
	fmt.Printf("%-20s  %-30s  %-30s  %8s  %10s\n",
		strings.Repeat("-", 20), strings.Repeat("-", 30), strings.Repeat("-", 30), "--------", "----------")

	for _, cmp := range result.Comparisons {
		cells := BuildParityMatrix(cmp, labels)
		for _, cell := range cells {
			eq := "NO"
			if cell.ToolSeqEqual {
				eq = "YES"
			}
			fmt.Printf("%-20s  %-30s  %-30s  %8s  %10.3f\n",
				truncate(cmp.ID, 20),
				truncate(cell.ArmA, 30),
				truncate(cell.ArmB, 30),
				eq,
				cell.OutputSimilarity,
			)
		}
	}

	// Non-determinism notice.
	fmt.Printf("\nNOTE: %s\n", NonDeterministicNotice)
	fmt.Printf("      Parity results for non-deterministic harnesses are advisory only.\n")
}

// printMarkdownReport renders a GitHub-flavored Markdown report.
func printMarkdownReport(result *comparison.BenchmarkResult) {
	fmt.Printf("## Benchmark: %s\n\n", result.Suite)
	fmt.Printf("- **Version**: %s\n", result.Version)
	fmt.Printf("- **Run at**: %s\n", result.Timestamp.Format("2006-01-02 15:04:05Z"))
	fmt.Printf("- **Prompts**: %d\n\n", result.Summary.TotalPrompts)

	fmt.Println("| Arm | OK | Fail | Tokens | Cost USD | Avg ms |")
	fmt.Println("|-----|---:|---:|---:|---:|---:|")
	for _, arm := range result.Summary.Arms {
		fmt.Printf("| %s | %d | %d | %d | %.6f | %d |\n",
			arm.Label, arm.Completed, arm.Failed,
			arm.TotalTokens, arm.TotalCostUSD, arm.AvgDurationMS)
	}

	// Cost cap skips section.
	var skipLines []string
	for _, cmp := range result.Comparisons {
		for _, arm := range cmp.Arms {
			if arm.Error == CostCapSkipReason {
				skipLines = append(skipLines, fmt.Sprintf("- prompt `%s`, arm `%s`", cmp.ID, arm.Harness))
			}
		}
	}
	if len(skipLines) > 0 {
		fmt.Printf("\n### Cost cap skips\n\n")
		for _, l := range skipLines {
			fmt.Println(l)
		}
	}

	// Parity matrix section.
	if len(result.Arms) >= 2 && len(result.Comparisons) > 0 {
		labels := make([]string, len(result.Arms))
		for i, a := range result.Arms {
			labels[i] = a.Label
		}

		fmt.Printf("\n### Parity matrix\n\n")
		fmt.Println("| Prompt | Arm A | Arm B | TC Equal | Out Sim |")
		fmt.Println("|--------|-------|-------|:--------:|--------:|")
		for _, cmp := range result.Comparisons {
			for _, cell := range BuildParityMatrix(cmp, labels) {
				eq := "no"
				if cell.ToolSeqEqual {
					eq = "**yes**"
				}
				fmt.Printf("| %s | %s | %s | %s | %.3f |\n",
					cmp.ID, cell.ArmA, cell.ArmB, eq, cell.OutputSimilarity)
			}
		}

		fmt.Printf("\n> **Note**: %s  \n", NonDeterministicNotice)
		fmt.Printf("> Parity results for non-deterministic harnesses are advisory only.\n")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
