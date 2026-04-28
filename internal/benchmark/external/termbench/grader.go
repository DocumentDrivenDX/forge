package termbench

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// GraderResult is the scored outcome of one TerminalBench task. It is
// produced by Harbor's verifier (pytest --ctrf), NOT by this package.
// We only read the artifacts the verifier writes.
type GraderResult struct {
	// TaskID identifies which task produced this result.
	TaskID string

	// Reward is the canonical pass/fail signal: 1 = passed, 0 = failed.
	// Mirrors Harbor's /logs/verifier/reward.txt contract.
	Reward int

	// CTRFPath, if set, points at the verifier's pytest CTRF JSON. Reporters
	// can drill down for per-test detail.
	CTRFPath string

	// RewardPath is the absolute path the reward was read from.
	RewardPath string

	// Notes carries human-readable detail (e.g. "missing reward.txt").
	Notes string
}

// Passed is true iff the verifier awarded reward >= 1.
func (g *GraderResult) Passed() bool { return g != nil && g.Reward >= 1 }

// ReadGraderResult inspects an output directory laid out the way Harbor
// produces it (see SD-008 §4) and returns the verifier's verdict. The
// contract:
//
//	<outDir>/logs/verifier/reward.txt   — single integer (required)
//	<outDir>/logs/verifier/ctrf.json    — pytest CTRF report (optional)
//
// If reward.txt is missing the function returns ErrNoVerifierOutput so
// callers can distinguish "task failed" (reward=0) from "task never
// graded". Both can be present if the agent ran but the verifier
// fell over.
func ReadGraderResult(taskID, outDir string) (*GraderResult, error) {
	verifierDir := filepath.Join(outDir, "logs", "verifier")
	rewardPath := filepath.Join(verifierDir, "reward.txt")
	data, err := os.ReadFile(rewardPath) // #nosec G304 -- path is constructed from caller-controlled outDir
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoVerifierOutput
		}
		return nil, fmt.Errorf("termbench: read %s: %w", rewardPath, err)
	}
	reward, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("termbench: parse reward %q: %w", string(data), err)
	}
	res := &GraderResult{
		TaskID:     taskID,
		Reward:     reward,
		RewardPath: rewardPath,
	}
	ctrfPath := filepath.Join(verifierDir, "ctrf.json")
	if _, err := os.Stat(ctrfPath); err == nil {
		res.CTRFPath = ctrfPath
	}
	return res, nil
}

// ErrNoVerifierOutput is returned by ReadGraderResult when the verifier
// did not produce a reward file. Sentinel so callers can treat
// "not graded" differently from "graded as failed".
var ErrNoVerifierOutput = errors.New("termbench: verifier produced no reward.txt")
