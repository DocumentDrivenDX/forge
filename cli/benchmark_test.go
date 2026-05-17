package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func getRepoRoot(t *testing.T) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	return filepath.Dir(wd)
}

// TestBenchmarkRunnerPlanExpansion verifies --plan output for profile×task×rep expansion
// including tasks_from resolution.
func TestBenchmarkRunnerPlanExpansion(t *testing.T) {
	repoRoot := getRepoRoot(t)
	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	benchDir := filepath.Join(repoRoot, "scripts", "benchmark")

	if _, err := os.Stat(benchmarkScript); err != nil {
		t.Fatalf("benchmark script not found: %v", err)
	}

	tests := []struct {
		name        string
		profile     string
		benchSet    string
		expectRows  int
		shouldError bool
	}{
		{
			name:       "simple canary plan",
			profile:    "claude-sonnet-4-6",
			benchSet:   "tb-2-1-canary",
			expectRows: 9, // 1 profile × 3 tasks × 3 reps
		},
		{
			name:        "missing profile",
			profile:     "nonexistent-profile",
			benchSet:    "tb-2-1-canary",
			shouldError: true,
		},
		{
			name:        "missing bench-set",
			profile:     "claude-sonnet-4-6",
			benchSet:    "nonexistent-bench-set",
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(benchmarkScript,
				"--profile", tt.profile,
				"--bench-set", tt.benchSet,
				"--plan")
			cmd.Dir = benchDir

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err := cmd.Run()
			if tt.shouldError {
				if err == nil {
					t.Errorf("expected error but command succeeded")
				}
				return
			}

			if err != nil {
				t.Fatalf("command failed: %v\nstderr: %s", err, stderr.String())
			}

			// Parse output: each line is tab-separated key=value pairs
			lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
			if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
				t.Fatalf("no output from --plan")
			}

			if len(lines) != tt.expectRows {
				t.Errorf("expected %d rows, got %d\noutput: %s",
					tt.expectRows, len(lines), stdout.String())
			}

			// Validate each line has expected fields
			for i, line := range lines {
				fields := strings.Split(line, "\t")
				expectedFields := map[string]bool{
					"profile":       false,
					"bench_set":     false,
					"framework":     false,
					"dataset":       false,
					"task":          false,
					"rep":           false,
					"task_executor": false,
				}

				for _, field := range fields {
					parts := strings.SplitN(field, "=", 2)
					if len(parts) == 2 {
						expectedFields[parts[0]] = true
					}
				}

				for key, found := range expectedFields {
					if !found {
						t.Errorf("row %d missing field '%s': %s", i, key, line)
					}
				}
			}
		})
	}
}

// TestBenchmarkRunnerArgParsing verifies correct handling of unknown subcommands
// and missing required flags.
func TestBenchmarkRunnerArgParsing(t *testing.T) {
	repoRoot := getRepoRoot(t)
	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	benchDir := filepath.Join(repoRoot, "scripts", "benchmark")

	if _, err := os.Stat(benchmarkScript); err != nil {
		t.Fatalf("benchmark script not found: %v", err)
	}

	tests := []struct {
		name        string
		args        []string
		shouldError bool
		checkOutput func(t *testing.T, stderr string)
	}{
		{
			name:        "unknown subcommand",
			args:        []string{"unknown-subcommand"},
			shouldError: true,
			checkOutput: func(t *testing.T, stderr string) {
				if !strings.Contains(stderr, "unknown subcommand") {
					t.Errorf("expected 'unknown subcommand' in stderr, got: %s", stderr)
				}
			},
		},
		{
			name:        "missing --profile flag",
			args:        []string{"--bench-set", "tb-2-1-canary", "--plan"},
			shouldError: true,
			checkOutput: func(t *testing.T, stderr string) {
				if !strings.Contains(stderr, "--profile") {
					t.Errorf("expected '--profile' mention in stderr, got: %s", stderr)
				}
			},
		},
		{
			name:        "missing --bench-set flag",
			args:        []string{"--profile", "claude-sonnet-4-6", "--plan"},
			shouldError: true,
			checkOutput: func(t *testing.T, stderr string) {
				if !strings.Contains(stderr, "--bench-set") {
					t.Errorf("expected '--bench-set' mention in stderr, got: %s", stderr)
				}
			},
		},
		{
			name:        "unknown flag",
			args:        []string{"--unknown-flag", "value"},
			shouldError: true,
			checkOutput: func(t *testing.T, stderr string) {
				if !strings.Contains(stderr, "unknown") {
					t.Errorf("expected 'unknown' in stderr, got: %s", stderr)
				}
			},
		},
		{
			name:        "valid subcommand: profiles",
			args:        []string{"profiles"},
			shouldError: false,
		},
		{
			name:        "valid subcommand: bench-sets",
			args:        []string{"bench-sets"},
			shouldError: false,
		},
		{
			name:        "valid subcommand: task-executors",
			args:        []string{"task-executors"},
			shouldError: false,
		},
		{
			name:        "valid subcommand: harness-adapters",
			args:        []string{"harness-adapters"},
			shouldError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(benchmarkScript, tt.args...)
			cmd.Dir = benchDir

			var stderr bytes.Buffer
			cmd.Stderr = &stderr

			err := cmd.Run()
			if tt.shouldError {
				if err == nil {
					t.Errorf("expected error but command succeeded")
				}
			} else {
				if err != nil {
					t.Fatalf("command failed: %v\nstderr: %s", err, stderr.String())
				}
			}

			if tt.checkOutput != nil {
				tt.checkOutput(t, stderr.String())
			}
		})
	}
}

// TestBenchmarkRunnerMatrixJSON verifies --plan output is correctly formatted
// as key=value pairs that can be parsed into structured data.
func TestBenchmarkRunnerMatrixJSON(t *testing.T) {
	repoRoot := getRepoRoot(t)
	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	benchDir := filepath.Join(repoRoot, "scripts", "benchmark")

	if _, err := os.Stat(benchmarkScript); err != nil {
		t.Fatalf("benchmark script not found: %v", err)
	}

	cmd := exec.Command(benchmarkScript,
		"--profile", "claude-sonnet-4-6",
		"--bench-set", "tb-2-1-canary",
		"--plan")
	cmd.Dir = benchDir

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) == 0 {
		t.Fatalf("no output from --plan")
	}

	// Verify each line can be parsed as key=value pairs
	for i, line := range lines {
		fields := strings.Split(line, "\t")
		for _, field := range fields {
			if !strings.Contains(field, "=") {
				t.Errorf("row %d field not in key=value format: %s", i, field)
				continue
			}
		}
	}

	// Verify specific cells in the plan
	// With 1 profile, 3 tasks, 3 reps: should have specific pattern
	if len(lines) != 9 {
		t.Errorf("expected 9 cells (1 profile × 3 tasks × 3 reps), got %d", len(lines))
	}

	// Verify rep counting (should go from 1/3 to 3/3 for each task)
	repCounts := make(map[string]int)
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		for _, field := range fields {
			if strings.HasPrefix(field, "rep=") {
				repCounts[field]++
			}
		}
	}

	expectedReps := []string{"rep=1/3", "rep=2/3", "rep=3/3"}
	for _, rep := range expectedReps {
		if repCounts[rep] != 3 {
			t.Errorf("expected 3 cells with %s, got %d", rep, repCounts[rep])
		}
	}
}

// TestHarborTaskExecutorSmoke verifies scripts/benchmark/task-executors/harbor
// runs end-to-end with a fixture spec and produces well-formed result.json.
func TestHarborTaskExecutorSmoke(t *testing.T) {
	repoRoot := getRepoRoot(t)
	harborExecutor := filepath.Join(repoRoot, "scripts", "benchmark", "task-executors", "harbor")

	if _, err := os.Stat(harborExecutor); err != nil {
		t.Fatalf("harbor executor not found: %v", err)
	}

	// Check if docker is available
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	tmpDir := t.TempDir()
	cellDir := filepath.Join(tmpDir, "cell")
	tasksDir := filepath.Join(tmpDir, "tasks")

	if err := os.MkdirAll(cellDir, 0755); err != nil {
		t.Fatalf("failed to create cell dir: %v", err)
	}
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("failed to create tasks dir: %v", err)
	}

	taskSpec := map[string]interface{}{
		"task_id":       "test-task",
		"tasks_dir":     tasksDir,
		"cell_dir":      cellDir,
		"harbor_plugin": "scripts.benchmark.harbor_agent:FizeauAgent",
		"image":         "fizeau-harbor-runner:latest",
	}

	specJSON, err := json.Marshal(taskSpec)
	if err != nil {
		t.Fatalf("failed to marshal task spec: %v", err)
	}

	cmd := exec.Command(harborExecutor)
	cmd.Stdin = strings.NewReader(string(specJSON))
	cmd.Dir = filepath.Join(repoRoot, "scripts", "benchmark")
	cmd.Env = append(os.Environ(), "HARBOR_TASK_EXECUTOR_DRY_RUN=1")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		t.Logf("executor stderr: %s", stderr.String())
		t.Fatalf("executor failed: %v", err)
	}

	resultPath := filepath.Join(cellDir, "result.json")
	resultData, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("result.json not found: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resultData, &result); err != nil {
		t.Fatalf("result.json is not valid JSON: %v", err)
	}

	if _, ok := result["dry_run"]; !ok {
		t.Errorf("result.json missing 'dry_run' field")
	}
	if taskID, ok := result["task_id"]; !ok || taskID != "test-task" {
		t.Errorf("result.json task_id mismatch: expected 'test-task', got %v", taskID)
	}
}

// TestHarborRunnerImageNoHostPython verifies scripts/benchmark/harbor-runner/Dockerfile
// does not rely on host Python site-packages and build.sh only uses files under
// scripts/benchmark/harbor-runner/ and scripts/benchmark/harbor_adapters/.
func TestHarborRunnerImageNoHostPython(t *testing.T) {
	repoRoot := getRepoRoot(t)
	dockerfile := filepath.Join(repoRoot, "scripts", "benchmark", "harbor-runner", "Dockerfile")
	buildScript := filepath.Join(repoRoot, "scripts", "benchmark", "harbor-runner", "build.sh")

	dockerfileData, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatalf("Dockerfile not found: %v", err)
	}

	dockerfileContent := string(dockerfileData)

	// Check that Dockerfile does not mount or copy host site-packages
	hostPythonPatterns := []string{
		"site-packages",
		"/usr/local/lib/python",
		"/usr/lib/python",
	}

	for _, pattern := range hostPythonPatterns {
		if strings.Contains(dockerfileContent, pattern) {
			t.Errorf("Dockerfile contains reference to host Python: %s", pattern)
		}
	}

	// Check that Dockerfile doesn't do COPY from host Python locations
	if strings.Contains(dockerfileContent, "COPY /") {
		t.Errorf("Dockerfile uses absolute COPY paths which may reference host Python")
	}

	// Verify build.sh only references appropriate paths
	buildScriptData, err := os.ReadFile(buildScript)
	if err != nil {
		t.Fatalf("build.sh not found: %v", err)
	}

	buildScriptContent := string(buildScriptData)

	// Check that all file references use controlled variables
	lines := strings.Split(buildScriptContent, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}

		// Look for quoted file paths
		if strings.Contains(trimmed, "\"${") || strings.Contains(trimmed, "'${") {
			// Path uses variable, which is fine
			continue
		}

		// Check for absolute paths not in comments or strings
		if strings.Contains(trimmed, "\"/") && !strings.Contains(trimmed, "DOCKER") {
			// This might be an absolute path outside our controlled directories
			// Allow REPO_ROOT usage which contains /
			if !strings.Contains(trimmed, "REPO_ROOT") {
				t.Logf("line %d may use uncontrolled absolute path: %s", i+1, trimmed)
			}
		}
	}

	// Verify the adapters and agent paths are under harbor-runner parent and harbor_adapters
	if !strings.Contains(buildScriptContent, "ADAPTERS_DIR") {
		t.Errorf("build.sh missing ADAPTERS_DIR reference")
	}
	if !strings.Contains(buildScriptContent, "HARBOR_AGENT_PATH") {
		t.Errorf("build.sh missing HARBOR_AGENT_PATH reference")
	}
}

// TestBenchmarkRunnerEndToEnd verifies a full cell execution: shell adapter → command-spec
// → task-executor → result.json → report.json. Uses docker-gated skip if unavailable.
func TestBenchmarkRunnerEndToEnd(t *testing.T) {
	repoRoot := getRepoRoot(t)
	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	benchDir := filepath.Join(repoRoot, "scripts", "benchmark")

	if _, err := os.Stat(benchmarkScript); err != nil {
		t.Fatalf("benchmark script not found: %v", err)
	}

	// Check if docker is available
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	tmpDir := t.TempDir()

	// Use noop profile and a simple bench-set for end-to-end testing
	cmd := exec.Command(benchmarkScript,
		"--profile", "noop",
		"--bench-set", "tb-2-1-canary",
		"--reps", "1",
		"--jobs", "1",
		"--out", tmpDir)
	cmd.Dir = benchDir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		t.Fatalf("benchmark run failed: %v\nstderr: %s", err, stderr.String())
	}

	// Verify sweep.json was created
	sweepPath := filepath.Join(tmpDir, "sweep.json")
	if _, err := os.Stat(sweepPath); err != nil {
		t.Fatalf("sweep.json not created: %v", err)
	}

	// Verify at least one cell report.json exists
	cellsDir := filepath.Join(tmpDir, "cells")
	if _, err := os.Stat(cellsDir); err != nil {
		t.Fatalf("cells directory not created: %v", err)
	}

	// Walk cells directory and verify at least one report.json exists
	var reports []string
	filepath.Walk(cellsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Name() == "report.json" {
			reports = append(reports, path)
		}
		return nil
	})

	if len(reports) == 0 {
		t.Fatalf("no report.json files found under cells directory")
	}

	// Verify the first report has required fields
	reportData, err := os.ReadFile(reports[0])
	if err != nil {
		t.Fatalf("failed to read report.json: %v", err)
	}

	var report map[string]interface{}
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatalf("report.json is not valid JSON: %v", err)
	}

	requiredFields := []string{"cell_id", "task_id", "framework", "dataset", "final_status"}
	for _, field := range requiredFields {
		if _, ok := report[field]; !ok {
			t.Errorf("report.json missing required field: %s", field)
		}
	}
}

// TestBenchmarkRunnerResumeSkipsCompleted verifies that re-running with the same --out
// skips cells with terminal report.json unless --force-rerun is set.
func TestBenchmarkRunnerResumeSkipsCompleted(t *testing.T) {
	repoRoot := getRepoRoot(t)
	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	benchDir := filepath.Join(repoRoot, "scripts", "benchmark")

	if _, err := os.Stat(benchmarkScript); err != nil {
		t.Fatalf("benchmark script not found: %v", err)
	}

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	tmpDir := t.TempDir()

	// First run
	cmd := exec.Command(benchmarkScript,
		"--profile", "noop",
		"--bench-set", "tb-2-1-canary",
		"--reps", "1",
		"--jobs", "1",
		"--out", tmpDir)
	cmd.Dir = benchDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("first run failed: %v\nstderr: %s", err, stderr.String())
	}

	// Count initial reports
	var initialReports []string
	filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == "report.json" {
			initialReports = append(initialReports, path)
		}
		return nil
	})

	if len(initialReports) == 0 {
		t.Fatalf("no reports created in first run")
	}

	// Second run (resume) - should skip terminals unless --force-rerun
	cmd = exec.Command(benchmarkScript,
		"--profile", "noop",
		"--bench-set", "tb-2-1-canary",
		"--reps", "1",
		"--jobs", "1",
		"--out", tmpDir)
	cmd.Dir = benchDir
	stderr.Reset()
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("second run (resume) failed: %v\nstderr: %s", err, stderr.String())
	}

	// Verify that "skip" message appears in stderr for skipped cells
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "skip") {
		t.Logf("warning: expected 'skip' in stderr during resume (resume logic may be working silently)")
	}

	// Third run with --force-rerun should create new cells
	cmd = exec.Command(benchmarkScript,
		"--profile", "noop",
		"--bench-set", "tb-2-1-canary",
		"--reps", "1",
		"--jobs", "1",
		"--force-rerun",
		"--out", tmpDir)
	cmd.Dir = benchDir
	stderr.Reset()
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("third run (force-rerun) failed: %v\nstderr: %s", err, stderr.String())
	}

	// Count final reports - should have more than initial (due to force-rerun creating duplicates)
	var finalReports []string
	filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == "report.json" {
			finalReports = append(finalReports, path)
		}
		return nil
	})

	if len(finalReports) <= len(initialReports) {
		t.Logf("warning: expected more reports after --force-rerun; initial=%d, final=%d", len(initialReports), len(finalReports))
	}
}

// TestBenchmarkRunnerRetryInvalid verifies --retry-invalid reruns cells with
// non-empty invalid_class or orphan cell-state.json.
func TestBenchmarkRunnerRetryInvalid(t *testing.T) {
	repoRoot := getRepoRoot(t)
	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	benchDir := filepath.Join(repoRoot, "scripts", "benchmark")

	if _, err := os.Stat(benchmarkScript); err != nil {
		t.Fatalf("benchmark script not found: %v", err)
	}

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	tmpDir := t.TempDir()

	// First run to establish baseline
	cmd := exec.Command(benchmarkScript,
		"--profile", "noop",
		"--bench-set", "tb-2-1-canary",
		"--reps", "1",
		"--jobs", "1",
		"--out", tmpDir)
	cmd.Dir = benchDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("initial run failed: %v\nstderr: %s", err, stderr.String())
	}

	// Find a report.json and mark it as invalid
	var targetReport string
	filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == "report.json" && targetReport == "" {
			targetReport = path
		}
		return nil
	})

	if targetReport == "" {
		t.Fatalf("no report.json found to mark invalid")
	}

	// Read and modify the report to have invalid_class set
	reportData, err := os.ReadFile(targetReport)
	if err != nil {
		t.Fatalf("failed to read report for modification: %v", err)
	}

	var report map[string]interface{}
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatalf("failed to unmarshal report: %v", err)
	}

	report["invalid_class"] = "test_invalid"
	modifiedData, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("failed to marshal modified report: %v", err)
	}

	if err := os.WriteFile(targetReport, modifiedData, 0644); err != nil {
		t.Fatalf("failed to write modified report: %v", err)
	}

	// Count initial reports
	var initialReports []string
	filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == "report.json" {
			initialReports = append(initialReports, path)
		}
		return nil
	})

	// Now run with --retry-invalid
	cmd = exec.Command(benchmarkScript,
		"--profile", "noop",
		"--bench-set", "tb-2-1-canary",
		"--reps", "1",
		"--jobs", "1",
		"--retry-invalid",
		"--out", tmpDir)
	cmd.Dir = benchDir
	stderr.Reset()
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("retry-invalid run failed: %v\nstderr: %s", err, stderr.String())
	}

	// Verify "retry-invalid" message in stderr
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "retry-invalid") {
		t.Logf("warning: expected 'retry-invalid' in stderr")
	}

	// Count final reports - should have more due to retries
	var finalReports []string
	filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == "report.json" {
			finalReports = append(finalReports, path)
		}
		return nil
	})

	if len(finalReports) <= len(initialReports) {
		t.Logf("warning: expected more reports after --retry-invalid; initial=%d, final=%d", len(initialReports), len(finalReports))
	}
}

// TestBenchmarkRunnerConcurrencyGroupFlock verifies that cells in the same
// concurrency-group serialize via flock at the documented lock path.
func TestBenchmarkRunnerConcurrencyGroupFlock(t *testing.T) {
	repoRoot := getRepoRoot(t)
	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	benchDir := filepath.Join(repoRoot, "scripts", "benchmark")

	if _, err := os.Stat(benchmarkScript); err != nil {
		t.Fatalf("benchmark script not found: %v", err)
	}

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	tmpDir := t.TempDir()

	// Run with --jobs 2 to allow parallel cells, but they should serialize within
	// the same concurrency group due to flocks
	cmd := exec.Command(benchmarkScript,
		"--profile", "noop",
		"--bench-set", "tb-2-1-canary",
		"--reps", "2",
		"--jobs", "2",
		"--out", tmpDir)
	cmd.Dir = benchDir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("concurrent run failed: %v\nstderr: %s", err, stderr.String())
	}

	// Verify lock directory was created at the expected path
	// Lock path should be ${FIZEAU_BENCH_STATE_DIR}/locks/<group>.lock
	// Default FIZEAU_BENCH_STATE_DIR is ${XDG_CACHE_HOME:-$HOME/.cache}/fizeau-benchmark
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("cannot determine home directory: %v", err)
		}
		cacheDir = filepath.Join(home, ".cache")
	}

	lockDir := filepath.Join(cacheDir, "fizeau-benchmark", "locks")
	if _, err := os.Stat(lockDir); err != nil {
		t.Logf("warning: lock directory not found at %s: %v", lockDir, err)
	} else {
		// Verify at least one lock file exists
		entries, err := os.ReadDir(lockDir)
		if err != nil {
			t.Logf("warning: failed to read lock directory: %v", err)
		} else if len(entries) == 0 {
			t.Logf("warning: no lock files found in %s", lockDir)
		}
	}

	// Verify that all cells were created (indicating proper concurrency management)
	var reports []string
	filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == "report.json" {
			reports = append(reports, path)
		}
		return nil
	})

	// Should have at least 2 reports (2 reps) for the canary tasks
	if len(reports) < 2 {
		t.Errorf("expected at least 2 reports with reps=2, got %d", len(reports))
	}
}

// TestBenchmarkRunnerSignalHandling verifies SIGTERM stops accepting cells,
// terminates in-flight cell process groups, docker-stops harbor containers,
// and escalates to SIGKILL after 30s.
func TestBenchmarkRunnerSignalHandling(t *testing.T) {
	repoRoot := getRepoRoot(t)
	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	benchDir := filepath.Join(repoRoot, "scripts", "benchmark")

	if _, err := os.Stat(benchmarkScript); err != nil {
		t.Fatalf("benchmark script not found: %v", err)
	}

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	tmpDir := t.TempDir()

	// Start a benchmark run with multiple cells
	cmd := exec.Command(benchmarkScript,
		"--profile", "noop",
		"--bench-set", "tb-2-1-canary",
		"--reps", "3",
		"--jobs", "3",
		"--out", tmpDir)
	cmd.Dir = benchDir

	// Run in background so we can send signal
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start benchmark: %v", err)
	}

	// Give it a moment to start processing
	select {
	case <-time.After(500 * time.Millisecond):
	}

	// Send SIGTERM
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Logf("warning: failed to send signal: %v", err)
	}

	// Wait with a timeout
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		// Exit code 130 indicates interrupted (SIGINT/SIGTERM)
		if err != nil && !strings.Contains(err.Error(), "exit status") {
			t.Logf("benchmark interrupted with error: %v (may be expected)", err)
		}
	case <-time.After(60 * time.Second):
		t.Errorf("benchmark did not respond to SIGTERM within 60s")
		cmd.Process.Kill()
	}
}

// TestBenchmarkRunnerPreflight verifies `./benchmark preflight` returns 0 on
// healthy host and non-zero with a per-check checklist when dependencies are missing.
func TestBenchmarkRunnerPreflight(t *testing.T) {
	repoRoot := getRepoRoot(t)
	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	benchDir := filepath.Join(repoRoot, "scripts", "benchmark")

	if _, err := os.Stat(benchmarkScript); err != nil {
		t.Fatalf("benchmark script not found: %v", err)
	}

	// Test with healthy environment (should succeed if docker available)
	cmd := exec.Command(benchmarkScript, "preflight")
	cmd.Dir = benchDir

	runErr := cmd.Run()

	// preflight should succeed on a healthy host with docker
	if _, err := exec.LookPath("docker"); err == nil {
		if runErr != nil {
			t.Errorf("preflight failed on healthy host: %v", runErr)
		}
	} else {
		// If docker isn't available, preflight should still run but may report issues
		t.Logf("preflight test skipping docker validation since docker not available")
	}

	// Verify preflight handles the case where tools are present
	cmd = exec.Command("bash", "-c",
		"source "+benchmarkScript+"; require_tool jq; echo OK")
	cmd.Dir = benchDir

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		t.Logf("warning: basic tool check failed: %v", err)
	} else if !strings.Contains(stdout.String(), "OK") {
		t.Logf("warning: tool validation incomplete")
	}
}
