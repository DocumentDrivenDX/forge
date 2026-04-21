package claude

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
)

var ansiPattern = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[a-zA-Z]|[^[])`)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

// ReadClaudeQuotaViaTmux starts claude in a detached tmux session, sends /usage,
// captures the pane output, and returns parsed quota windows and account info.
//
// Deprecated: this is a diagnostic-only legacy path. Supported quota probes use
// ReadClaudeQuotaViaPTY so accepted evidence passes through direct PTY cassettes.
// Returns an error if tmux or claude are not found, or probing times out.
func ReadClaudeQuotaViaTmux(timeout time.Duration) ([]harnesses.QuotaWindow, *harnesses.AccountInfo, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, nil, fmt.Errorf("tmux not found in PATH: %w", err)
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, nil, fmt.Errorf("claude not found in PATH: %w", err)
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	sessName := fmt.Sprintf("ddx-claude-quota-%d", os.Getpid())
	if err := exec.Command("tmux", "new-session", "-d", "-s", sessName, "-x", "220", "-y", "50", "claude").Run(); err != nil {
		return nil, nil, fmt.Errorf("start tmux session: %w", err)
	}
	defer func() { _ = exec.Command("tmux", "kill-session", "-t", sessName).Run() }()

	// Poll until claude shows its interactive prompt (ready state).
	// The prompt "❯" (or "> ") appears once the REPL is initialized.
	deadline := time.Now().Add(timeout)
	ready := false
	for !ready && time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		// Bail early if the session has already exited (e.g. non-interactive binary).
		if err := exec.Command("tmux", "has-session", "-t", sessName).Run(); err != nil {
			return nil, nil, fmt.Errorf("claude session exited before initialization")
		}
		out, err := exec.Command("tmux", "capture-pane", "-t", sessName, "-p").Output()
		if err == nil {
			text := stripANSI(string(out))
			// Look for the interactive prompt — appears when REPL is ready.
			if strings.Contains(text, "❯") || strings.Contains(text, "> ") {
				ready = true
			}
		}
	}
	if !ready {
		return nil, nil, fmt.Errorf("timed out waiting for claude to initialize")
	}

	if err := exec.Command("tmux", "send-keys", "-t", sessName, "/usage", "Enter").Run(); err != nil {
		return nil, nil, fmt.Errorf("send /usage: %w", err)
	}

	// Poll until usage data appears (look for "% used" and "Resets").
	var captured string
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		out, err := exec.Command("tmux", "capture-pane", "-t", sessName, "-p", "-S", "-100").Output()
		if err == nil {
			text := stripANSI(string(out))
			if strings.Contains(text, "% used") && strings.Contains(strings.ToLower(text), "resets") {
				captured = text
				break
			}
		}
	}
	if captured == "" {
		return nil, nil, fmt.Errorf("timed out waiting for /usage output")
	}

	windows, acct := parseClaudeUsageOutput(captured)
	if len(windows) == 0 {
		return nil, acct, fmt.Errorf("no quota windows found in /usage output")
	}
	return windows, acct, nil
}

var (
	claudeUsedPercentPattern = regexp.MustCompile(`(\d+)%\s+used`)
	claudePlanTypePattern    = regexp.MustCompile(`(?i)(Claude\s+(?:Max|Pro|Team|Enterprise|Free))`)
)

type claudeUsageSection struct {
	Name       string
	LimitID    string
	WindowMins int
}

var claudeUsageSections = []claudeUsageSection{
	{"Current session", "session", 300},
	{"Current week (all models)", "weekly-all", 10080},
	{"Current week (Sonnet only)", "weekly-sonnet", 10080},
	{"Extra usage", "extra", 0},
}

// parseClaudeUsageOutput parses text captured from a claude /usage pane.
// Returns quota windows and optional account info (plan type from header).
func parseClaudeUsageOutput(text string) ([]harnesses.QuotaWindow, *harnesses.AccountInfo) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")

	var acct *harnesses.AccountInfo
	var windows []harnesses.QuotaWindow

	// Extract plan type from header line.
	for _, line := range lines {
		if m := claudePlanTypePattern.FindString(line); m != "" {
			acct = &harnesses.AccountInfo{PlanType: m}
			break
		}
	}

	// Walk lines looking for section headers, then harvest % used and Resets.
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		trimmedLower := strings.ToLower(trimmed)

		var sec *claudeUsageSection
		for j := range claudeUsageSections {
			if trimmedLower == strings.ToLower(claudeUsageSections[j].Name) {
				sec = &claudeUsageSections[j]
				break
			}
		}
		if sec == nil {
			continue
		}

		// Scan ahead up to 5 lines for "% used" then "Resets".
		var usedPct int
		var resetsAt string
		found := false

		for j := i + 1; j < len(lines) && j <= i+5; j++ {
			next := strings.TrimSpace(lines[j])
			if !found {
				if m := claudeUsedPercentPattern.FindStringSubmatch(next); m != nil {
					pct, _ := strconv.Atoi(m[1])
					usedPct = pct
					found = true
				}
			}
			if found && resetsAt == "" && strings.Contains(strings.ToLower(next), "resets") {
				resetsAt = extractResetsText(next)
			}
			if found && resetsAt != "" {
				break
			}
		}

		if !found {
			continue
		}

		windows = append(windows, harnesses.QuotaWindow{
			Name:          sec.Name,
			LimitID:       sec.LimitID,
			WindowMinutes: sec.WindowMins,
			UsedPercent:   float64(usedPct),
			ResetsAt:      resetsAt,
			State:         harnesses.QuotaStateFromUsedPercent(usedPct),
		})
	}

	return windows, acct
}

// extractResetsText strips the "Resets" prefix from a line.
// Handles: "Resets 4pm (America/New_York)"
//
//	"$200 spent · Resets May 1 (America/New_York)"
func extractResetsText(line string) string {
	lower := strings.ToLower(line)
	idx := strings.Index(lower, "resets")
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(line[idx+len("resets"):])
}
