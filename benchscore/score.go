package benchscore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Result    string          `json:"result"`
	Error     string          `json:"error,omitempty"`
}

type Step struct {
	Source    string     `json:"source"`
	Message   string     `json:"message"`
	ToolCalls []ToolCall `json:"tool_calls"`
}

type Trajectory struct {
	Steps []Step `json:"steps"`
}

type TaskResult struct {
	TaskID         string  `json:"task_id"`
	Reward         *string `json:"reward"`
	TrajectoryFile string  `json:"trajectory_file"`
}

type TaskMetrics struct {
	TaskID                  string  `json:"task_id"`
	Resolved                bool    `json:"resolved"`
	ClarificationDetected   bool    `json:"clarification_detected"`
	BashCalls               int     `json:"bash_calls"`
	BashAntiPatterns        int     `json:"bash_anti_patterns"`
	ShellAntiPatternRate    float64 `json:"shell_anti_pattern_rate"`
	StructuredEditCalls     int     `json:"structured_edit_calls"`
	StructuredEditSuccesses int     `json:"structured_edit_successes"`
	StructuredEditRate      float64 `json:"structured_edit_success_rate"`
}

type Summary struct {
	TotalTasks                   int     `json:"total_tasks"`
	ResolvedTasks                int     `json:"resolved_tasks"`
	ResolvedTaskRate             float64 `json:"resolved_task_rate"`
	ClarificationTrials          int     `json:"clarification_trials"`
	ClarificationQuestionRate    float64 `json:"clarification_question_rate"`
	TotalBashCalls               int     `json:"total_bash_calls"`
	TotalBashAntiPatterns        int     `json:"total_bash_anti_patterns"`
	ShellAntiPatternRate         float64 `json:"shell_anti_pattern_rate"`
	TotalStructuredEditCalls     int     `json:"total_structured_edit_calls"`
	TotalStructuredEditSuccesses int     `json:"total_structured_edit_successes"`
	StructuredEditSuccessRate    float64 `json:"structured_edit_success_rate"`
}

type Report struct {
	Summary Summary       `json:"summary"`
	Tasks   []TaskMetrics `json:"tasks"`
}

type bashInput struct {
	Command string `json:"command"`
}

var (
	shellAntiPatternRE = regexp.MustCompile(`(^|[|;&()\s])(ls|find|cat|grep|rg)\b`)
)

func ScoreTaskResultsJSONL(path string) (Report, error) {
	// #nosec G304 -- path comes from benchmark harness, not untrusted input
	f, err := os.Open(path)
	if err != nil {
		return Report{}, fmt.Errorf("benchscore: open task results: %w", err)
	}
	defer f.Close()

	var report Report
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var task TaskResult
		if err := json.Unmarshal(line, &task); err != nil {
			return Report{}, fmt.Errorf("benchscore: decode task result: %w", err)
		}
		taskMetrics, err := scoreTaskResult(task)
		if err != nil {
			return Report{}, err
		}
		report.Tasks = append(report.Tasks, taskMetrics)
		accumulate(&report.Summary, taskMetrics)
	}
	if err := scanner.Err(); err != nil {
		return Report{}, fmt.Errorf("benchscore: scan task results: %w", err)
	}
	finalizeSummary(&report.Summary)
	return report, nil
}

func scoreTaskResult(task TaskResult) (TaskMetrics, error) {
	metrics := TaskMetrics{TaskID: task.TaskID}
	metrics.Resolved = task.Reward != nil && *task.Reward == "1"
	if task.TrajectoryFile == "" {
		return metrics, nil
	}

	data, err := os.ReadFile(filepath.Clean(task.TrajectoryFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return metrics, nil
		}
		return TaskMetrics{}, fmt.Errorf("benchscore: read trajectory for %s: %w", task.TaskID, err)
	}

	var trajectory Trajectory
	if err := json.Unmarshal(data, &trajectory); err != nil {
		return TaskMetrics{}, fmt.Errorf("benchscore: decode trajectory for %s: %w", task.TaskID, err)
	}

	metrics.ClarificationDetected = detectClarification(trajectory.Steps)
	for _, step := range trajectory.Steps {
		for _, call := range step.ToolCalls {
			switch call.Name {
			case "bash":
				metrics.BashCalls++
				if isShellAntiPattern(call.Arguments) {
					metrics.BashAntiPatterns++
				}
			case "edit", "patch":
				metrics.StructuredEditCalls++
				if structuredEditSucceeded(call) {
					metrics.StructuredEditSuccesses++
				}
			}
		}
	}

	if metrics.BashCalls > 0 {
		metrics.ShellAntiPatternRate = float64(metrics.BashAntiPatterns) / float64(metrics.BashCalls)
	}
	if metrics.StructuredEditCalls > 0 {
		metrics.StructuredEditRate = float64(metrics.StructuredEditSuccesses) / float64(metrics.StructuredEditCalls)
	}
	return metrics, nil
}

func accumulate(summary *Summary, task TaskMetrics) {
	summary.TotalTasks++
	if task.Resolved {
		summary.ResolvedTasks++
	}
	if task.ClarificationDetected {
		summary.ClarificationTrials++
	}
	summary.TotalBashCalls += task.BashCalls
	summary.TotalBashAntiPatterns += task.BashAntiPatterns
	summary.TotalStructuredEditCalls += task.StructuredEditCalls
	summary.TotalStructuredEditSuccesses += task.StructuredEditSuccesses
}

func finalizeSummary(summary *Summary) {
	if summary.TotalTasks > 0 {
		summary.ResolvedTaskRate = float64(summary.ResolvedTasks) / float64(summary.TotalTasks)
		summary.ClarificationQuestionRate = float64(summary.ClarificationTrials) / float64(summary.TotalTasks)
	}
	if summary.TotalBashCalls > 0 {
		summary.ShellAntiPatternRate = float64(summary.TotalBashAntiPatterns) / float64(summary.TotalBashCalls)
	}
	if summary.TotalStructuredEditCalls > 0 {
		summary.StructuredEditSuccessRate = float64(summary.TotalStructuredEditSuccesses) / float64(summary.TotalStructuredEditCalls)
	}
}

func detectClarification(steps []Step) bool {
	for _, step := range steps {
		if step.Source != "agent" {
			continue
		}
		if len(step.ToolCalls) > 0 {
			return false
		}
		return isClarificationMessage(step.Message)
	}
	return false
}

func isClarificationMessage(msg string) bool {
	normalized := strings.ToLower(strings.TrimSpace(msg))
	if normalized == "" {
		return false
	}
	if strings.Contains(normalized, "?") {
		return true
	}
	phrases := []string{
		"please clarify",
		"can you clarify",
		"could you clarify",
		"need more information",
		"need more detail",
		"please provide",
		"can you provide",
		"could you provide",
		"before i can",
		"let me know",
		"which file",
		"which files",
		"what file",
		"what files",
		"which path",
		"what path",
		"what exactly",
		"what would you like",
		"which one",
		"specify",
	}
	for _, phrase := range phrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

func isShellAntiPattern(arguments json.RawMessage) bool {
	if len(arguments) == 0 {
		return false
	}
	var input bashInput
	if err := json.Unmarshal(arguments, &input); err != nil {
		return false
	}
	command := strings.ToLower(strings.TrimSpace(input.Command))
	if command == "" {
		return false
	}
	return shellAntiPatternRE.MatchString(command)
}

func structuredEditSucceeded(call ToolCall) bool {
	if strings.TrimSpace(call.Error) != "" {
		return false
	}
	return !strings.HasPrefix(strings.ToLower(strings.TrimSpace(call.Result)), "error:")
}
