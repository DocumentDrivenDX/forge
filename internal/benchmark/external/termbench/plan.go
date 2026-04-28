package termbench

import (
	"time"

	agent "github.com/DocumentDrivenDX/agent"
)

// ExecutionPlan is the ServiceExecuteRequest payload + ancillary metadata
// for one TerminalBench task. The caller (cmd/bench) is responsible for
// invoking agent.Service.Execute with Request and consuming the resulting
// event channel; this package never spawns a goroutine of its own.
type ExecutionPlan struct {
	// Task is the source task (kept for downstream reporting).
	Task *Task

	// Request is the ServiceExecuteRequest the caller should hand to
	// agent.New(...).Execute. WorkDir is left to the caller because a
	// real Harbor run mounts the task workspace at a container path
	// (/app), while a dry-run from cmd/bench may point at a temp dir.
	Request agent.ServiceExecuteRequest

	// Timeout matches the task's MaxAgentTimeoutSec budget, suitable for
	// a context.WithTimeout wrapping Execute.
	Timeout time.Duration
}

// PlanOptions tunes how a Task is converted into a ServiceExecuteRequest.
// The defaults match what SD-008 §3 + §5 documented for the Harbor smoke
// run, so callers can leave most fields zero-valued.
type PlanOptions struct {
	// Harness is the agent harness label (e.g. "ddx-agent", "claude-code",
	// "codex"). Passed through verbatim into ServiceExecuteRequest.
	Harness string

	// Model is the provider model ID (e.g. "openrouter/qwen/qwen3.6-plus").
	Model string

	// WorkDir is the directory the agent operates in. For a real Harbor
	// trial this is /app inside the container; for a Go-side dry-run it
	// can be a tempdir seeded with the task workspace.
	WorkDir string

	// Permissions, if non-empty, overrides the default "safe" preset. The
	// TerminalBench tasks routinely require shell + edit access so the
	// caller may want "trusted" here.
	Permissions string

	// Seed enables deterministic sampling (matches cmd/bench parity runs).
	// Zero means "leave unset".
	Seed int64

	// Temperature, if non-nil, overrides the request temperature. The
	// default-zero behavior is "leave unset" so the agent's own bench
	// path can pin temperature to 0 separately.
	Temperature *float32
}

// BuildPlan converts a Task into an ExecutionPlan. It does not execute
// anything; callers are free to inspect or mutate Request before handing
// it to agent.Service.Execute.
//
// The instruction text is used verbatim as the prompt — TerminalBench
// tasks are written to be agent-ready, so wrapping them in additional
// scaffolding would change the contract the upstream grader expects.
func BuildPlan(task *Task, opts PlanOptions) *ExecutionPlan {
	if task == nil {
		return nil
	}
	perms := opts.Permissions
	if perms == "" {
		// Use "trusted" — TerminalBench tasks routinely require edits and
		// shell. "safe" rejects most operations and would dead-end nearly
		// every task. The grader is the source of truth for whether the
		// agent did the right thing; permissions inside the container do
		// not change that signal.
		perms = "trusted"
	}
	req := agent.ServiceExecuteRequest{
		Harness:     opts.Harness,
		Model:       opts.Model,
		Prompt:      task.Instruction,
		WorkDir:     opts.WorkDir,
		Permissions: perms,
	}
	if opts.Temperature != nil {
		req.Temperature = opts.Temperature
	}
	if opts.Seed != 0 {
		s := opts.Seed
		req.Seed = &s
	}
	timeout := time.Duration(task.MaxAgentTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(DefaultAgentTimeoutSec) * time.Second
	}
	return &ExecutionPlan{
		Task:    task,
		Request: req,
		Timeout: timeout,
	}
}
