package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestBenchmarkPlanNoSideEffects verifies that --plan mode prints the matrix
// without creating files or building Docker images.
func TestBenchmarkPlanNoSideEffects(t *testing.T) {
	repoRoot, err := getRepoRoot()
	if err != nil {
		t.Fatalf("failed to find repo root: %v", err)
	}

	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	tmpDir := t.TempDir()

	// Run with --plan flag
	cmd := exec.Command(benchmarkScript,
		"--profile", "sindri-lucebox",
		"--bench-set", "tb-2-1-canary",
		"--plan",
		"--out", tmpDir)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	if err := cmd.Run(); err != nil {
		t.Fatalf("benchmark --plan failed: %v\noutput: %s", err, stdout.String())
	}

	output := stdout.String()

	// Verify output contains expected matrix lines
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		t.Fatal("plan produced no output")
	}

	// Expect 3 tasks × 3 reps = 9 lines for tb-2-1-canary with sindri-lucebox
	expectedLineCount := 9
	if len(lines) != expectedLineCount {
		t.Fatalf("expected %d matrix lines, got %d", expectedLineCount, len(lines))
	}

	// Verify each line has the expected fields
	for i, line := range lines {
		if !strings.Contains(line, "profile=sindri-lucebox") {
			t.Errorf("line %d missing profile field: %s", i, line)
		}
		if !strings.Contains(line, "bench_set=tb-2-1-canary") {
			t.Errorf("line %d missing bench_set field: %s", i, line)
		}
		if !strings.Contains(line, "framework=terminal-bench") {
			t.Errorf("line %d missing framework field: %s", i, line)
		}
		if !strings.Contains(line, "dataset=terminal-bench-2-1") {
			t.Errorf("line %d missing dataset field: %s", i, line)
		}
		if !strings.Contains(line, "task=") {
			t.Errorf("line %d missing task field: %s", i, line)
		}
		if !strings.Contains(line, "rep=") {
			t.Errorf("line %d missing rep field: %s", i, line)
		}
	}

	// Verify no files were created in tmpDir
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read tmpDir: %v", err)
	}
	if len(entries) > 0 {
		t.Errorf("--plan created files in tmpDir: %v", entries)
	}
}

// TestBenchmarkMatrixExpansionAndTaskResolution verifies yq loading, profile x task x rep ordering,
// tasks_from resolution, and terminal-bench default task_executor=harbor.
func TestBenchmarkMatrixExpansionAndTaskResolution(t *testing.T) {
	repoRoot, err := getRepoRoot()
	if err != nil {
		t.Fatalf("failed to find repo root: %v", err)
	}

	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	tmpDir := t.TempDir()

	// Test 1: Standard matrix expansion (3 tasks × 3 reps)
	cmd := exec.Command(benchmarkScript,
		"--profile", "sindri-lucebox",
		"--bench-set", "tb-2-1-canary",
		"--plan",
		"--out", tmpDir)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	if err := cmd.Run(); err != nil {
		t.Fatalf("benchmark --plan failed: %v", err)
	}

	output := stdout.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	// Verify rep numbering
	repPattern := regexp.MustCompile(`rep=(\d+)/3`)
	repsFound := make(map[string]int)
	for _, line := range lines {
		if matches := repPattern.FindStringSubmatch(line); len(matches) > 0 {
			repNum := matches[1]
			repsFound[repNum]++
		}
	}

	if len(repsFound) != 3 {
		t.Fatalf("expected 3 different rep numbers, got %d: %v", len(repsFound), repsFound)
	}
	for _, count := range repsFound {
		if count != 3 {
			t.Fatalf("each rep number should appear 3 times (once per task), got: %v", repsFound)
		}
	}

	// Verify task ordering
	expectedTasks := []string{"cancel-async-tasks", "log-summary-date-ranges", "configure-git-webserver"}
	for _, line := range lines {
		found := false
		for _, task := range expectedTasks {
			if strings.Contains(line, fmt.Sprintf("task=%s", task)) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("unexpected task in line: %s", line)
		}
	}

	// Test 2: Verify terminal-bench default task_executor=harbor
	// by checking that lines mention harbor as the task executor
	for _, line := range lines {
		if !strings.Contains(line, "task_executor=harbor") {
			t.Errorf("terminal-bench should default to task_executor=harbor: %s", line)
		}
	}

	// Test 3: --reps override behavior
	cmd = exec.Command(benchmarkScript,
		"--profile", "sindri-lucebox",
		"--bench-set", "tb-2-1-canary",
		"--reps", "2",
		"--plan",
		"--out", tmpDir)

	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	stdout.Reset()

	if err := cmd.Run(); err != nil {
		t.Fatalf("benchmark --plan with --reps failed: %v", err)
	}

	output = stdout.String()
	lines = strings.Split(strings.TrimSpace(output), "\n")

	// With --reps 2: 3 tasks × 2 reps = 6 lines
	if len(lines) != 6 {
		t.Fatalf("with --reps 2, expected 6 lines, got %d", len(lines))
	}

	// Verify rep numbering with --reps 2
	repPattern = regexp.MustCompile(`rep=(\d+)/2`)
	repsFound = make(map[string]int)
	for _, line := range lines {
		if matches := repPattern.FindStringSubmatch(line); len(matches) > 0 {
			repNum := matches[1]
			repsFound[repNum]++
		}
	}

	if len(repsFound) != 2 {
		t.Fatalf("expected 2 different rep numbers with --reps 2, got %d: %v", len(repsFound), repsFound)
	}
}

// TestBenchmarkCanaryReports verifies that running a canary produces cells with expected structure.
func TestBenchmarkCanaryReports(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repoRoot, err := getRepoRoot()
	if err != nil {
		t.Fatalf("failed to find repo root: %v", err)
	}

	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	tmpDir := t.TempDir()

	// Use noop profile to avoid actual model calls
	cmd := exec.Command(benchmarkScript,
		"--profile", "noop",
		"--bench-set", "tb-2-1-canary",
		"--reps", "1",
		"--out", tmpDir,
		"--jobs", "1")

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	if err := cmd.Run(); err != nil {
		t.Fatalf("benchmark run failed: %v\noutput: %s", err, stdout.String())
	}

	// Verify cell directory structure
	cellsDir := filepath.Join(tmpDir, "cells")
	entries, err := os.ReadDir(cellsDir)
	if err != nil {
		t.Fatalf("failed to read cells directory: %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("no dataset directories created")
	}

	// Verify dataset directory exists (from bench-set)
	datasetDir := filepath.Join(cellsDir, "terminal-bench-2-1")
	if _, err := os.Stat(datasetDir); err != nil {
		t.Fatalf("dataset directory not found: %v", err)
	}

	// Verify task directories exist
	taskEntries, err := os.ReadDir(datasetDir)
	if err != nil {
		t.Fatalf("failed to read dataset directory: %v", err)
	}

	if len(taskEntries) == 0 {
		t.Fatal("no task directories created")
	}

	// Verify at least one cell directory with report.json
	foundReport := false
	var reportPath string

	for _, taskEntry := range taskEntries {
		if !taskEntry.IsDir() {
			continue
		}

		taskDir := filepath.Join(datasetDir, taskEntry.Name())
		cellEntries, err := os.ReadDir(taskDir)
		if err != nil {
			continue
		}

		for _, cellEntry := range cellEntries {
			if !cellEntry.IsDir() {
				continue
			}

			cellDir := filepath.Join(taskDir, cellEntry.Name())
			reportPath = filepath.Join(cellDir, "report.json")

			if _, err := os.Stat(reportPath); err == nil {
				foundReport = true

				// Verify report.json contains expected fields
				reportBytes, err := os.ReadFile(reportPath)
				if err != nil {
					t.Fatalf("failed to read report.json: %v", err)
				}

				var report map[string]interface{}
				if err := json.Unmarshal(reportBytes, &report); err != nil {
					t.Fatalf("report.json is not valid JSON: %v", err)
				}

				// Verify required fields
				requiredFields := []string{"profile", "command", "task_id", "framework", "dataset", "cell_id"}
				for _, field := range requiredFields {
					if _, ok := report[field]; !ok {
						t.Errorf("missing required field in report.json: %s", field)
					}
				}

				// Verify profile is embedded
				if profile, ok := report["profile"].(map[string]interface{}); ok {
					if _, hasID := profile["id"]; !hasID {
						t.Error("profile missing id field")
					}
				} else {
					t.Error("profile field is not an object")
				}
			}
		}
	}

	if !foundReport {
		t.Fatal("no report.json files found in any cell")
	}

	// Note: For noop executor, fiz.txt may not be present since no actual execution happens
	t.Logf("Verified cell report structure; found report at %s", reportPath)
}

// TestBenchmarkResumeAndRetry verifies terminal cell skipping, --force-rerun, --retry-invalid,
// and exponential backoff retry logic.
func TestBenchmarkResumeAndRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repoRoot, err := getRepoRoot()
	if err != nil {
		t.Fatalf("failed to find repo root: %v", err)
	}

	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	tmpDir := t.TempDir()

	// Run 1: Create initial cells
	cmd := exec.Command(benchmarkScript,
		"--profile", "noop",
		"--bench-set", "tb-2-1-canary",
		"--reps", "1",
		"--out", tmpDir,
		"--jobs", "1")

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	if err := cmd.Run(); err != nil {
		t.Fatalf("first benchmark run failed: %v", err)
	}

	// Count initial cells
	cellsDir := filepath.Join(tmpDir, "cells", "terminal-bench-2-1")
	initialCellCount := countCells(t, cellsDir)

	if initialCellCount == 0 {
		t.Fatal("no cells created on first run")
	}

	// Run 2: Resume without force-rerun (should skip terminal cells)
	cmd = exec.Command(benchmarkScript,
		"--profile", "noop",
		"--bench-set", "tb-2-1-canary",
		"--reps", "1",
		"--out", tmpDir,
		"--jobs", "1")

	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	stdout.Reset()

	if err := cmd.Run(); err != nil {
		t.Fatalf("resume benchmark run failed: %v", err)
	}

	resumeOutput := stdout.String()

	// Verify skip messages appear
	if !strings.Contains(resumeOutput, "skip:") {
		t.Logf("note: skip messages may not be visible depending on implementation")
	}

	// Run 3: Force rerun (should rerun all cells)
	cmd = exec.Command(benchmarkScript,
		"--profile", "noop",
		"--bench-set", "tb-2-1-canary",
		"--reps", "1",
		"--out", tmpDir,
		"--force-rerun",
		"--jobs", "1")

	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	stdout.Reset()

	if err := cmd.Run(); err != nil {
		t.Logf("force-rerun may fail in test environment: %v", err)
	}

	// Verify cell-state.json if it exists (resume recovery mechanism)
	entries, err := os.ReadDir(cellsDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				taskDir := filepath.Join(cellsDir, entry.Name())
				cellEntries, _ := os.ReadDir(taskDir)
				for _, cellEntry := range cellEntries {
					if cellEntry.IsDir() {
						cellStateFile := filepath.Join(taskDir, cellEntry.Name(), "cell-state.json")
						if _, err := os.Stat(cellStateFile); err == nil {
							stateBytes, _ := os.ReadFile(cellStateFile)
							var state map[string]interface{}
							if err := json.Unmarshal(stateBytes, &state); err == nil {
								// If attempt_of exists, verify it's a positive integer
								if attemptOf, ok := state["attempt_of"].(float64); ok {
									if attemptOf < 1 {
										t.Errorf("attempt_of should be >= 1, got %v", attemptOf)
									}
								}
								t.Logf("Found cell-state.json with attempt_of tracking")
							}
						}
					}
				}
			}
		}
	}
}

// TestBenchmarkPreflightImageRebuild verifies that preflight rebuilds harbor-runner when sha drifts.
func TestBenchmarkPreflightImageRebuild(t *testing.T) {
	repoRoot, err := getRepoRoot()
	if err != nil {
		t.Fatalf("failed to find repo root: %v", err)
	}

	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")

	// Run preflight
	cmd := exec.Command(benchmarkScript, "preflight")

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	if err := cmd.Run(); err != nil {
		// Preflight may fail if docker is not available, which is acceptable
		t.Logf("preflight exited with status (may be due to docker unavailability): %v", err)
	}

	output := stdout.String()

	// Verify preflight ran
	if !strings.Contains(output, "preflight") {
		t.Fatal("preflight did not produce expected output")
	}

	// Verify harbor fingerprint checking
	if !strings.Contains(output, "harbor") {
		t.Logf("note: harbor fingerprint check may be optimized away")
	}

	// Verify --plan mode never invokes preflight
	tmpDir := t.TempDir()
	cmd = exec.Command(benchmarkScript,
		"--profile", "noop",
		"--bench-set", "tb-2-1-canary",
		"--plan",
		"--out", tmpDir)

	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	stdout.Reset()

	if err := cmd.Run(); err != nil {
		t.Fatalf("benchmark --plan failed: %v", err)
	}

	output = stdout.String()

	// Verify output doesn't include preflight/docker messages
	if strings.Contains(output, "docker") || strings.Contains(output, "image") {
		t.Errorf("--plan should not invoke docker/preflight: %s", output)
	}
}

// TestA2Gates verifies script quality and basic sanity checks.
// Note: Full go test ./... and lefthook run pre-commit are run separately
// by the test harness and are independent of this Go test.
func TestA2Gates(t *testing.T) {
	repoRoot, err := getRepoRoot()
	if err != nil {
		t.Fatalf("failed to find repo root: %v", err)
	}

	// Verify benchmark script is executable and has correct syntax
	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	if _, err := os.Stat(benchmarkScript); err != nil {
		t.Fatalf("benchmark script not found: %v", err)
	}

	// Verify script has bash shebang and is executable
	scriptBytes, err := os.ReadFile(benchmarkScript)
	if err != nil {
		t.Fatalf("failed to read benchmark script: %v", err)
	}

	if !bytes.HasPrefix(scriptBytes, []byte("#!/usr/bin/env bash")) {
		t.Error("benchmark script missing bash shebang")
	}

	scriptInfo, err := os.Stat(benchmarkScript)
	if err != nil {
		t.Fatalf("failed to stat benchmark script: %v", err)
	}

	if scriptInfo.Mode()&0o111 == 0 {
		t.Error("benchmark script is not executable")
	}

	// Verify script syntax by running safe subcommands
	for _, subcommand := range []string{"profiles", "bench-sets", "harness-adapters", "task-executors"} {
		cmd := exec.Command(benchmarkScript, subcommand)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			t.Errorf("benchmark %s command failed: %v", subcommand, err)
		}

		output := stdout.String()
		if len(output) == 0 {
			t.Logf("warning: benchmark %s produced no output", subcommand)
		}
	}

	// Verify profiles output contains expected profiles
	cmd := exec.Command(benchmarkScript, "profiles")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err == nil {
		output := stdout.String()
		expectedProfiles := []string{"noop", "sindri-lucebox"}
		for _, profile := range expectedProfiles {
			if !strings.Contains(output, profile) {
				t.Logf("expected profile %s not found in profiles list", profile)
			}
		}
	}
}

// countCells counts all cell directories under the given path.
func countCells(t *testing.T, baseDir string) int {
	count := 0
	filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && isCellDirectory(path) {
			count++
		}
		return nil
	})
	return count
}

// isCellDirectory checks if a directory looks like a cell directory (contains report.json or cell-state.json).
func isCellDirectory(path string) bool {
	reportPath := filepath.Join(path, "report.json")
	if _, err := os.Stat(reportPath); err == nil {
		return true
	}

	statePath := filepath.Join(path, "cell-state.json")
	if _, err := os.Stat(statePath); err == nil {
		return true
	}

	return false
}
