package termbench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
)

// ATIFSchemaVersion pins the trajectory schema to the version Harbor's
// grader currently consumes. SD-008 §4 documented the fields required.
const ATIFSchemaVersion = "1.4"

// AgentInfo identifies the executor in trajectory output. Harbor's
// reporters use Name + Version to label leaderboard entries.
type AgentInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	ModelName string `json:"model_name,omitempty"`
}

// TrajectoryStep is one transcript element in ATIF v1.4 form.
type TrajectoryStep struct {
	StepID    int             `json:"step_id"`
	Timestamp string          `json:"timestamp"`
	Source    string          `json:"source"` // user|agent|system|tool
	Message   string          `json:"message,omitempty"`
	ToolCalls []TrajectoryTC  `json:"tool_calls,omitempty"`
	Metrics   *TrajectoryStat `json:"metrics,omitempty"`
}

// TrajectoryTC is one tool invocation as ATIF expects it.
type TrajectoryTC struct {
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input,omitempty"`
	Output string          `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// TrajectoryStat carries per-step or final usage/cost metrics.
type TrajectoryStat struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	Cost         float64 `json:"cost"`
}

// Trajectory is the top-level ATIF v1.4 document.
type Trajectory struct {
	SchemaVersion string           `json:"schema_version"`
	SessionID     string           `json:"session_id"`
	TaskID        string           `json:"task_id"`
	Agent         AgentInfo        `json:"agent"`
	Steps         []TrajectoryStep `json:"steps"`
	FinalMetrics  TrajectoryStat   `json:"final_metrics"`
	FinalStatus   string           `json:"final_status,omitempty"`
	ExitCode      int              `json:"exit_code"`
	DurationMS    int64            `json:"duration_ms"`
	Error         string           `json:"error,omitempty"`
}

// CaptureOptions controls how harness events are folded into a trajectory.
type CaptureOptions struct {
	SessionID string
	Agent     AgentInfo
	TaskID    string
	StartedAt time.Time // used for relative timestamps; defaults to now()
}

// Capture consumes harness events from ch and returns an ATIF trajectory.
// The function blocks until ch closes. It is the inverse of the Python
// adapter's `populate_context_post_run` hook described in SD-008 §4.
//
// Mapping rules:
//
//   - text_delta events accumulate into a single "agent" step's message.
//     We do not split on token boundaries because Harbor's grader scores
//     the rendered transcript, not the streaming protocol.
//   - tool_call + matching tool_result are paired by ID into one
//     TrajectoryTC entry on a "tool" step.
//   - final carries the exit code, status, total usage, and cost.
//
// Unknown event types (compaction, stall, routing_decision) are recorded
// as "system" steps with their JSON payload in the message field, so
// downstream reporters can still see them without breaking the schema.
func Capture(ch <-chan harnesses.Event, opts CaptureOptions) *Trajectory {
	if opts.StartedAt.IsZero() {
		opts.StartedAt = time.Now().UTC()
	}
	traj := &Trajectory{
		SchemaVersion: ATIFSchemaVersion,
		SessionID:     opts.SessionID,
		TaskID:        opts.TaskID,
		Agent:         opts.Agent,
		Steps:         []TrajectoryStep{},
	}

	pendingTools := make(map[string]*TrajectoryTC)
	var stepID int
	addStep := func(source, message string, tcs []TrajectoryTC) {
		stepID++
		traj.Steps = append(traj.Steps, TrajectoryStep{
			StepID:    stepID,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Source:    source,
			Message:   message,
			ToolCalls: tcs,
		})
	}

	var agentText string
	flushAgentText := func() {
		if agentText == "" {
			return
		}
		addStep("agent", agentText, nil)
		agentText = ""
	}

	for ev := range ch {
		switch ev.Type {
		case harnesses.EventTypeTextDelta:
			var td harnesses.TextDeltaData
			if err := json.Unmarshal(ev.Data, &td); err == nil {
				agentText += td.Text
			}
		case harnesses.EventTypeToolCall:
			flushAgentText()
			var tc harnesses.ToolCallData
			if err := json.Unmarshal(ev.Data, &tc); err == nil {
				entry := &TrajectoryTC{
					Name:  tc.Name,
					Input: append(json.RawMessage(nil), tc.Input...),
				}
				pendingTools[tc.ID] = entry
				addStep("agent", "", []TrajectoryTC{*entry})
			}
		case harnesses.EventTypeToolResult:
			var tr harnesses.ToolResultData
			if err := json.Unmarshal(ev.Data, &tr); err == nil {
				if tc, ok := pendingTools[tr.ID]; ok {
					tc.Output = tr.Output
					tc.Error = tr.Error
				}
				addStep("tool", tr.Output, nil)
			}
		case harnesses.EventTypeFinal:
			flushAgentText()
			var fd harnesses.FinalData
			if err := json.Unmarshal(ev.Data, &fd); err == nil {
				traj.FinalStatus = fd.Status
				traj.ExitCode = fd.ExitCode
				traj.DurationMS = fd.DurationMS
				traj.Error = fd.Error
				if fd.FinalText != "" {
					addStep("agent", fd.FinalText, nil)
				}
				if fd.Usage != nil {
					traj.FinalMetrics = TrajectoryStat{
						InputTokens:  derefInt(fd.Usage.InputTokens),
						OutputTokens: derefInt(fd.Usage.OutputTokens),
						Cost:         fd.CostUSD,
					}
				} else {
					traj.FinalMetrics.Cost = fd.CostUSD
				}
			}
		default:
			// Preserve unknown event payloads so debugging is possible.
			payload := string(ev.Data)
			addStep("system", fmt.Sprintf("%s: %s", ev.Type, payload), nil)
		}
	}
	// Drain any trailing text the harness emitted without a final event.
	flushAgentText()
	return traj
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// WriteHarnessOutput writes the artifacts Harbor's grader expects under
// outDir, mirroring the container layout from SD-008 §4:
//
//	<outDir>/logs/agent/trajectory.json   — ATIF v1.4
//	<outDir>/logs/agent/transcript.txt    — flattened messages, debug aid
//
// The function does NOT write reward.txt or ctrf.json — those come from
// the verifier (pytest run) which is upstream's job.
func WriteHarnessOutput(outDir string, traj *Trajectory) error {
	if outDir == "" {
		return fmt.Errorf("termbench: empty output directory")
	}
	agentDir := filepath.Join(outDir, "logs", "agent")
	if err := os.MkdirAll(agentDir, 0o750); err != nil {
		return fmt.Errorf("termbench: mkdir %s: %w", agentDir, err)
	}
	data, err := json.MarshalIndent(traj, "", "  ")
	if err != nil {
		return fmt.Errorf("termbench: marshal trajectory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "trajectory.json"), data, 0o600); err != nil {
		return fmt.Errorf("termbench: write trajectory: %w", err)
	}
	// Human-readable transcript: useful for triage even when we don't
	// have a Harbor verifier in the loop.
	var transcript string
	for _, s := range traj.Steps {
		transcript += fmt.Sprintf("[%s] %s\n", s.Source, s.Message)
		for _, tc := range s.ToolCalls {
			transcript += fmt.Sprintf("  tool: %s input=%s\n", tc.Name, string(tc.Input))
			if tc.Output != "" {
				transcript += fmt.Sprintf("  -> %s\n", tc.Output)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(agentDir, "transcript.txt"), []byte(transcript), 0o600); err != nil {
		return fmt.Errorf("termbench: write transcript: %w", err)
	}
	return nil
}
