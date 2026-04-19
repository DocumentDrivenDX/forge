# CONTRACT-003: DdxAgent Service Interface

**Status:** Draft
**Owner:** DDX Agent maintainers
**Replaces:** CONTRACT-002-ddx-harness-interface (deleted; entanglement-era contract)

## Purpose

This contract defines the **entire public Go surface** of the `ddx-agent` module.
Anything not reachable through the `DdxAgent` interface — or through the input/output
struct types referenced by its methods — is internal and may change without notice.

Consumers (DDx CLI, future HELIX/Dun integrations, the standalone `ddx-agent`
binary, anything else) interact only through this surface. **They do not import
agent internal packages.** When new behavior is needed, consumers file an issue or
PR against this contract; agent maintainers decide whether the surface grows.

## Module value proposition

`ddx-agent` is the one stop shop for optimally routed one-shot noninteractive
agentic prompts. Two roles:

1. **Direct first-class agent** over native model providers (LM Studio, OpenRouter,
   Anthropic, etc.). Designed to be the high-performance choice for batch
   noninteractive tasks.
2. **Wrapper around other agents** — subprocess harnesses (claude, codex,
   opencode, pi, gemini) — used when their interactive features, vendor billing,
   specific capabilities, or comparison/fallback routing wants them in the
   candidate pool.

A single internal routing engine ranks `(harness, provider?, model)` candidates
uniformly. Consumers see one surface; the internals decide how to dispatch.

## Interface

```go
package agentlib

import (
    "context"
    "io"
    "time"
    "encoding/json"
)

// DdxAgent is the entire public Go surface of the ddx-agent module.
type DdxAgent interface {
    // Execute runs an agent task in-process; emits Events on the channel until
    // the task terminates (channel closes). The final event (type=final) carries
    // status, usage, cost, session log path, optional message history, and
    // routing_actual (the resolved fallback chain that fired).
    Execute(ctx context.Context, req ExecuteRequest) (<-chan Event, error)

    // TailSessionLog streams events from a previously-started or in-progress
    // session by ID. Used by clients (DDx workers, UIs) to subscribe to a run
    // started elsewhere — e.g., a server-managed worker that the CLI wants to
    // follow. Multi-subscriber-safe.
    TailSessionLog(ctx context.Context, sessionID string) (<-chan Event, error)

    // ListHarnesses returns metadata for every registered harness (native and
    // subprocess). HarnessInfo includes install state, supported permission
    // levels, supported reasoning values, and live quota when applicable.
    ListHarnesses(ctx context.Context) ([]HarnessInfo, error)

    // ListProviders returns providers known to the native-agent harness with
    // live status, configured-default markers, and cooldown state.
    ListProviders(ctx context.Context) ([]ProviderInfo, error)

    // ListModels returns models matching the filter, with full metadata
    // (cost, perf signals, capabilities, context length, ranking).
    ListModels(ctx context.Context, filter ModelFilter) ([]ModelInfo, error)

    // HealthCheck triggers a fresh probe and updates internal state.
    // Target.Type is "harness" or "provider".
    HealthCheck(ctx context.Context, target HealthTarget) error

    // ResolveRoute resolves a single under-specified request to a concrete
    // (Harness, Provider, Model). The returned RouteDecision can be passed
    // back to Execute via ExecuteRequest.PreResolved to skip re-resolution
    // (used by dry-run-then-execute flows).
    ResolveRoute(ctx context.Context, req RouteRequest) (*RouteDecision, error)

    // RouteStatus returns global routing state across all routes: cooldowns,
    // recent decisions, observation-derived per-(provider,model) latency.
    // Distinct from per-request ResolveRoute — this is the read-only operator
    // dashboard view.
    RouteStatus(ctx context.Context) (*RouteStatusReport, error)
}

// New constructs a DdxAgent. Options is intentionally minimal.
func New(opts Options) (DdxAgent, error)
```

**Eight methods total.** `Execute` is the primary verb; `TailSessionLog`,
`ListHarnesses`, `ListProviders`, `ListModels`, `HealthCheck`, `ResolveRoute`,
`RouteStatus` are the supporting surface. (Counted as "7 + New" in earlier
drafts; interface has 8 methods.)

## Public types

```go
type Options struct {
    ConfigPath string    // optional override; default $XDG_CONFIG_HOME/ddx-agent/config.yaml
    Logger     io.Writer // optional; agent writes structured session logs internally regardless

    // Test-only injection seams. Each MUST be nil in production builds —
    // enforced by build tag `//go:build testseam`. Forming an Options with
    // any of these set in a non-test build is a compile error. Four seams
    // exist because consumers today inject at four different layers.
    FakeProvider            *FakeProvider
    PromptAssertionHook     PromptAssertionHook
    CompactionAssertionHook CompactionAssertionHook
    ToolWiringHook          ToolWiringHook
}

// Reasoning is the single public model-reasoning control. It is one scalar:
// named values such as auto/off/low/medium/high/minimal/xhigh/max, or numeric
// strings produced by ReasoningTokens for explicit token budgets.
//
// The root package may re-export this type, constants, and helper from a shared
// leaf package such as internal/reasoning. Internal packages such as
// internal/modelcatalog import the leaf package, not root agent, to avoid
// root-agent/internal-modelcatalog import cycles.
type Reasoning string

const (
    ReasoningAuto   Reasoning = "auto"
    ReasoningOff    Reasoning = "off"
    ReasoningLow    Reasoning = "low"
    ReasoningMedium Reasoning = "medium"
    ReasoningHigh   Reasoning = "high"
    ReasoningMinimal Reasoning = "minimal" // accepted only when advertised
    ReasoningXHigh  Reasoning = "xhigh"    // normalizes x-high
    ReasoningMax    Reasoning = "max"      // requires known model/provider max
)

func ReasoningTokens(n int) Reasoning

type ExecuteRequest struct {
    Prompt       string  // required
    SystemPrompt string  // optional; agent supplies a sane default if empty
    Model        string  // optional; resolved via ResolveRoute if empty
    Provider     string  // optional preference (soft); empty = router decides
    Harness      string  // optional preference (hard); empty = router decides
    ModelRef     string  // optional alias from the catalog: cheap/standard/smart/<custom>
    Temperature  float32 // model sampling temperature; 0 = deterministic
    Seed         int64   // sampling seed; 0 = unset/provider chooses
    Reasoning    Reasoning // optional; auto|off|low|medium|high|minimal|xhigh|max|<tokens>
    Permissions  string  // "safe" | "supervised" | "unrestricted"; default "safe"
    WorkDir      string  // required when the chosen harness uses tools

    // PreResolved bypasses ResolveRoute when the caller already has a decision
    // (e.g., from a prior ResolveRoute call). When non-nil, agent uses these
    // values verbatim and does not re-route. Provider/Model/Harness fields
    // above are ignored in this mode.
    PreResolved *RouteDecision

    // Three independent timeout knobs:
    //   Timeout         — wall-clock cap; the request fails after this duration
    //                     regardless of activity. 0 = no cap.
    //   IdleTimeout     — streaming-quiet cap; the request fails after this
    //                     duration of no events from the model. 0 = use harness
    //                     default (typically 60s).
    //   ProviderTimeout — per-HTTP-request cap to the provider; longer requests
    //                     are retried per the harness's failover rules. 0 = use
    //                     provider default.
    Timeout         time.Duration
    IdleTimeout     time.Duration
    ProviderTimeout time.Duration

    // Optional stall policy. When non-nil, agent enforces and ends execution
    // with Status="stalled" if any limit hits. The agent also derives an
    // implicit MaxIterations ceiling from StallPolicy (typically 2× the
    // ReadOnly limit) — caller does not configure MaxIterations directly.
    StallPolicy *StallPolicy

    // SessionLogDir overrides the default session-log directory for this
    // request. Used by execute-bead to direct logs into a per-bundle evidence
    // directory. Empty = use Options.ConfigPath-derived default.
    SessionLogDir string

    // Metadata is bidirectional: echoed back in every Event via Event.Metadata,
    // AND stamped onto every line written to the session log (e.g., bead_id,
    // attempt_id) so external log consumers can correlate.
    Metadata map[string]string
}

type StallPolicy struct {
    MaxReadOnlyToolIterations int // 0 = disabled
    MaxNoopCompactions        int // 0 = disabled
}

type RouteRequest struct {
    Model       string
    Provider    string
    Harness     string
    ModelRef    string
    Reasoning   Reasoning
    Permissions string
}

type RouteDecision struct {
    Harness    string
    Provider   string  // empty for harnesses without provider concept
    Model      string
    Reason     string  // human-readable explanation
    Candidates []Candidate  // full ranking, including rejected candidates
}

type Candidate struct {
    Harness       string
    Provider      string
    Model         string
    Score         float64
    Eligible      bool
    Reason        string
    EstimatedCost CostEstimate
    PerfSignal    PerfSignal
}

type HarnessInfo struct {
    Name                 string
    Type                 string   // "native" | "subprocess"
    Available            bool
    Path                 string   // for subprocess harnesses
    Error                string   // when Available=false
    IsLocal              bool
    IsSubscription       bool
    ExactPinSupport      bool
    SupportedPermissions []string // subset of {"safe","supervised","unrestricted"}
    SupportedReasoning   []string // values such as {"off","low","medium","high","minimal","xhigh","max"}
    CostClass            string   // "local" | "cheap" | "medium" | "expensive"
    Quota                *QuotaState // nil if not applicable; live field
}

type ProviderInfo struct {
    Name          string
    Type          string  // "openai-compat" | "anthropic" | "virtual"
    BaseURL       string
    Status        string  // "connected" | "unreachable" | "error: <msg>"
    ModelCount    int
    Capabilities  []string  // {"tool_use","vision","json_mode","streaming"}
    IsDefault     bool      // matches the configured default_provider
    DefaultModel  string    // the per-provider configured default model, if any
    CooldownState *CooldownState  // nil if not in cooldown
}

type ModelInfo struct {
    ID            string
    Provider      string
    Harness       string  // for subprocess-only models, the owning harness
    ContextLength int     // resolved (provider API > catalog > default)
    Capabilities  []string
    Cost          CostInfo
    PerfSignal    PerfSignal
    Available     bool
    IsConfigured  bool    // matches an explicit model_routes entry
    IsDefault     bool    // matches the configured default model
    CatalogRef    string  // canonical catalog reference if recognized
    ReasoningDefault Reasoning // catalog/provider default for this model, if known
    ReasoningMaxTokens int     // 0 when unknown or not applicable
    RankPosition  int     // ordinal in the latest discovery rank for this provider; -1 if unranked
}

type ModelFilter struct {
    Harness  string  // empty = all harnesses
    Provider string  // empty = all providers
}

type HealthTarget struct {
    Type string  // "harness" | "provider"
    Name string
}

type CooldownState struct {
    Reason    string    // "consecutive_failures" | "manual" | etc.
    Until     time.Time
    FailCount int
    LastError string
}

type RouteStatusReport struct {
    Routes          []RouteStatusEntry
    GeneratedAt     time.Time
    GlobalCooldowns []CooldownState
}

type RouteStatusEntry struct {
    Model          string                  // route key
    Strategy       string                  // "priority-round-robin" | "first-available"
    Candidates     []RouteCandidateStatus
    LastDecision   *RouteDecision          // most recent ResolveRoute result for this key (cached)
    LastDecisionAt time.Time
}

type RouteCandidateStatus struct {
    Provider          string
    Model             string
    Priority          int
    Healthy           bool
    Cooldown          *CooldownState
    RecentLatencyMS   float64  // observation-derived
    RecentSuccessRate float64  // 0-1
}

type Event struct {
    Type     string          // see event types below
    Sequence int64
    Time     time.Time
    Metadata map[string]string  // echoed from ExecuteRequest.Metadata
    Data     json.RawMessage    // shape depends on Type; see schemas below
}
```

## Event JSON shapes

Closed union of event types. Every harness backend emits these identically.

```jsonc
// type=text_delta
{"text": "..."}

// type=tool_call
{"id": "...", "name": "find", "input": {...}}

// type=tool_result
{"id": "...", "output": "...", "error": "...", "duration_ms": 123}

// type=compaction
// (Emitted ONLY when actual compaction work was performed. No-op compactions
// emit nothing — the compactor was asked, decided no work needed, returned silently.)
{"messages_before": 30, "messages_after": 12, "tokens_freed": 4521}

// type=routing_decision
// (Emitted at start of execution.)
{
  "harness": "agent",
  "provider": "bragi",
  "model": "qwen/qwen3.6-35b-a3b",
  "reason": "cheap-tier match; bragi reachable; 256K context",
  "fallback_chain": ["openrouter:qwen/qwen3.6"]
}

// type=stall
// (Emitted just before final when StallPolicy triggers.)
{"reason": "no_compactions_exceeded", "count": 50}

// type=final
// (Emitted last; channel closes after.)
{
  "status": "success" | "failed" | "stalled" | "timed_out" | "cancelled",
  "exit_code": 0,
  "error": "",
  "duration_ms": 12345,
  "usage": {"input_tokens": 7996, "output_tokens": 267, "total_tokens": 8263},
  "cost_usd": 0.0042,
  "session_log_path": "/path/to/session.jsonl",
  "messages": [...],   // optional history continuation
  "routing_actual": {
    "harness": "agent",
    "provider": "openrouter",   // distinct from start-event routing_decision when fallback fired
    "model": "qwen/qwen3.6",
    "fallback_chain_fired": ["bragi:qwen/qwen3.6 (timeout)", "openrouter:qwen/qwen3.6 (success)"]
  }
}
```

## Test seam types

```go
// FakeProvider supports three patterns:
//   - Static script: sequence of pre-recorded responses, consumed in order.
//   - Dynamic callback: function called per request returning a response.
//   - Error injection: per-call status override.
type FakeProvider struct {
    Static      []FakeResponse                            // for static script pattern
    Dynamic     func(req FakeRequest) (FakeResponse, error)  // for dynamic per-call pattern
    InjectError func(callIndex int) error                 // for error injection pattern
}

type FakeRequest struct {
    Messages []Message
    Tools    []string
    Model    string
}

type FakeResponse struct {
    Text      string
    ToolCalls []ToolCall
    Usage     TokenUsage
    Status    string  // "success" by default
}

// PromptAssertionHook is called once per Execute, with the system+user prompt
// the agent actually sent to the model. Used by tests that verify prompt
// construction/compaction without running a real provider.
type PromptAssertionHook func(systemPrompt, userPrompt string, contextFiles []string)

// CompactionAssertionHook is called whenever a real compaction runs. No-op
// compactions are NOT delivered (they don't fire compaction events either).
type CompactionAssertionHook func(messagesBefore, messagesAfter int, tokensFreed int)

// ToolWiringHook is called once per Execute, with the resolved tool list and
// the harness that received it. Used by tests that verify the right tools
// land at the right harness given the request's permission level.
type ToolWiringHook func(harness string, toolNames []string)
```

## Reasoning contract

`Reasoning` is the only preferred public control for model-side reasoning.
Consumers do not set separate public thinking, effort, level, or budget fields.
The scalar accepts named values (`auto`, `off`, `low`, `medium`, `high`) and
provider/harness-supported extended values such as `minimal`, `xhigh` /
`x-high`, and `max`. It also accepts numeric values through
`ReasoningTokens(n)`, where `0` means explicit off and positive integers mean
an explicit max reasoning-token budget or documented provider-equivalent
numeric value.

Normalization is tri-state:

- Empty means no caller preference.
- `auto` means resolve model, catalog, or provider defaults.
- `off`, `none`, `false`, and `0` mean explicit reasoning off.
- Positive integers mean an explicit numeric request.

Default portable named-to-token budgets are `low=2048`, `medium=8192`, and
`high=32768` only when a selected provider/model does not publish a more
specific map. Providers and subprocess harnesses may map resolved reasoning to
wire or CLI knobs named `reasoning`, `thinking`, `effort`, `variant`, or a
numeric budget. They may also drop auto/default reasoning controls for models
that do not support explicit reasoning control. Explicit unsupported values,
unknown extended values, and over-limit numeric values fail clearly.

Catalog tier defaults are part of route resolution: below-smart tiers
(`cheap`, `fast`, `standard`, `code-economy`, and `code-medium`) default to
`reasoning=off`, including local/economy Qwen targets. `smart` and `code-high`
default to `reasoning=high`. Any explicit caller `Reasoning` value wins over
these defaults, including supported values above high such as `xhigh` or
`max`, and numeric values.

## Sampling contract

`ExecuteRequest.Temperature` and `ExecuteRequest.Seed` are the portable
sampling controls. `Temperature=0` requests deterministic sampling. `Seed=0`
means unset and lets the provider choose. Native OpenAI-compatible providers
honor both fields. Providers or subprocess harnesses that do not expose an
equivalent seed control may ignore `Seed`; callers that require strict parity
must treat those runs as advisory/non-deterministic.

## Bead Execution Policy

DDx bead implementation should use a two-pass policy against this service:
try a cheap or standard profile with `reasoning=off` first, then escalate only
when the first pass produced evidence that a smarter model is likely to help.
The initial pass should use `ModelRef=cheap` or `ModelRef=standard` with an
explicit `ReasoningOff` request so local/economy models do not spend
reasoning tokens by default.

Smart retry is eligible when the first pass failed because of model capability,
reasoning quality, a post-implementation test failure, or an explicit agent
failure after the agent had a valid checkout and attempted the bead. The retry
uses `ModelRef=smart` and the smart-tier catalog default, currently
`reasoning=high`, unless the caller supplies a tighter explicit value. The
retry must preserve the same bead context and retain first-pass logs/evidence
so reviewers can compare the cheap attempt with the smart attempt.

Smart retry is not eligible for deterministic setup failures: dirty-worktree or
merge conflicts, missing repository checkout, invalid bead metadata, unresolved
dependencies, config parse errors, missing harness binaries, authentication
setup failures, or command-not-found/toolchain setup failures. These failures
should stop with actionable evidence instead of spending a smart attempt.

Cost caps, timeout limits, permission policy, and determinism controls apply
across both passes as one execution budget. The agent-side contract defines the
fields and semantics; the DDx execute-loop implementation is tracked in the
paired DDx repo bead `ddx-785d02f7`.

## Behaviors the contract guarantees

The agent owns these execution-time behaviors. Callers do not opt in or out.

- **Orphan-model validation.** When `Model` is set but matches no provider's
  discovery and no catalog entry, `Execute` fails fast with
  `Status="failed", error="orphan model: <name>"` rather than silently picking
  the wrong provider.

- **Provider request deadline wrapping.** Every HTTP call to a provider is
  wrapped with `ProviderTimeout`. Per-request failures classified as
  transport/auth/upstream are eligible for failover within the route's
  candidate list; prompt/tool-schema errors are not.

- **Route-reason attribution.** The start-event `routing_decision` and
  final-event `routing_actual` together capture why each candidate was
  tried/picked.

- **Stall detection.** Per `StallPolicy`. Default policy (when caller passes
  `nil`) uses conservative limits matching today's circuit-breaker thresholds.

- **Compaction-stuck breaking.** Implicit in the `StallPolicy` default;
  callers don't configure separately.

- **OS-level subprocess cleanup.** On `ctx.Done()`, agent reaps PTY and
  orphan processes for subprocess harnesses. Tested and guaranteed.

- **No-op compaction telemetry suppression.** Compaction events fire ONLY
  when actual work was performed. The compactor's pre-/post-turn checkpoint
  probes that decide "no compaction needed" emit nothing at default verbosity.

## Extensions and stability

This contract is the boundary. Internal packages (`compaction`, `prompt`,
`tool`, `session`, `observations`, `modelcatalog`, `provider/*`) live under
`internal/` and the Go compiler blocks external imports.

When a consumer needs new contract behavior, file a PR against this document
proposing the addition. Maintainers decide whether the surface grows. Do not
work around the boundary by importing internals (impossible after `internal/`
enforcement) or by forking the module.
