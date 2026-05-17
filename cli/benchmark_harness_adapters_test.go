package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type adapterCommandSpec struct {
	Command       []string          `json:"command"`
	Env           map[string]string `json:"env"`
	SecretEnvKeys []string          `json:"secret_env_keys"`
}

type taskExecutorSpec struct {
	TaskID        string            `json:"task_id"`
	TasksDir      string            `json:"tasks_dir"`
	CellDir       string            `json:"cell_dir"`
	HarborPlugin  string            `json:"harbor_plugin"`
	Image         string            `json:"image"`
	Env           map[string]string `json:"env"`
	SecretEnvKeys []string          `json:"secret_env_keys"`
	ExtraArgs     []string          `json:"extra_args"`
}

func runScript(t *testing.T, script string, dir string, stdin string, args ...string) (string, string, error) {
	t.Helper()

	cmd := exec.Command(script, args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func writeTextFile(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("failed to create parent dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("failed to chmod %s: %v", path, err)
	}
}

func mustJSONMarshal(t *testing.T, v any) string {
	t.Helper()

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal JSON: %v", err)
	}
	return string(data)
}

func decodeAdapterSpec(t *testing.T, raw string) adapterCommandSpec {
	t.Helper()

	var spec adapterCommandSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatalf("failed to decode adapter command-spec: %v\nraw: %s", err, raw)
	}
	return spec
}

func decodeTaskSpec(t *testing.T, raw string) taskExecutorSpec {
	t.Helper()

	var spec taskExecutorSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatalf("failed to decode task-spec: %v\nraw: %s", err, raw)
	}
	return spec
}

func benchmarkProfileJSON(t *testing.T) string {
	t.Helper()

	return mustJSONMarshal(t, map[string]any{
		"id": "fiz-shell-contract",
		"provider": map[string]any{
			"type":        "openrouter",
			"model":       "openai/gpt-5.5",
			"base_url":    "https://openrouter.ai/api/v1",
			"api_key_env": "TEST_PROFILE_API_KEY",
		},
		"sampling": map[string]any{
			"temperature":   0,
			"reasoning":     "medium",
			"planning_mode": true,
			"top_p":         0.95,
			"top_k":         20,
			"min_p":         0,
		},
		"limits": map[string]any{
			"max_output_tokens": 128000,
			"context_tokens":    1000000,
		},
		"metadata": map[string]any{
			"runtime": "shell-test",
		},
	})
}

func setupBenchmarkTaskSpecFixture(t *testing.T) (repoRoot, benchmarkScript, benchDir, profilesDir, benchSetsDir, tasksDir, executorDir, captureDir, outDir, fakeExecutor string) {
	t.Helper()

	repoRoot = getRepoRoot(t)
	benchmarkScript = filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	benchDir = filepath.Join(repoRoot, "scripts", "benchmark")

	root := t.TempDir()
	profilesDir = filepath.Join(root, "profiles")
	benchSetsDir = filepath.Join(root, "bench-sets")
	tasksDir = filepath.Join(root, "tasks")
	executorDir = filepath.Join(root, "task-executors")
	captureDir = filepath.Join(root, "capture")
	outDir = filepath.Join(root, "out")
	stateDir := filepath.Join(root, "state")

	for _, dir := range []string{profilesDir, benchSetsDir, tasksDir, executorDir, captureDir, outDir, stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create %s: %v", dir, err)
		}
	}

	writeTextFile(t, filepath.Join(tasksDir, "demo-task", "task.toml"), "name = \"demo-task\"\n", 0o644)

	writeTextFile(t, filepath.Join(profilesDir, "demo-profile.yaml"), `id: demo-profile
harness: none
surface: fiz_provider_native
concurrency_group: demo-group
provider:
  type: openrouter
  model: openai/gpt-5.5
  base_url: https://openrouter.ai/api/v1
  api_key_env: TEST_PROFILE_API_KEY
pricing:
  input_usd_per_mtok: 0.0
  output_usd_per_mtok: 0.0
  cached_input_usd_per_mtok: 0.0
limits:
  max_output_tokens: 128000
  context_tokens: 1000000
  rate_limit_rpm: 1
  rate_limit_tpm: 1
sampling:
  temperature: 0.1
  reasoning: medium
  planning_mode: true
  top_p: 0.95
  top_k: 20
  min_p: 0
metadata:
  runtime: shell-test
versioning:
  resolved_at: "2026-05-17"
  snapshot: "shell-test"
`, 0o644)

	writeTextFile(t, filepath.Join(benchSetsDir, "demo-set.yaml"), `id: demo-set
framework: terminal-bench
dataset: demo-dataset
default_reps: 1
tasks:
  - demo-task
`, 0o644)

	fakeExecutor = filepath.Join(executorDir, "fake-executor")
	writeTextFile(t, fakeExecutor, `#!/usr/bin/env bash
set -euo pipefail

spec="$(cat)"
capture_dir="${TASK_EXECUTOR_CAPTURE_DIR:?TASK_EXECUTOR_CAPTURE_DIR is required}"
mkdir -p "${capture_dir}"
printf '%s' "${spec}" >"${capture_dir}/task-spec.json"

cell_dir="$(jq -r '.cell_dir // empty' <<<"${spec}")"
task_id="$(jq -r '.task_id // empty' <<<"${spec}")"
if [[ -z "${cell_dir}" || -z "${task_id}" ]]; then
  echo "fake executor: missing task_id or cell_dir" >&2
  exit 2
fi

mkdir -p "${cell_dir}"
jq -n \
  --arg task_id "${task_id}" \
  '{final_status:"completed", invalid_class:"", process_outcome:"completed", task_id:$task_id, dry_run:true}' \
  >"${cell_dir}/result.json"
`, 0o755)

	t.Setenv("PROFILES_DIR", profilesDir)
	t.Setenv("BENCH_SETS_DIR", benchSetsDir)
	t.Setenv("BENCH_TASKS_DIR", tasksDir)
	t.Setenv("TASK_EXECUTORS_DIR", executorDir)
	t.Setenv("BENCH_TASK_EXECUTOR_OVERRIDE", fakeExecutor)
	t.Setenv("TASK_EXECUTOR_CAPTURE_DIR", captureDir)
	t.Setenv("FIZEAU_BENCH_STATE_DIR", stateDir)
	t.Setenv("TEST_PROFILE_API_KEY", "test-profile-secret")
	t.Setenv("PYTHONPATH", "/definitely/not-needed")

	return repoRoot, benchmarkScript, benchDir, profilesDir, benchSetsDir, tasksDir, executorDir, captureDir, outDir, fakeExecutor
}

func TestHarnessAdapterDiscovery(t *testing.T) {
	repoRoot := getRepoRoot(t)
	benchmarkScript := filepath.Join(repoRoot, "scripts", "benchmark", "benchmark")
	benchDir := filepath.Join(repoRoot, "scripts", "benchmark")

	stdout, stderr, err := runScript(t, benchmarkScript, benchDir, "", "harness-adapters")
	if err != nil {
		t.Fatalf("benchmark harness-adapters failed: %v\nstderr: %s", err, stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		t.Fatalf("benchmark harness-adapters produced no output")
	}

	got := map[string]string{}
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			t.Fatalf("unexpected harness-adapters line: %q", line)
		}
		got[parts[0]] = parts[1]
	}

	wantNames := []string{"claude", "codex", "cost-probe", "dumb-script", "fiz", "noop", "opencode", "pi"}
	if len(got) != len(wantNames) {
		names := make([]string, 0, len(got))
		for name := range got {
			names = append(names, name)
		}
		sort.Strings(names)
		t.Fatalf("benchmark harness-adapters listed %d entries, want %d: %v", len(got), len(wantNames), names)
	}
	for _, name := range wantNames {
		summary, ok := got[name]
		if !ok {
			t.Fatalf("benchmark harness-adapters missing %q entry; got keys %v", name, got)
		}
		if strings.TrimSpace(summary) == "" {
			t.Fatalf("benchmark harness-adapters summary for %q was empty", name)
		}
	}
	if _, ok := got["CONTRACT.md"]; ok {
		t.Fatalf("benchmark harness-adapters should not list non-executable docs")
	}
}

func TestHarnessAdapterCommandSpec(t *testing.T) {
	repoRoot := getRepoRoot(t)
	adapter := filepath.Join(repoRoot, "scripts", "benchmark", "harness-adapters", "fiz")
	benchDir := filepath.Join(repoRoot, "scripts", "benchmark")
	profileJSON := benchmarkProfileJSON(t)

	stdout1, stderr1, err := runScript(t, adapter, benchDir, profileJSON, "command")
	if err != nil {
		t.Fatalf("fiz command failed: %v\nstderr: %s", err, stderr1)
	}
	stdout2, stderr2, err := runScript(t, adapter, benchDir, profileJSON, "command")
	if err != nil {
		t.Fatalf("fiz command second run failed: %v\nstderr: %s", err, stderr2)
	}

	if stdout1 != stdout2 {
		t.Fatalf("fiz command output was not deterministic:\nfirst:  %s\nsecond: %s", stdout1, stdout2)
	}

	spec := decodeAdapterSpec(t, stdout1)
	if len(spec.Command) == 0 {
		t.Fatalf("fiz command-spec missing command")
	}
	if len(spec.Command) < 4 {
		t.Fatalf("fiz command-spec command too short: %#v", spec.Command)
	}
	if got, want := spec.Command[:4], []string{"/installed-agent/fiz", "--json", "--preset", "default"}; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("fiz command prefix = %#v, want %#v", got, want)
	}
	if !containsString(spec.Command, "--plan") {
		t.Fatalf("fiz command-spec missing --plan for planning_mode=true: %#v", spec.Command)
	}
	if got := spec.Env["FIZEAU_BASE_URL"]; got != "https://openrouter.ai/api/v1" {
		t.Fatalf("FIZEAU_BASE_URL = %q, want %q", got, "https://openrouter.ai/api/v1")
	}
	if got := spec.Env["FIZEAU_MODEL"]; got != "openai/gpt-5.5" {
		t.Fatalf("FIZEAU_MODEL = %q, want %q", got, "openai/gpt-5.5")
	}
	if got := spec.Env["FIZEAU_API_KEY"]; got != "${TEST_PROFILE_API_KEY}" {
		t.Fatalf("FIZEAU_API_KEY = %q, want %q", got, "${TEST_PROFILE_API_KEY}")
	}
	if got := spec.Env["FIZEAU_PROVIDER"]; got != "openrouter" {
		t.Fatalf("FIZEAU_PROVIDER = %q, want %q", got, "openrouter")
	}
	if got := spec.Env["FIZEAU_TEMPERATURE"]; got != "0" {
		t.Fatalf("FIZEAU_TEMPERATURE = %q, want %q", got, "0")
	}
	if got := spec.Env["FIZEAU_TOP_P"]; got != "0.95" {
		t.Fatalf("FIZEAU_TOP_P = %q, want %q", got, "0.95")
	}
	if got := spec.Env["FIZEAU_TOP_K"]; got != "20" {
		t.Fatalf("FIZEAU_TOP_K = %q, want %q", got, "20")
	}
	if got := spec.Env["FIZEAU_MIN_P"]; got != "0" {
		t.Fatalf("FIZEAU_MIN_P = %q, want %q", got, "0")
	}
	if got := spec.Env["FIZEAU_REASONING"]; got != "medium" {
		t.Fatalf("FIZEAU_REASONING = %q, want %q", got, "medium")
	}
	if got := spec.Env["FIZEAU_PLANNING_MODE"]; got != "1" {
		t.Fatalf("FIZEAU_PLANNING_MODE = %q, want %q", got, "1")
	}
	if got := strings.Join(spec.SecretEnvKeys, ","); got != "FIZEAU_API_KEY" {
		t.Fatalf("secret_env_keys = %#v, want [FIZEAU_API_KEY]", spec.SecretEnvKeys)
	}
}

func TestHarnessAdapterRejectsInvalidInput(t *testing.T) {
	repoRoot := getRepoRoot(t)
	adapter := filepath.Join(repoRoot, "scripts", "benchmark", "harness-adapters", "fiz")
	benchDir := filepath.Join(repoRoot, "scripts", "benchmark")

	t.Run("malformed-json", func(t *testing.T) {
		stdout, stderr, err := runScript(t, adapter, benchDir, "not json", "command")
		if err == nil {
			t.Fatalf("fiz command unexpectedly succeeded with malformed JSON: %s", stdout)
		}
		if stdout != "" {
			t.Fatalf("fiz command produced stdout for malformed JSON: %s", stdout)
		}
		if !strings.Contains(stderr, "parse error") && !strings.Contains(stderr, "invalid") {
			t.Fatalf("fiz command stderr did not explain malformed JSON: %s", stderr)
		}
	})

	t.Run("schema-invalid", func(t *testing.T) {
		stdout, stderr, err := runScript(t, adapter, benchDir, "{}", "command")
		if err == nil {
			t.Fatalf("fiz command unexpectedly succeeded with schema-invalid profile: %s", stdout)
		}
		if stdout != "" {
			t.Fatalf("fiz command produced stdout for schema-invalid profile: %s", stdout)
		}
		if !strings.Contains(stderr, "missing required field") {
			t.Fatalf("fiz command stderr did not identify the missing field: %s", stderr)
		}
	})
}

func TestBenchmarkTaskSpecComposition(t *testing.T) {
	_, benchmarkScript, benchDir, _, _, tasksDir, _, captureDir, outDir, _ := setupBenchmarkTaskSpecFixture(t)

	stdout, stderr, err := runScript(t, benchmarkScript, benchDir, "", "--profile", "demo-profile", "--bench-set", "demo-set", "--out", outDir, "--jobs", "1")
	if err != nil {
		t.Fatalf("benchmark run failed: %v\nstderr: %s\nstdout: %s", err, stderr, stdout)
	}

	specPath := filepath.Join(captureDir, "task-spec.json")
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("task executor did not capture task-spec.json: %v", err)
	}

	spec := decodeTaskSpec(t, string(data))
	if spec.TaskID != "demo-task" {
		t.Fatalf("task_id = %q, want %q", spec.TaskID, "demo-task")
	}
	if spec.TasksDir != tasksDir {
		t.Fatalf("tasks_dir = %q, want %q", spec.TasksDir, tasksDir)
	}
	if !strings.Contains(spec.CellDir, filepath.Join("cells", "demo-dataset", "demo-task")) {
		t.Fatalf("cell_dir = %q, want it to be under demo-dataset/demo-task", spec.CellDir)
	}
	if spec.HarborPlugin != "scripts.benchmark.harbor_agent:FizeauAgent" {
		t.Fatalf("harbor_plugin = %q, want %q", spec.HarborPlugin, "scripts.benchmark.harbor_agent:FizeauAgent")
	}
	if spec.Image != "fizeau-harbor-runner:latest" {
		t.Fatalf("image = %q, want %q", spec.Image, "fizeau-harbor-runner:latest")
	}
	if got := spec.Env["FIZEAU_BASE_URL"]; got != "https://openrouter.ai/api/v1" {
		t.Fatalf("task-spec env FIZEAU_BASE_URL = %q, want %q", got, "https://openrouter.ai/api/v1")
	}
	if got := spec.Env["FIZEAU_MODEL"]; got != "openai/gpt-5.5" {
		t.Fatalf("task-spec env FIZEAU_MODEL = %q, want %q", got, "openai/gpt-5.5")
	}
	if got := spec.Env["FIZEAU_API_KEY"]; got != "${TEST_PROFILE_API_KEY}" {
		t.Fatalf("task-spec env FIZEAU_API_KEY = %q, want %q", got, "${TEST_PROFILE_API_KEY}")
	}
	if got := strings.Join(spec.SecretEnvKeys, ","); got != "FIZEAU_API_KEY" {
		t.Fatalf("task-spec secret_env_keys = %#v, want [FIZEAU_API_KEY]", spec.SecretEnvKeys)
	}
	if len(spec.ExtraArgs) != 0 {
		t.Fatalf("task-spec extra_args = %#v, want empty", spec.ExtraArgs)
	}
}

func TestBenchmarkNoHostPythonPathAdapterFlow(t *testing.T) {
	repoRoot := getRepoRoot(t)
	adapter := filepath.Join(repoRoot, "scripts", "benchmark", "harness-adapters", "fiz")
	benchDir := filepath.Join(repoRoot, "scripts", "benchmark")
	profileJSON := benchmarkProfileJSON(t)

	t.Setenv("PYTHONPATH", "/definitely/not-needed")

	stdout, stderr, err := runScript(t, adapter, benchDir, profileJSON, "command")
	if err != nil {
		t.Fatalf("fiz command failed without repo-root PYTHONPATH: %v\nstderr: %s", err, stderr)
	}
	if stdout == "" {
		t.Fatalf("fiz command produced no command-spec output")
	}

	spec := decodeAdapterSpec(t, stdout)
	if spec.Env["FIZEAU_BASE_URL"] != "https://openrouter.ai/api/v1" {
		t.Fatalf("fiz command-spec did not parse under a bogus PYTHONPATH: %#v", spec.Env)
	}
	if strings.Contains(stdout, "scripts.benchmark.harbor_adapters") {
		t.Fatalf("fiz command-spec unexpectedly referenced a Python harbor adapter: %s", stdout)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
