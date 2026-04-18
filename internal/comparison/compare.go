package comparison

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RunCompare dispatches the same prompt to multiple harnesses,
// optionally in isolated worktrees, and returns a ComparisonRecord.
func RunCompare(run RunFunc, opts CompareOptions) (*ComparisonRecord, error) {
	if len(opts.Harnesses) == 0 {
		return nil, fmt.Errorf("comparison: RunCompare requires at least one harness")
	}

	id := genCompareID()
	record := &ComparisonRecord{
		ID:        id,
		Timestamp: time.Now().UTC(),
		Prompt:    opts.Prompt,
		Arms:      make([]ComparisonArm, len(opts.Harnesses)),
	}

	// Resolve base working directory.
	baseDir := opts.WorkDir
	if baseDir == "" {
		baseDir, _ = os.Getwd()
	}

	// Create worktrees sequentially (git worktree add takes a lock)
	// then run agent arms in parallel.
	worktrees := make([]string, len(opts.Harnesses))
	if opts.Sandbox {
		for i, harness := range opts.Harnesses {
			label := harness
			if l, ok := opts.ArmLabels[i]; ok {
				label = l
			}
			wt, err := createCompareWorktree(baseDir, id, label)
			if err != nil {
				record.Arms[i] = ComparisonArm{
					Harness:  label,
					ExitCode: 1,
					Error:    fmt.Sprintf("worktree: %s", err),
				}
				continue
			}
			worktrees[i] = wt
		}
	}

	var wg sync.WaitGroup
	for i, harness := range opts.Harnesses {
		// Skip arms that failed worktree creation.
		if opts.Sandbox && worktrees[i] == "" && record.Arms[i].Error != "" {
			continue
		}
		wg.Add(1)
		go func(idx int, harnessName string) {
			defer wg.Done()
			record.Arms[idx] = runCompareArm(run, opts, idx, harnessName, baseDir, id, opts.Prompt, worktrees[idx])
		}(i, harness)
	}
	wg.Wait()

	// Cleanup worktrees unless KeepSandbox.
	if opts.Sandbox && !opts.KeepSandbox {
		cleanupCompareWorktrees(baseDir, id)
	}

	return record, nil
}

// runCompareArm executes one harness arm, optionally in a pre-created worktree.
func runCompareArm(run RunFunc, opts CompareOptions, armIdx int, harnessName, baseDir, compareID, prompt, worktreePath string) ComparisonArm {
	label := harnessName
	if l, ok := opts.ArmLabels[armIdx]; ok {
		label = l
	}
	arm := ComparisonArm{Harness: label}

	// Determine working directory.
	workDir := baseDir
	if worktreePath != "" {
		workDir = worktreePath
	}

	// Resolve per-arm model override.
	model := ""
	if m, ok := opts.ArmModels[armIdx]; ok {
		model = m
	}

	result := run(harnessName, model, prompt)

	arm.Model = result.Model
	arm.Output = result.Output
	arm.ToolCalls = result.ToolCalls
	arm.Tokens = result.Tokens
	arm.InputTokens = result.InputTokens
	arm.OutputTokens = result.OutputTokens
	arm.CostUSD = result.CostUSD
	arm.DurationMS = result.DurationMS
	arm.ExitCode = result.ExitCode
	arm.Error = result.Error

	// Capture git diff if we're in a worktree.
	if worktreePath != "" {
		arm.Diff = captureGitDiff(worktreePath)
	}

	// Run post-run command if specified.
	if opts.PostRun != "" && workDir != "" {
		out, ok := runPostCommand(workDir, opts.PostRun)
		arm.PostRunOut = out
		arm.PostRunOK = &ok
	}

	return arm
}

// createCompareWorktree creates a git worktree for a comparison arm.
func createCompareWorktree(workDir, compareID, harnessName string) (string, error) {
	gitRoot, err := resolveGitRoot(workDir)
	if err != nil {
		return "", fmt.Errorf("resolving git root: %w", err)
	}

	wtDir := filepath.Join(gitRoot, ".worktrees", fmt.Sprintf("%s-%s", compareID, harnessName))

	cmd := exec.Command("git", "worktree", "add", "--detach", wtDir, "HEAD")
	cmd.Dir = gitRoot
	cmd.Env = cleanGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add: %s\n%s", err, string(out))
	}
	return wtDir, nil
}

// resolveGitRoot finds the git repository root from any directory within it.
func resolveGitRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	cmd.Env = cleanGitEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %s", dir)
	}
	return strings.TrimSpace(string(out)), nil
}

// captureGitDiff captures the unified diff of all changes in a worktree.
func captureGitDiff(worktreePath string) string {
	cmd := exec.Command("git", "diff", "HEAD")
	cmd.Dir = worktreePath
	cmd.Env = cleanGitEnv()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	diff := string(out)

	// Also capture untracked files as a diff-like listing.
	cmd3 := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd3.Dir = worktreePath
	cmd3.Env = cleanGitEnv()
	untrackedOut, _ := cmd3.Output()
	untracked := strings.TrimSpace(string(untrackedOut))
	if untracked != "" {
		for _, f := range strings.Split(untracked, "\n") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			content, err := os.ReadFile(filepath.Join(worktreePath, f))
			if err != nil {
				continue
			}
			diff += fmt.Sprintf("\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1 @@\n", f)
			for _, line := range strings.Split(string(content), "\n") {
				if line != "" || len(content) > 0 {
					diff += "+" + line + "\n"
				}
			}
		}
	}

	return strings.TrimSpace(diff)
}

// cleanGitEnv returns the current environment with git hook-specific vars removed.
func cleanGitEnv() []string {
	blocked := map[string]bool{
		"GIT_DIR":                          true,
		"GIT_INDEX_FILE":                   true,
		"GIT_WORK_TREE":                    true,
		"GIT_OBJECT_DIRECTORY":             true,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, e := range env {
		key := e
		if i := strings.Index(e, "="); i >= 0 {
			key = e[:i]
		}
		if !blocked[key] {
			out = append(out, e)
		}
	}
	return out
}

// runPostCommand runs a shell command in the given directory.
func runPostCommand(dir, command string) (string, bool) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// cleanupCompareWorktrees removes worktrees created for a comparison.
func cleanupCompareWorktrees(repoDir, compareID string) {
	if root, err := resolveGitRoot(repoDir); err == nil {
		repoDir = root
	}
	wtBase := filepath.Join(repoDir, ".worktrees")
	entries, err := os.ReadDir(wtBase)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), compareID) {
			wtPath := filepath.Join(wtBase, e.Name())
			cmd := exec.Command("git", "worktree", "remove", "--force", wtPath)
			cmd.Dir = repoDir
			cmd.Env = cleanGitEnv()
			_ = cmd.Run()
		}
	}
	cmd := exec.Command("git", "worktree", "prune")
	cmd.Dir = repoDir
	cmd.Env = cleanGitEnv()
	_ = cmd.Run()
}

func genCompareID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "cmp-" + hex.EncodeToString(b)
}
