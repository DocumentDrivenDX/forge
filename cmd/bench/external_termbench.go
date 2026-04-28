package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	agent "github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/benchmark/external/termbench"
	agentConfig "github.com/DocumentDrivenDX/agent/internal/config"
)

// termbenchSubsetEntry is one row from scripts/beadbench/external/termbench-subset.json.
type termbenchSubsetEntry struct {
	ID         string   `json:"id"`
	Category   string   `json:"category,omitempty"`
	Difficulty string   `json:"difficulty,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Rationale  string   `json:"rationale,omitempty"`
}

type termbenchSubset struct {
	Version       string                 `json:"version"`
	Captured      string                 `json:"captured"`
	Dataset       string                 `json:"dataset"`
	DatasetRepo   string                 `json:"dataset_repo"`
	DatasetCommit string                 `json:"dataset_commit"`
	SelectionRule string                 `json:"selection_rule"`
	Tasks         []termbenchSubsetEntry `json:"tasks"`
}

// termbenchTaskRunSummary is one row in benchmark-results/termbench/<run>/results.json.
// Fields chosen to be reporter-friendly (jq, simple table renderers).
type termbenchTaskRunSummary struct {
	TaskID       string  `json:"task_id"`
	Category     string  `json:"category,omitempty"`
	Difficulty   string  `json:"difficulty,omitempty"`
	Harness      string  `json:"harness"`
	Model        string  `json:"model"`
	ExitCode     int     `json:"exit_code"`
	DurationMS   int64   `json:"duration_ms"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Reward       *int    `json:"reward,omitempty"` // nil = not graded yet
	Status       string  `json:"status"`           // ran|skipped|error|graded
	Error        string  `json:"error,omitempty"`
	Trajectory   string  `json:"trajectory_path,omitempty"`
}

type termbenchRunReport struct {
	RunID     string                    `json:"run_id"`
	StartedAt time.Time                 `json:"started_at"`
	Subset    string                    `json:"subset_path"`
	TasksDir  string                    `json:"tasks_dir"`
	Harness   string                    `json:"harness"`
	Model     string                    `json:"model"`
	Results   []termbenchTaskRunSummary `json:"results"`
	Notes     []string                  `json:"notes,omitempty"`
}

// runExternalTermbench is the cmd/bench entry point for `--external=termbench`.
// It is invoked from cmdRun when the flag is set; see runner.go.
//
// What it does:
//
//  1. Loads the frozen subset manifest (default
//     scripts/beadbench/external/termbench-subset.json).
//  2. For each entry, locates the upstream task directory under
//     scripts/benchmark/external/terminal-bench(-2)/tasks/<id>/.
//  3. Builds an ExecutionPlan and feeds it to agent.Service.Execute.
//  4. Captures harness events into ATIF v1.4 trajectory and writes
//     them to benchmark-results/termbench/<run-id>/<task>/logs/agent/.
//  5. Reads any verifier output (reward.txt) the upstream grader has
//     placed in the same directory and folds the verdict into the
//     report. We do NOT run the grader ourselves — running pytest in
//     the task's Docker image is upstream's responsibility (see SD-008).
//
// On a clean machine with no Docker stack, runs will complete with
// status=ran and no reward — that is the honest outcome the bead
// describes ("don't fake passing tests").
func runExternalTermbench(opts externalRunOptions) int {
	tasksDir := opts.tasksDir
	if tasksDir == "" {
		// terminal-bench-2 layout: task directories live at the repo
		// root (each task is a top-level directory), not under tasks/.
		// SD-008 §1 records this. The legacy TB1 path is also probed.
		candidates := []string{
			filepath.Join(opts.workDir, "scripts", "benchmark", "external", "terminal-bench-2"),
			filepath.Join(opts.workDir, "scripts", "benchmark", "external", "terminal-bench", "tasks"),
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				tasksDir = c
				break
			}
		}
	}
	subsetPath := opts.subsetPath
	if subsetPath == "" {
		subsetPath = filepath.Join(opts.workDir, "scripts", "beadbench", "external", "termbench-subset.json")
	}

	subset, err := loadTermbenchSubset(subsetPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s run --external=termbench: load subset %s: %v\n", benchCommandName(), subsetPath, err)
		return 1
	}

	// Build the agent service once; reuse across tasks.
	cfg, err := agentConfig.Load(opts.workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s run --external=termbench: load config: %v\n", benchCommandName(), err)
		return 1
	}
	svc, err := agent.New(agent.ServiceOptions{
		ServiceConfig: &configAdapter{cfg: cfg, workDir: opts.workDir},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s run --external=termbench: new service: %v\n", benchCommandName(), err)
		return 1
	}

	runID := fmt.Sprintf("termbench-%d", time.Now().Unix())
	outBase := filepath.Join(opts.workDir, "benchmark-results", "termbench", runID)
	if err := os.MkdirAll(outBase, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "%s run --external=termbench: mkdir: %v\n", benchCommandName(), err)
		return 1
	}

	report := termbenchRunReport{
		RunID:     runID,
		StartedAt: time.Now().UTC(),
		Subset:    subsetPath,
		TasksDir:  tasksDir,
		Harness:   opts.harness,
		Model:     opts.model,
	}

	maxTasks := opts.maxTasks
	if maxTasks <= 0 || maxTasks > len(subset.Tasks) {
		maxTasks = len(subset.Tasks)
	}

	for i, entry := range subset.Tasks[:maxTasks] {
		summary := termbenchTaskRunSummary{
			TaskID:     entry.ID,
			Category:   entry.Category,
			Difficulty: entry.Difficulty,
			Harness:    opts.harness,
			Model:      opts.model,
		}
		taskOutDir := filepath.Join(outBase, entry.ID)
		taskDir := filepath.Join(tasksDir, entry.ID)
		if _, err := os.Stat(taskDir); err != nil {
			summary.Status = "skipped"
			summary.Error = fmt.Sprintf("task dir not found: %s (submodule not initialized?)", taskDir)
			report.Results = append(report.Results, summary)
			fmt.Fprintf(os.Stderr, "[%d/%d] %s: %s\n", i+1, maxTasks, entry.ID, summary.Error)
			continue
		}
		task, err := termbench.LoadTask(taskDir)
		if err != nil {
			summary.Status = "error"
			summary.Error = err.Error()
			report.Results = append(report.Results, summary)
			fmt.Fprintf(os.Stderr, "[%d/%d] %s: load task: %v\n", i+1, maxTasks, entry.ID, err)
			continue
		}
		if err := os.MkdirAll(taskOutDir, 0o750); err != nil {
			summary.Status = "error"
			summary.Error = err.Error()
			report.Results = append(report.Results, summary)
			continue
		}
		// For the Go-side dry-run we operate the agent against a fresh
		// per-task tempdir. Real Harbor runs would mount the container
		// workspace; here we just need a writable cwd for the agent.
		workDir, err := os.MkdirTemp("", "termbench-"+entry.ID+"-")
		if err != nil {
			summary.Status = "error"
			summary.Error = err.Error()
			report.Results = append(report.Results, summary)
			continue
		}
		defer os.RemoveAll(workDir)

		plan := termbench.BuildPlan(task, termbench.PlanOptions{
			Harness:     opts.harness,
			Model:       opts.model,
			WorkDir:     workDir,
			Permissions: opts.permissions,
		})
		ctx, cancel := context.WithTimeout(context.Background(), plan.Timeout)
		start := time.Now()
		ch, err := svc.Execute(ctx, plan.Request)
		if err != nil {
			cancel()
			summary.Status = "error"
			summary.Error = err.Error()
			summary.DurationMS = time.Since(start).Milliseconds()
			report.Results = append(report.Results, summary)
			fmt.Fprintf(os.Stderr, "[%d/%d] %s: execute: %v\n", i+1, maxTasks, entry.ID, err)
			continue
		}
		traj := termbench.Capture(ch, termbench.CaptureOptions{
			SessionID: runID + "/" + entry.ID,
			TaskID:    entry.ID,
			Agent: termbench.AgentInfo{
				Name:      opts.harness,
				Version:   "bench",
				ModelName: opts.model,
			},
			StartedAt: start,
		})
		cancel()
		if err := termbench.WriteHarnessOutput(taskOutDir, traj); err != nil {
			summary.Status = "error"
			summary.Error = err.Error()
		} else {
			summary.Status = "ran"
			summary.Trajectory = filepath.Join(taskOutDir, "logs", "agent", "trajectory.json")
		}
		summary.ExitCode = traj.ExitCode
		summary.DurationMS = traj.DurationMS
		summary.InputTokens = traj.FinalMetrics.InputTokens
		summary.OutputTokens = traj.FinalMetrics.OutputTokens
		summary.CostUSD = traj.FinalMetrics.Cost

		// If the upstream verifier already produced a reward (e.g. a
		// previous Harbor pass dropped it into the same dir), surface
		// it. Absent reward stays nil so reporters can distinguish
		// "not graded" from "graded as failed".
		if g, gerr := termbench.ReadGraderResult(entry.ID, taskOutDir); gerr == nil {
			r := g.Reward
			summary.Reward = &r
			summary.Status = "graded"
		} else if !errors.Is(gerr, termbench.ErrNoVerifierOutput) {
			summary.Error = strings.Join([]string{summary.Error, gerr.Error()}, "; ")
		}

		report.Results = append(report.Results, summary)
		fmt.Fprintf(os.Stderr, "[%d/%d] %s: status=%s exit=%d duration=%dms\n",
			i+1, maxTasks, entry.ID, summary.Status, summary.ExitCode, summary.DurationMS)
	}

	if len(report.Notes) == 0 {
		report.Notes = []string{
			"Reward fields are populated only when an upstream Harbor verifier has written /logs/verifier/reward.txt into the per-task output directory.",
			"To complete grading, run: scripts/benchmark/run_benchmark.sh after this command (Docker required).",
		}
	}

	reportPath := filepath.Join(outBase, "results.json")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s run --external=termbench: marshal report: %v\n", benchCommandName(), err)
		return 1
	}
	if err := os.WriteFile(reportPath, data, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "%s run --external=termbench: write report: %v\n", benchCommandName(), err)
		return 1
	}
	fmt.Printf("termbench results: %s\n", reportPath)
	return 0
}

// externalRunOptions is the parsed set of flags consumed by external
// benchmark adapters.
type externalRunOptions struct {
	workDir     string
	subsetPath  string
	tasksDir    string
	harness     string
	model       string
	permissions string
	maxTasks    int
}

func loadTermbenchSubset(path string) (*termbenchSubset, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is operator-controlled flag
	if err != nil {
		return nil, err
	}
	var s termbenchSubset
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse subset: %w", err)
	}
	if len(s.Tasks) == 0 {
		return nil, fmt.Errorf("subset has no tasks")
	}
	return &s, nil
}
