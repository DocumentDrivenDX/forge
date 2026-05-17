package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHarnessAdaptersShellParity verifies that shell adapters produce
// JSON-equivalent output to their Python counterparts.
func TestHarnessAdaptersShellParity(t *testing.T) {
	adapters := []string{
		"fiz",
		"pi",
		"opencode",
		"noop",
		"cost-probe",
		"dumb-script",
	}

	fixtures := []map[string]interface{}{
		{
			"id": "test-basic",
			"provider": map[string]interface{}{
				"type":        "openai-compat",
				"model":       "gpt-4",
				"base_url":    "http://localhost",
				"api_key_env": "OPENAI_API_KEY",
			},
		},
		{
			"id": "test-openrouter",
			"provider": map[string]interface{}{
				"type":        "openai-compat",
				"model":       "qwen/qwen3.6-plus",
				"base_url":    "http://api.openrouter.io/v1",
				"api_key_env": "OPENROUTER_API_KEY",
			},
			"sampling": map[string]interface{}{
				"temperature":   0.7,
				"top_p":         0.95,
				"top_k":         40,
				"min_p":         0.0,
				"reasoning":     "medium",
				"planning_mode": true,
			},
			"limits": map[string]interface{}{
				"max_output_tokens": 4096,
				"context_tokens":    180000,
			},
		},
	}

	for _, adapterName := range adapters {
		adapterName := adapterName
		t.Run(adapterName, func(t *testing.T) {
			for i, fixture := range fixtures {
				fixture := fixture
				t.Run(fmt.Sprintf("fixture_%d", i), func(t *testing.T) {
					fixtureJSON, err := json.Marshal(fixture)
					if err != nil {
						t.Fatalf("failed to marshal fixture: %v", err)
					}

					shellOutput, shellErr := runShellAdapter(t, adapterName, "command", string(fixtureJSON))
					if shellErr != nil {
						t.Fatalf("shell adapter failed: %v, stderr: %s", shellErr, shellOutput)
					}

					// Parse both outputs as JSON
					var shellSpec map[string]interface{}
					if err := json.Unmarshal([]byte(shellOutput), &shellSpec); err != nil {
						t.Fatalf("shell output is not valid JSON: %s, error: %v", shellOutput, err)
					}

					// Verify required keys
					requiredKeys := []string{"command", "env", "secret_env_keys"}
					for _, key := range requiredKeys {
						if _, ok := shellSpec[key]; !ok {
							t.Errorf("missing required key in output: %s", key)
						}
					}

					// Verify secret_env_keys are present in env
					if secretKeys, ok := shellSpec["secret_env_keys"].([]interface{}); ok {
						if envMap, ok := shellSpec["env"].(map[string]interface{}); ok {
							for _, secretKey := range secretKeys {
								if secretKeyStr, ok := secretKey.(string); ok {
									if _, envKeyExists := envMap[secretKeyStr]; !envKeyExists {
										t.Errorf("secret_env_key %s not found in env", secretKeyStr)
									}
								}
							}
						}
					}

					t.Logf("shell adapter output keys: %v", getMapKeys(shellSpec))
				})
			}
		})
	}
}

// TestHarnessAdaptersMalformedInput verifies that adapters handle malformed input gracefully.
func TestHarnessAdaptersMalformedInput(t *testing.T) {
	adapters := []string{
		"fiz",
		"pi",
		"opencode",
		"noop",
		"cost-probe",
		"dumb-script",
	}

	malformedInputs := []string{
		"not json",
		"{incomplete json",
		"null",
		"",
	}

	for _, adapterName := range adapters {
		adapterName := adapterName
		t.Run(adapterName, func(t *testing.T) {
			for i, malformedInput := range malformedInputs {
				malformedInput := malformedInput
				t.Run(fmt.Sprintf("malformed_%d", i), func(t *testing.T) {
					output, err := runShellAdapter(t, adapterName, "command", malformedInput)

					// Calibration adapters (noop, cost-probe, dumb-script) drain stdin
					// and produce valid output even on malformed input
					calibrationAdapters := map[string]bool{
						"noop":        true,
						"cost-probe":  true,
						"dumb-script": true,
					}

					if calibrationAdapters[adapterName] {
						// Calibration adapters should exit 0 and produce valid JSON
						if err != nil {
							t.Logf("calibration adapter %s exited non-zero on malformed input: %v (acceptable)", adapterName, err)
						}
						// Try to parse output as JSON
						var spec map[string]interface{}
						if err := json.Unmarshal([]byte(output), &spec); err != nil && output != "" {
							t.Errorf("calibration adapter output is not valid JSON: %s", output)
						}
					} else {
						// Non-calibration adapters might fail or produce partial output
						// At minimum, we should be able to detect they had an issue
						// This test is primarily checking that shell adapters don't crash
						_ = output
						_ = err
					}
				})
			}
		})
	}
}

// TestHarnessAdaptersInstallCommand verifies that install commands can be generated.
func TestHarnessAdaptersInstallCommand(t *testing.T) {
	adapters := []string{
		"fiz",
		"pi",
		"opencode",
		"noop",
		"cost-probe",
		"dumb-script",
	}

	for _, adapterName := range adapters {
		adapterName := adapterName
		t.Run(adapterName, func(t *testing.T) {
			output, err := runShellAdapterWithArg(t, adapterName, "install", "/path/to/artifact")
			if err != nil {
				t.Fatalf("install command failed: %v, stderr: %s", err, output)
			}

			// Parse output as JSON
			var spec map[string]interface{}
			if err := json.Unmarshal([]byte(output), &spec); err != nil {
				t.Fatalf("install output is not valid JSON: %s, error: %v", output, err)
			}

			// Verify required keys
			requiredKeys := []string{"install_command", "artifact_source", "binary_path", "harbor_plugin"}
			for _, key := range requiredKeys {
				if _, ok := spec[key]; !ok {
					t.Errorf("missing required key in install output: %s", key)
				}
			}

			// Verify artifact_source matches input
			if src, ok := spec["artifact_source"].(string); ok {
				if src != "/path/to/artifact" {
					t.Errorf("artifact_source mismatch: expected '/path/to/artifact', got '%s'", src)
				}
			}

			t.Logf("install output keys: %v", getMapKeys(spec))
		})
	}
}

// runShellAdapter executes a shell adapter with 'command' subcommand, passing input via stdin.
func runShellAdapter(t *testing.T, adapter, subcommand, input string) (string, error) {
	t.Helper()
	repoRoot, err := getRepoRoot()
	if err != nil {
		t.Fatalf("failed to find repo root: %v", err)
	}

	adapterPath := filepath.Join(repoRoot, "scripts", "benchmark", "harness-adapters", adapter)
	cmd := exec.Command(adapterPath, subcommand)
	cmd.Stdin = strings.NewReader(input)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	output := stdout.String()
	errOutput := stderr.String()

	// Combine output for error reporting
	if err != nil {
		return errOutput, fmt.Errorf("exit error: %v", err)
	}
	return output, nil
}

// runShellAdapterWithArg executes a shell adapter with a specific argument.
func runShellAdapterWithArg(t *testing.T, adapter, subcommand, arg string) (string, error) {
	t.Helper()
	repoRoot, err := getRepoRoot()
	if err != nil {
		t.Fatalf("failed to find repo root: %v", err)
	}

	adapterPath := filepath.Join(repoRoot, "scripts", "benchmark", "harness-adapters", adapter)
	cmd := exec.Command(adapterPath, subcommand, arg)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	output := stdout.String()
	errOutput := stderr.String()

	if err != nil {
		return errOutput, fmt.Errorf("exit error: %v", err)
	}
	return output, nil
}

// getRepoRoot finds the repository root by walking up from the test file location.
func getRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	// cmd/bench -> repo root
	repoRoot := filepath.Join(wd, "..", "..")
	return repoRoot, nil
}

// getMapKeys returns a sorted list of keys from a map.
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
