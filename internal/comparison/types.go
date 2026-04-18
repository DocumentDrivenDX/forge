// Package comparison provides cross-harness comparison, benchmarking, and
// quorum primitives for the ddx-agent integration suite.
package comparison

import "time"

// RunResult is the minimal result shape the comparison engine needs from a
// single harness invocation. Callers adapt their concrete result type (e.g.
// agent.Result in DDx, or service-level events in agent) to RunResult.
type RunResult struct {
	Harness      string
	Model        string
	Output       string
	ToolCalls    []ToolCallEntry
	Tokens       int
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	DurationMS   int
	ExitCode     int
	Error        string
}

// ToolCallEntry records one tool execution during an agent run.
type ToolCallEntry struct {
	Tool     string `json:"tool"`
	Input    string `json:"input"`
	Output   string `json:"output,omitempty"`
	Duration int    `json:"duration_ms,omitempty"`
	Error    string `json:"error,omitempty"`
}

// RunFunc is the single-invocation primitive that RunCompare and RunQuorum
// drive. It receives a harness name and a prompt, and returns a RunResult.
// Callers wire this to whatever execution engine they use (DDx Runner.Run,
// agent service.Execute, etc.).
type RunFunc func(harness, model, prompt string) RunResult

// CompareOptions configures a comparison dispatch.
type CompareOptions struct {
	Harnesses   []string       // harnesses to compare
	ArmModels   map[int]string // per-arm model overrides keyed by arm index
	ArmLabels   map[int]string // per-arm display labels
	Prompt      string         // prompt text
	WorkDir     string         // working directory for worktree operations
	Sandbox     bool           // run each arm in an isolated worktree
	KeepSandbox bool           // preserve worktrees after comparison
	PostRun     string         // command to run in each worktree after the agent completes
}

// QuorumOptions configures a quorum dispatch.
type QuorumOptions struct {
	Harnesses []string // multiple harnesses to invoke
	Strategy  string   // any, majority, unanimous, or numeric
	Threshold int      // numeric threshold (when Strategy is "")
	Prompt    string
	Model     string
}

// ComparisonArm holds the result of one harness arm in a comparison.
type ComparisonArm struct {
	Harness      string          `json:"harness"`
	Model        string          `json:"model,omitempty"`
	Output       string          `json:"output"`
	Diff         string          `json:"diff,omitempty"`         // git diff of side effects
	ToolCalls    []ToolCallEntry `json:"tool_calls,omitempty"`   // agent tool call log
	PostRunOut   string          `json:"post_run_out,omitempty"` // post-run command output
	PostRunOK    *bool           `json:"post_run_ok,omitempty"`  // post-run pass/fail
	Tokens       int             `json:"tokens,omitempty"`
	InputTokens  int             `json:"input_tokens,omitempty"`
	OutputTokens int             `json:"output_tokens,omitempty"`
	CostUSD      float64         `json:"cost_usd,omitempty"`
	DurationMS   int             `json:"duration_ms"`
	ExitCode     int             `json:"exit_code"`
	Error        string          `json:"error,omitempty"`
}

// ComparisonRecord is the complete record of a comparison run.
type ComparisonRecord struct {
	ID        string          `json:"id"`
	Timestamp time.Time       `json:"timestamp"`
	Prompt    string          `json:"prompt"`
	Arms      []ComparisonArm `json:"arms"`
}

// BenchmarkPrompt is a single test case in a benchmark suite.
type BenchmarkPrompt struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Prompt      string   `json:"prompt"`                // inline prompt text
	PromptFile  string   `json:"prompt_file,omitempty"` // or path to prompt file
	Tags        []string `json:"tags,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
}

// BenchmarkArm defines one arm in a benchmark suite.
type BenchmarkArm struct {
	Label   string `json:"label"`
	Harness string `json:"harness"`
	Tier    string `json:"tier,omitempty"`  // "smart" | "standard" | "cheap"
	Model   string `json:"model,omitempty"` // explicit override
}

// BenchmarkSuite defines a repeatable set of comparison runs.
type BenchmarkSuite struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Version     string            `json:"version"`
	Arms        []BenchmarkArm    `json:"arms"`
	Prompts     []BenchmarkPrompt `json:"prompts"`
	Sandbox     bool              `json:"sandbox,omitempty"`
	PostRun     string            `json:"post_run,omitempty"`
	Timeout     string            `json:"timeout,omitempty"`
}

// BenchmarkResult is the output of running a full benchmark suite.
type BenchmarkResult struct {
	Suite       string             `json:"suite"`
	Version     string             `json:"version"`
	Timestamp   time.Time          `json:"timestamp"`
	Arms        []BenchmarkArm     `json:"arms"`
	Comparisons []ComparisonRecord `json:"comparisons"`
	Summary     BenchmarkSummary   `json:"summary"`
}

// BenchmarkArmSummary aggregates stats for one arm across all prompts.
type BenchmarkArmSummary struct {
	Label        string  `json:"label"`
	Completed    int     `json:"completed"`
	Failed       int     `json:"failed"`
	TotalTokens  int     `json:"total_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	AvgDurationMS int    `json:"avg_duration_ms"`
	AvgScore     float64 `json:"avg_score,omitempty"`
}

// BenchmarkSummary aggregates stats across all arms and prompts.
type BenchmarkSummary struct {
	TotalPrompts int                   `json:"total_prompts"`
	Arms         []BenchmarkArmSummary `json:"arms"`
}
