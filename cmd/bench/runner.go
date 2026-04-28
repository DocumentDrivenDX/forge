package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	agent "github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/comparison"
	agentConfig "github.com/DocumentDrivenDX/agent/internal/config"
	"github.com/DocumentDrivenDX/agent/internal/harnesses"
)

// CostCapExceededError is returned (via RunResult.Error) when the accumulated
// cost across the bench sweep would exceed --max-cost-usd before a task runs.
const CostCapSkipReason = "skipped: cost cap"

// DeterministicSamplingNotice is surfaced in bench output so result artifacts
// state the sampling controls used for parity runs.
const DeterministicSamplingNotice = "deterministic sampling requested: temperature=0 and per-task seed=base-seed+task-index; harnesses/providers that ignore seed remain advisory"

// buildRunFuncWithCap constructs a comparison.RunFunc that drives agent
// execution via service.Execute, enforcing an optional cost cap. When
// maxCostUSD > 0 and accumulated cost would exceed the cap, the run function
// skips the task and returns a result with Error = CostCapSkipReason.
// The seed parameter is used as the base for per-task ServiceExecuteRequest
// seeds, with temperature fixed at 0 for deterministic sampling.
func buildRunFuncWithCap(wd string, timeout time.Duration, maxCostUSD float64, baseSeed int64) (comparison.RunFunc, error) {
	return buildRunFunc(wd, timeout, maxCostUSD, baseSeed)
}

// buildRunFunc constructs a comparison.RunFunc that drives agent execution via
// service.Execute. The RunFunc signature is (harness, model, prompt) ->
// RunResult per CONTRACT-003.
func buildRunFunc(wd string, timeout time.Duration, maxCostUSD float64, baseSeed int64) (comparison.RunFunc, error) {
	cfg, err := agentConfig.Load(wd)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	svc, err := agent.New(agent.ServiceOptions{
		ServiceConfig: &configAdapter{cfg: cfg, workDir: wd},
	})
	if err != nil {
		return nil, fmt.Errorf("new service: %w", err)
	}

	var (
		mu          sync.Mutex
		accumulated float64
		taskIndex   int64
	)

	return func(harness, model, prompt string) comparison.RunResult {
		result := comparison.RunResult{
			Harness: harness,
			Model:   model,
		}

		// Pre-flight cost cap check: if we already know accumulated cost is
		// at or beyond the cap, skip without invoking the provider.
		if maxCostUSD > 0 {
			mu.Lock()
			acc := accumulated
			mu.Unlock()
			if acc >= maxCostUSD {
				result.Error = CostCapSkipReason
				result.ExitCode = -1
				return result
			}
		}

		// Record the per-task seed for reproducibility. The seed is derived
		// from baseSeed + monotonic task counter.
		mu.Lock()
		taskIdx := taskIndex
		taskIndex++
		mu.Unlock()
		seed := baseSeed + taskIdx

		ctx := context.Background()
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		start := time.Now()

		// bench corpus runs pin temperature=0 + explicit seed for
		// reproducibility (per CONTRACT-003 the wire emits 0 explicitly,
		// distinct from "unset"). Per ADR-007 this is now a deliberate
		// override — bench wants greedy decoding so its corpus comparisons
		// are byte-stable across runs.
		bTemp := float32(0)
		bSeed := seed
		req := agent.ServiceExecuteRequest{
			Harness:     harness,
			Model:       model,
			Prompt:      prompt,
			WorkDir:     wd,
			Temperature: &bTemp,
			Seed:        &bSeed,
			// Use safe permissions for bench corpus tasks.
			Permissions: "safe",
		}

		ch, err := svc.Execute(ctx, req)
		if err != nil {
			result.Error = err.Error()
			result.ExitCode = 1
			result.DurationMS = int(time.Since(start).Milliseconds())
			return result
		}

		var outputBuf strings.Builder
		for ev := range ch {
			switch ev.Type {
			case harnesses.EventTypeTextDelta:
				var td harnesses.TextDeltaData
				if err := json.Unmarshal(ev.Data, &td); err == nil {
					outputBuf.WriteString(td.Text)
				}
			case harnesses.EventTypeToolCall:
				var tc harnesses.ToolCallData
				if err := json.Unmarshal(ev.Data, &tc); err == nil {
					var inputStr string
					if tc.Input != nil {
						inputStr = string(tc.Input)
					}
					result.ToolCalls = append(result.ToolCalls, comparison.ToolCallEntry{
						Tool:  tc.Name,
						Input: inputStr,
					})
				}
			case harnesses.EventTypeFinal:
				var fd harnesses.FinalData
				if err := json.Unmarshal(ev.Data, &fd); err == nil {
					result.ExitCode = fd.ExitCode
					result.Error = fd.Error
					if fd.Usage != nil {
						result.InputTokens = usageInt(fd.Usage.InputTokens)
						result.OutputTokens = usageInt(fd.Usage.OutputTokens)
						result.Tokens = usageInt(fd.Usage.TotalTokens)
					}
					result.CostUSD = fd.CostUSD
				}
			}
		}

		result.Output = outputBuf.String()
		result.DurationMS = int(time.Since(start).Milliseconds())

		// Accumulate cost after the task completes.
		if result.CostUSD > 0 {
			mu.Lock()
			accumulated += result.CostUSD
			mu.Unlock()
		}

		return result
	}, nil
}

// cmdRun implements the 'run' subcommand.
func cmdRun(args []string) int {
	fs := flagSet("run")
	corpusDir := fs.String("corpus", "", "Corpus directory (default: bench/corpus relative to work-dir)")
	workDir := fs.String("work-dir", "", "Agent working directory (default: cwd)")
	jsonOut := fs.Bool("json", false, "Emit JSON results")
	harnessFilter := fs.String("harness", "", "Only run against this harness")
	maxCostUSD := fs.Float64("max-cost-usd", 0.50, "Cost cap in USD; sweep halts when accumulated cost reaches this limit (0 = no cap)")
	timeoutSec := fs.Int("timeout", 120, "Per-task timeout in seconds")
	resultsDir := fs.String("results-dir", "", "Directory to write result JSON (default: bench/results relative to work-dir)")
	external := fs.String("external", "", "External benchmark adapter (currently supported: termbench)")
	externalSubset := fs.String("external-subset", "", "Path to external benchmark subset manifest (default: scripts/beadbench/external/<adapter>-subset.json)")
	externalTasks := fs.String("external-tasks-dir", "", "Path to external benchmark tasks directory (default: scripts/benchmark/external/terminal-bench/tasks)")
	externalHarness := fs.String("external-harness", "ddx-agent", "Harness label for external benchmark runs")
	externalModel := fs.String("external-model", "", "Model ID for external benchmark runs")
	externalPerms := fs.String("external-permissions", "", "Permissions preset override for external runs (default: trusted)")
	externalMax := fs.Int("external-max-tasks", 0, "Cap external benchmark to first N tasks (0 = no cap)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	wd := resolveWorkDir(*workDir)

	if *external != "" {
		switch *external {
		case "termbench":
			return runExternalTermbench(externalRunOptions{
				workDir:     wd,
				subsetPath:  *externalSubset,
				tasksDir:    *externalTasks,
				harness:     *externalHarness,
				model:       *externalModel,
				permissions: *externalPerms,
				maxTasks:    *externalMax,
			})
		default:
			fmt.Fprintf(os.Stderr, "%s run: unknown --external=%q (supported: termbench)\n", benchCommandName(), *external)
			return 2
		}
	}

	// Resolve corpus directory.
	corpus := *corpusDir
	if corpus == "" {
		corpus = filepath.Join(wd, "bench", "corpus")
	}

	// Resolve results directory.
	outDir := *resultsDir
	if outDir == "" {
		outDir = filepath.Join(wd, "bench", "results")
	}
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "%s run: create results dir: %v\n", benchCommandName(), err)
		return 1
	}

	// Load corpus tasks.
	tasks, err := loadCorpus(corpus)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s run: load corpus: %v\n", benchCommandName(), err)
		return 1
	}
	if len(tasks) == 0 {
		fmt.Fprintf(os.Stderr, "%s run: no tasks found in corpus\n", benchCommandName())
		return 1
	}

	// Discover candidates.
	candidates, err := discoverCandidates(wd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s run: discover: %v\n", benchCommandName(), err)
		return 1
	}

	// Apply harness filter.
	if *harnessFilter != "" {
		var filtered []Candidate
		for _, c := range candidates {
			if c.Harness == *harnessFilter {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
	}

	if len(candidates) == 0 {
		fmt.Fprintf(os.Stderr, "%s run: no candidates available\n", benchCommandName())
		return 1
	}

	timeout := time.Duration(*timeoutSec) * time.Second
	baseSeed := time.Now().UnixNano()
	runFn, err := buildRunFunc(wd, timeout, *maxCostUSD, baseSeed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s run: build runner: %v\n", benchCommandName(), err)
		return 1
	}
	if *maxCostUSD > 0 {
		fmt.Fprintf(os.Stderr, "%s: cost cap: $%.4f  base-seed: %d  note: %s\n",
			benchCommandName(), *maxCostUSD, baseSeed, DeterministicSamplingNotice)
	}

	// Build a BenchmarkSuite from corpus tasks + candidates.
	suite := buildSuite(tasks, candidates)
	result, err := comparison.RunBenchmark(runFn, suite)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s run: benchmark: %v\n", benchCommandName(), err)
		return 1
	}

	// Save results.
	outPath := filepath.Join(outDir, fmt.Sprintf("bench-%d.json", time.Now().Unix()))
	if err := comparison.SaveBenchmarkResult(outPath, result); err != nil {
		fmt.Fprintf(os.Stderr, "%s run: save: %v\n", benchCommandName(), err)
		return 1
	}

	if *jsonOut {
		data, _ := json.MarshalIndent(result.Summary, "", "  ")
		fmt.Println(string(data))
	} else {
		printSummaryTable(result)
		fmt.Printf("\nResults written to: %s\n", outPath)
	}

	return 0
}

// buildSuite converts corpus tasks and candidates into a BenchmarkSuite.
func buildSuite(tasks []CorpusTask, candidates []Candidate) *comparison.BenchmarkSuite {
	arms := make([]comparison.BenchmarkArm, 0, len(candidates))
	for _, c := range candidates {
		label := c.Harness
		if c.Provider != "" {
			label = c.Harness + "/" + c.Provider
		}
		if c.Model != "" {
			label += "/" + c.Model
		}
		arms = append(arms, comparison.BenchmarkArm{
			Label:   label,
			Harness: c.Harness,
			Model:   c.Model,
		})
	}

	prompts := make([]comparison.BenchmarkPrompt, 0, len(tasks))
	for _, t := range tasks {
		prompts = append(prompts, comparison.BenchmarkPrompt{
			ID:          t.ID,
			Name:        t.Description,
			Description: t.Description,
			Prompt:      t.Prompt,
			Tags:        t.Tags,
		})
	}

	return &comparison.BenchmarkSuite{
		Name:    benchCommandName(),
		Version: "1",
		Arms:    arms,
		Prompts: prompts,
	}
}

func usageInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
