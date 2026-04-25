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
    // status, normalized final_text, usage, cost, session log path, optional
    // message history, and routing_actual (the resolved fallback chain that fired).
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
    // (cost, perf signals, capabilities, context length, ranking, provider
    // type, and endpoint identity when applicable).
    ListModels(ctx context.Context, filter ModelFilter) ([]ModelInfo, error)

    // ListProfiles returns catalog profile names and alias projections with
    // provenance metadata. Consumers use this instead of reading
    // ~/.config/agent/models.yaml.
    ListProfiles(ctx context.Context) ([]ProfileInfo, error)

    // ResolveProfile projects one profile or alias into service-supported
    // surfaces. Surface names are public service names, not internal catalog
    // keys such as "agent.openai".
    ResolveProfile(ctx context.Context, name string) (*ResolvedProfile, error)

    // ProfileAliases returns known alias -> canonical profile/target mappings,
    // including deprecated aliases mapped to their replacement when available.
    ProfileAliases(ctx context.Context) (map[string]string, error)

    // HealthCheck triggers a fresh probe and updates internal state.
    // Target.Type is "harness" or "provider".
    HealthCheck(ctx context.Context, target HealthTarget) error

    // ResolveRoute resolves a single under-specified request to a concrete
    // (Harness, Provider, Model). The returned RouteDecision is informational —
    // operator dashboards, route-status displays, debug surfaces — and is not
    // re-injectable into Execute. Execute always re-resolves on its own inputs
    // (idempotent for the same caller intent, modulo health changes which is
    // the intended behavior).
    ResolveRoute(ctx context.Context, req RouteRequest) (*RouteDecision, error)

    // RecordRouteAttempt records caller feedback about a routed candidate.
    // Non-success statuses create a same-process cooldown keyed by
    // harness/provider/model/endpoint; success clears matching active failures.
    RecordRouteAttempt(ctx context.Context, attempt RouteAttempt) error

    // RouteStatus returns global routing state across all routes: cooldowns,
    // recent decisions, observation-derived per-(provider,model) latency.
    // Distinct from per-request ResolveRoute — this is the read-only operator
    // dashboard view.
    RouteStatus(ctx context.Context) (*RouteStatusReport, error)

    // UsageReport aggregates token, cost, and reliability totals across the
    // service-owned session-log directory. CLI subcommands such as
    // `ddx-agent usage` consume this projection rather than re-reading
    // session-log JSONL records.
    UsageReport(ctx context.Context, opts UsageReportOptions) (*UsageReport, error)

    // ListSessionLogs returns the historical session-log entries known to the
    // service (session id, mod time, size). Consumers display these without
    // touching the on-disk session-log layout.
    ListSessionLogs(ctx context.Context) ([]SessionLogEntry, error)

    // WriteSessionLog renders every event in the named session log to w as
    // indented JSON, one event per object. The format is service-owned;
    // consumers do not parse it back into private session-log structs.
    WriteSessionLog(ctx context.Context, sessionID string, w io.Writer) error

    // ReplaySession renders a human-readable conversation transcript for the
    // named session log onto w. Used by `ddx-agent replay <id>`.
    ReplaySession(ctx context.Context, sessionID string, w io.Writer) error
}

// ValidateUsageSince returns nil when spec is a usage window value accepted by
// UsageReport. CLI subcommands call this to surface validation errors with
// exit-code 2 before invoking the service.
func ValidateUsageSince(spec string) error

// New constructs a DdxAgent. Options is intentionally minimal.
func New(opts Options) (DdxAgent, error)
```

**Sixteen methods total.** `Execute` is the primary verb; `TailSessionLog`,
`ListHarnesses`, `ListProviders`, `ListModels`, `ListProfiles`,
`ResolveProfile`, `ProfileAliases`, `HealthCheck`, `ResolveRoute`,
`RecordRouteAttempt`, and `RouteStatus` are the supporting routing/status
surface; `UsageReport`, `ListSessionLogs`, `WriteSessionLog`, and
`ReplaySession` are the historical session-log projection used by
`ddx-agent log`, `replay`, and `usage`.

## Public types

```go
type Options struct {
    ConfigPath string    // optional override; default $XDG_CONFIG_HOME/ddx-agent/config.yaml
    Logger     io.Writer // optional; agent writes structured session logs internally regardless

    // SessionLogDir overrides the directory used by the historical session-log
    // projections (UsageReport, ListSessionLogs, WriteSessionLog,
    // ReplaySession). Empty falls back to ServiceConfig.WorkDir() +
    // "/.agent/sessions". Per-Execute requests still set their own
    // ExecuteRequest.SessionLogDir.
    SessionLogDir string

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

// Tool is the native agent tool interface. ExecuteRequest.Tools is only used
// by the in-process `agent` harness; subprocess harnesses own their tool
// policy internally.
type Tool interface {
    Name() string
    Description() string
    Schema() json.RawMessage
    Execute(ctx context.Context, params json.RawMessage) (string, error)
    Parallel() bool
}

// Routing placement is profile-owned. Callers either choose a named Profile
// (cheap, standard, smart, or a user-defined profile) or pin Provider+Model
// directly. Profiles are catalog/config data bundles that can carry placement
// order, cost ceilings, failure policy, and reasoning defaults; callers do not
// pass a per-request local/subscription preference enum.

type ExecuteRequest struct {
    Prompt       string  // required
    SystemPrompt string  // optional; agent supplies a sane default if empty
    Model        string  // optional; resolved via ResolveRoute if empty
    Provider     string  // optional preference (soft); empty = router decides
    Harness      string  // optional preference (hard); empty = router decides
    Profile      string  // optional named routing policy bundle: cheap/standard/smart/custom
    ModelRef     string  // optional alias from the catalog: cheap/standard/smart/<custom>
    Temperature  float32 // model sampling temperature; 0 = deterministic
    Seed         int64   // sampling seed; 0 = unset/provider chooses
    Reasoning    Reasoning // optional; auto|off|low|medium|high|minimal|xhigh|max|<tokens>
    Permissions  string  // "safe" | "supervised" | "unrestricted"; default "safe"
    WorkDir      string  // required when the chosen harness uses tools
    Tools        []Tool  // optional native-agent override; nil = built-in tools
    ToolPreset   string  // optional native built-in selector; "benchmark" excludes task

    // Auto-selection inputs. When the caller pins nothing (Profile, Model,
    // ModelRef, Provider all empty), Execute uses these to filter candidates
    // by capability before scoring. Explicit pins always win — these never
    // override an explicit Provider/Model. Defaults: 0 / false skip the
    // corresponding filter. See ADR-005.
    EstimatedPromptTokens int  // when >0, filter candidates whose context window cannot hold the prompt
    RequiresTools         bool // when true, filter providers whose SupportsTools() is false

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

Native `agent` permission modes are enforced by tool exposure at the service
boundary:

- `safe` (and empty/default) exposes only read-only built-ins: `read`, `find`,
  `grep`, and `ls`.
- `unrestricted` exposes the full native built-in tool set for the request's
  `ToolPreset`.
- `supervised` is rejected for the native `agent` harness until an approval loop
  exists. Subprocess harnesses may still implement their own supervised modes.

type RouteRequest struct {
    Profile               string
    Model                 string
    Provider              string
    Harness               string
    ModelRef              string
    Reasoning             Reasoning
    Permissions           string
    EstimatedPromptTokens int  // when >0, filter candidates whose context window cannot hold the prompt
    RequiresTools         bool // when true, filter providers whose SupportsTools() is false
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

type RouteAttempt struct {
    Harness   string
    Provider  string
    Model     string
    Endpoint  string
    Status    string        // "success" clears active failures; other values record failure
    Reason    string        // machine-readable failure reason when available
    Error     string        // human-readable failure detail
    Duration  time.Duration
    Timestamp time.Time     // zero = service clock
}

type HarnessInfo struct {
    Name                 string
    Type                 string   // "native" | "subprocess"
    Available            bool
    Path                 string   // for subprocess harnesses
    Error                string   // when Available=false
    IsLocal              bool
    IsSubscription       bool
    TestOnly             bool     // true for sentinel harnesses excluded from production routing
    ExactPinSupport      bool
    DefaultModel         string   // built-in default model when no override is supplied
    SupportedPermissions []string // subset of {"safe","supervised","unrestricted"}
    SupportedReasoning   []string // values such as {"off","low","medium","high","minimal","xhigh","max"}
    CostClass            string   // "local" | "cheap" | "medium" | "expensive"
    Quota                *QuotaState // nil if not applicable; live field
    Account              *AccountStatus
    UsageWindows         []UsageWindow
    LastError            *StatusError
    CapabilityMatrix     HarnessCapabilityMatrix
}

type QuotaState struct {
    Windows    []QuotaWindow
    CapturedAt time.Time
    Fresh      bool
    Source     string
    Status     string // ok|blocked|stale|unavailable|unauthenticated|unknown
    LastError  *StatusError
}

type QuotaWindow struct {
    Name          string
    LimitID       string
    WindowMinutes int
    UsedPercent   float64
    ResetsAt      string
    ResetsAtUnix  int64
    State         string // ok|blocked|unknown
}

type AccountStatus struct {
    Authenticated   bool
    Unauthenticated bool
    Email           string
    PlanType        string
    OrgName         string
    Source          string
    CapturedAt      time.Time
    Fresh           bool
    Detail          string
}

type UsageWindow struct {
    Name         string
    Source       string
    CapturedAt   time.Time
    Fresh        bool
    InputTokens  int
    OutputTokens int
    TotalTokens  int
    CostUSD      float64
}

type EndpointStatus struct {
    Name          string
    BaseURL       string
    ProbeURL      string
    Status        string // connected|unreachable|unauthenticated|error|unknown
    Source        string
    CapturedAt    time.Time
    Fresh         bool
    LastSuccessAt time.Time
    ModelCount    int
    LastError     *StatusError
}

type StatusError struct {
    Type      string // unavailable|unauthenticated|error
    Detail    string
    Source    string
    Timestamp time.Time
}

type HarnessCapabilityStatus string

const (
    HarnessCapabilityRequired      HarnessCapabilityStatus = "required"
    HarnessCapabilityOptional      HarnessCapabilityStatus = "optional"
    HarnessCapabilityUnsupported   HarnessCapabilityStatus = "unsupported"
    HarnessCapabilityNotApplicable HarnessCapabilityStatus = "not_applicable"
)

type HarnessCapability struct {
    Status HarnessCapabilityStatus
    Detail string // human-readable reason tied to the current implementation
}

type HarnessCapabilityMatrix struct {
    ExecutePrompt     HarnessCapability
    ModelDiscovery    HarnessCapability
    ModelPinning      HarnessCapability
    WorkdirContext    HarnessCapability
    ReasoningLevels   HarnessCapability
    PermissionModes   HarnessCapability
    ProgressEvents    HarnessCapability
    UsageCapture      HarnessCapability
    FinalText         HarnessCapability
    ToolEvents        HarnessCapability
    QuotaStatus       HarnessCapability
    RecordReplay      HarnessCapability
}

type ProviderInfo struct {
    Name          string
    Type          string  // "openai" | "openrouter" | "lmstudio" | "omlx" | "ollama" | "anthropic" | "virtual"
    BaseURL       string
    Status        string  // "connected" | "unreachable" | "error: <msg>"
    ModelCount    int
    Capabilities  []string  // {"tool_use","vision","json_mode","streaming"}
    IsDefault     bool      // matches the configured default_provider
    DefaultModel  string    // the per-provider configured default model, if any
    CooldownState *CooldownState  // nil if not in cooldown
    Auth          AccountStatus
    EndpointStatus []EndpointStatus
    Quota         *QuotaState
    UsageWindows  []UsageWindow
    LastError     *StatusError
}

type ModelInfo struct {
    ID            string
    Provider      string
    ProviderType  string  // concrete provider type, e.g. "openrouter", "lmstudio", or "omlx"
    Harness       string  // for subprocess-only models, the owning harness
    EndpointName  string  // configured endpoint name, "default", or host:port fallback
    EndpointBaseURL string // endpoint base URL used for discovery; empty when not applicable
    ContextLength int     // resolved (provider API > catalog > default)
    Capabilities  []string
    Cost          CostInfo
    PerfSignal    PerfSignal
    Available     bool
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

type ProfileInfo struct {
    Name            string  // profile or alias visible to callers
    Target          string  // canonical catalog target/profile
    AliasOf         string  // non-empty when Name is an alias
    Deprecated      bool
    Replacement     string
    CatalogVersion  string
    ManifestSource  string
    ManifestVersion int
}

type ResolvedProfile struct {
    Name            string
    Target          string
    Deprecated      bool
    Replacement     string
    CatalogVersion  string
    ManifestSource  string
    ManifestVersion int
    Surfaces        []ProfileSurface
}

type ProfileSurface struct {
    Name                    string  // public service surface, e.g. "native-openai"
    Harness                 string
    ProviderSystem          string  // provider family, not a configured provider name
    Model                   string
    Candidates              []string
    PlacementOrder          []string
    CostCeilingInputPerMTok *float64
    ReasoningDefault        Reasoning
    FailurePolicy           string
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
    LastAttempt time.Time
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

type UsageReportOptions struct {
    Since string     // "today", "7d", "30d", "YYYY-MM-DD", or "YYYY-MM-DD..YYYY-MM-DD"
    Now   time.Time  // zero = time.Now().UTC()
}

type UsageReport struct {
    Window *UsageReportWindow `json:"window,omitempty"`
    Rows   []UsageReportRow   `json:"rows"`
    Totals UsageReportRow     `json:"totals"`
}

type UsageReportWindow struct {
    Start time.Time `json:"start"`
    End   time.Time `json:"end"`
}

type UsageReportRow struct {
    Provider            string   `json:"provider"`
    Model               string   `json:"model"`
    Sessions            int      `json:"sessions"`
    SuccessSessions     int      `json:"success_sessions"`
    FailedSessions      int      `json:"failed_sessions"`
    InputTokens         int      `json:"input_tokens"`
    OutputTokens        int      `json:"output_tokens"`
    TotalTokens         int      `json:"total_tokens"`
    DurationMs          int64    `json:"duration_ms"`
    KnownCostUSD        *float64 `json:"known_cost_usd"`
    UnknownCostSessions int      `json:"unknown_cost_sessions"`
    CacheReadTokens     int      `json:"cache_read_tokens"`
    CacheWriteTokens    int      `json:"cache_write_tokens"`
}

// Derived helpers on UsageReportRow:
//   SuccessRate() float64           — successful sessions / total sessions
//   CostPerSuccess() *float64       — known cost / successful sessions, nil when unknown
//   InputTokensPerSecond() float64
//   OutputTokensPerSecond() float64
//   CacheHitRate() float64          — cache_read / (input + cache_read + cache_write)

type SessionLogEntry struct {
    SessionID string    `json:"session_id"`
    ModTime   time.Time `json:"mod_time"`
    Size      int64     `json:"size"`
}

type Event struct {
    Type     string          // see event types below
    Sequence int64
    Time     time.Time
    Metadata map[string]string  // echoed from ExecuteRequest.Metadata
    Data     json.RawMessage    // shape depends on Type; see schemas below
}

const (
    ServiceEventTypeTextDelta       = "text_delta"
    ServiceEventTypeToolCall        = "tool_call"
    ServiceEventTypeToolResult      = "tool_result"
    ServiceEventTypeCompaction      = "compaction"
    ServiceEventTypeRoutingDecision = "routing_decision"
    ServiceEventTypeStall           = "stall"
    ServiceEventTypeFinal           = "final"
)

type ServiceTextDeltaData struct { Text string }
type ServiceToolCallData struct { ID, Name string; Input json.RawMessage }
type ServiceToolResultData struct { ID, Output, Error string; DurationMS int64 }
type ServiceCompactionData struct { MessagesBefore, MessagesAfter, TokensFreed int }
type ServiceRoutingDecisionData struct {
    Harness, Provider, Model, Reason string
    FallbackChain []string
    SessionID string
}
type ServiceStallData struct { Reason string; Count int64 }
type ServiceFinalData struct {
    Status, Error, FinalText, SessionLogPath string
    ExitCode int
    DurationMS int64
    Usage *ServiceFinalUsage
    Warnings []ServiceFinalWarning
    CostUSD float64
    RoutingActual *ServiceRoutingActual
}
type ServiceFinalUsage struct {
    InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens *int
    CacheTokens, ReasoningTokens, TotalTokens *int
    Source string
    Fresh *bool
    CapturedAt string
    Sources []ServiceUsageSourceEvidence
}
type ServiceFinalWarning struct {
    Code, Message string
    Sources []ServiceUsageSourceEvidence
}
type ServiceUsageSourceEvidence struct {
    Source string
    Fresh *bool
    CapturedAt string
    Usage *ServiceUsageTokenCounts
    Warning string
}
type ServiceUsageTokenCounts struct {
    InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens *int
    CacheTokens, ReasoningTokens, TotalTokens *int
}
type ServiceRoutingActual struct {
    Harness, Provider, Model string
    FallbackChainFired []string
}

type ServiceDecodedEvent struct {
    Type string
    Sequence int64
    Time time.Time
    Metadata map[string]string
    TextDelta *ServiceTextDeltaData
    ToolCall *ServiceToolCallData
    ToolResult *ServiceToolResultData
    Compaction *ServiceCompactionData
    RoutingDecision *ServiceRoutingDecisionData
    Stall *ServiceStallData
    Final *ServiceFinalData
}

func DecodeServiceEvent(ev Event) (ServiceDecodedEvent, error)

type DrainExecuteResult struct {
    Events []ServiceDecodedEvent
    TextDeltas []ServiceTextDeltaData
    ToolCalls []ServiceToolCallData
    ToolResults []ServiceToolResultData
    Compactions []ServiceCompactionData
    Stalls []ServiceStallData
    RoutingDecision *ServiceRoutingDecisionData
    Final *ServiceFinalData
    FinalStatus string
    FinalText string
    Usage *ServiceFinalUsage
    CostUSD float64
    SessionLogPath string
    RoutingActual *ServiceRoutingActual
    TerminalError string
}

func DrainExecute(ctx context.Context, events <-chan Event) (*DrainExecuteResult, error)
```

## Status Signal Semantics

`ListHarnesses`, `ListProviders`, and `RouteStatus` are the status API for
doctor-style consumers. Consumers must not read provider-native files, auth
stores, quota caches, or config files directly to build routing diagnostics.

Every normalized status datum carries:

- `Source`: the endpoint, cache, config, or probe path that produced it.
- `CapturedAt`: when the service captured or read the datum.
- `Fresh`: whether the value is inside the service's freshness window.
- `LastError`: normalized `unavailable`, `unauthenticated`, or `error`
  information when the datum could not be captured successfully.

Provider endpoint probes report `EndpointStatus` with reachability,
`ModelCount`, and `LastSuccessAt` when connected. Provider authentication is
reported through `ProviderInfo.Auth`; missing API keys or 401/403-style probe
failures are `Unauthenticated=true` and do not require consumers to know the
provider's native auth file format.

`ListModels` is the public model-listing surface for configured native
providers. For `openrouter`, `lmstudio`, and `omlx`, it must query each
configured endpoint's OpenAI-compatible models endpoint (`<base_url>/models`,
where `base_url` normally ends in `/v1`) and return one `ModelInfo` per
discovered `(provider, endpoint, model)` tuple. Results carry the configured
provider name in `Provider`, the concrete backend type in `ProviderType`, and
the endpoint identity in `EndpointName` / `EndpointBaseURL`; consumers must not
infer these from URLs or internal config. If an endpoint is unreachable or
returns a non-OK response, that endpoint contributes no models and the method
continues listing other endpoints/providers. Missing credentials for cloud
providers surface through the same empty-result behavior here and through
`ListProviders`/`HealthCheck` status for diagnostics.

Claude Code and Codex subscription quotas are read from durable service-owned
caches by `ListHarnesses`; `HealthCheck` may refresh stale caches by invoking
the authenticated direct PTY probe. Existing tmux-backed quota probes are legacy
diagnostics and must not be treated as final capability evidence. Live record
mode must fail fast with a clear unavailable or unauthenticated status when the
target binary, credentials, or direct PTY transport dependency is missing.
Replay mode reads committed/generated cassette data or quota cache fixtures and
must not require credentials.

`UsageWindows` are the normalized historical-usage projection. An empty slice
means no service-owned usage source is available for that harness/provider yet;
consumers should display that as unavailable rather than reading native logs
directly.

## CLI Projection Boundary

The standalone `cmd/ddx-agent` binary is a first-party consumer of this service
contract. Its job is to translate user input into public service requests and
render public service results. The CLI boundary is strict:

- execution goes through `DdxAgent.Execute`;
- session replay/follow goes through `TailSessionLog`;
- output decoding uses `DecodeServiceEvent` or `DrainExecute`, not local copies
  of private payload structs;
- historical session-log projections (`ddx-agent log`, `replay`, `usage`) go
  through `ListSessionLogs`, `WriteSessionLog`, `ReplaySession`, and
  `UsageReport`; CLI subcommands do not parse session-log JSONL records
  directly;
- harness capabilities, profile projection, route feedback, quota/status, and
  test-only harness dispatch are consumed through public service methods.

The CLI must not:

- construct native providers or provider failover wrappers;
- call `internal/core` loop entry points directly;
- synthesize service session lifecycle records into the internal session-log
  schema;
- rebuild `RouteDecision` candidate lists from config as a substitute for
  calling `ResolveRoute` or passing routing intent to `Execute`.

In practice this means `cmd/ddx-agent` must not depend on
`internal/core`, `internal/provider/*`, `internal/tool`, `internal/session`,
`internal/compaction`, `internal/harnesses`, or `internal/routing`.

If a CLI-visible behavior cannot be expressed through `DdxAgent` methods or the
public request/event/result types, the contract must grow first. Internal
package reach-through from `cmd/ddx-agent` is architecture debt and must not be
normalized as a permanent compatibility layer.

## Catalog Profile Projection

Catalog profiles are service data, not consumer configuration. Consumers that
need to present, validate, or route by profile call:

- `ListProfiles` for selectable profile names, alias relationships, and catalog
  provenance.
- `ResolveProfile` for the public service projection of one profile or alias:
  placement order, supported surfaces, candidate model IDs, cost ceiling,
  reasoning default, and failure policy.
- `ProfileAliases` for lightweight alias migration and validation maps.

Public `ProfileSurface.Name` values are stable service names:
`native-openai`, `native-anthropic`, `codex`, and `claude`. Consumers must not
depend on internal catalog surface strings such as `agent.openai`,
`agent.anthropic`, or `claude-code`; those remain model-catalog implementation
details.

Migration rule: any consumer currently reading `~/.config/agent/models.yaml`,
model-catalog manifests, or hard-coded surface strings to discover `cheap`,
`standard`, `smart`, aliases, or placement policy must switch to the service
methods above. Direct YAML reads are allowed only inside the agent service and
model-catalog implementation.

## Harness Capability Matrix

`ListHarnesses` exposes `HarnessInfo.CapabilityMatrix` so consumers can decide
which harnesses are eligible without reading internal registry structs. Status
semantics:

- `required`: the service contract relies on this capability for that harness.
- `optional`: the harness supports the capability, but callers must tolerate its
  absence on other harnesses.
- `unsupported`: the capability is meaningful for the harness class but is not
  currently available.
- `not_applicable`: the capability does not apply to that harness class.

The broad matrix below is a compatibility view across subprocess harnesses,
test-only harnesses, and current provider-backend rows. It is not the
authoritative health signal for the primary harnesses. Primary harness health is
specified separately in
`docs/helix/02-design/primary-harness-capability-baseline.md` and covers only
`agent`, `codex`, and `claude`.

Primary-harness baseline capabilities are strict: `Run`, `FinalText`,
`ProgressEvents`, `Cancel`, `WorkdirContext`, `PermissionModes`, `ListModels`,
`SetModel`, `ListReasoning`, `SetReasoning`, `TokenUsage`, `QuotaStatus` for
primary subscription harnesses, `ErrorStatus`, and `RequestMetadata`. These capabilities
must not be reported as `optional` in the primary baseline. In particular,
`ListModels` is required for `codex` and `claude`; if model choices are only
available through their interactive TUI surfaces and no headless collector is
implemented yet, the primary baseline reports a visible `gap` or `blocked`
state rather than treating model listing as unsupported or optional.

Current builtin matrix:

| Harness | ExecutePrompt | ModelDiscovery | ModelPinning | WorkdirContext | ReasoningLevels | PermissionModes | ProgressEvents | UsageCapture | FinalText | ToolEvents | QuotaStatus | RecordReplay |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| codex | required | unsupported | optional | optional | optional | optional | required | optional | optional | optional | optional | unsupported |
| claude | required | unsupported | optional | optional | optional | optional | required | optional | optional | optional | optional | unsupported |
| gemini | required | optional | optional | optional | unsupported | optional | required | optional | optional | unsupported | unsupported | optional |
| opencode | required | unsupported | optional | optional | optional | optional | required | optional | optional | unsupported | unsupported | unsupported |
| agent | required | optional | optional | optional | optional | optional | required | optional | optional | optional | not_applicable | unsupported |
| pi | required | unsupported | optional | optional | optional | unsupported | required | optional | optional | unsupported | unsupported | unsupported |
| virtual | required | not_applicable | not_applicable | not_applicable | not_applicable | not_applicable | required | optional | optional | not_applicable | not_applicable | required |
| script | required | not_applicable | not_applicable | not_applicable | not_applicable | not_applicable | required | optional | optional | not_applicable | not_applicable | required |
| openrouter | required | required | unsupported | unsupported | unsupported | unsupported | required | optional | optional | unsupported | unsupported | unsupported |
| lmstudio | required | required | unsupported | unsupported | unsupported | unsupported | required | optional | optional | unsupported | not_applicable | unsupported |
| omlx | required | required | unsupported | unsupported | unsupported | unsupported | required | optional | optional | unsupported | not_applicable | unsupported |

Notes:

- `ExecutePrompt=required` means `Service.Execute` has a wired dispatch path
  today. Registered subprocess runners that are not wired through
  `Service.Execute` remain `unsupported` in that row even when lower-level
  runner code exists.
- `FinalText=optional` means final events populate `final_text` when the
  harness or native provider produced user-facing response text. During the
  migration window, `text_delta` remains available for consumers that still
  stream output incrementally, but final verdict parsers should prefer
  `final_text` and avoid parsing raw harness stream frames.
- `RecordReplay=required` only for test-only harnesses (`virtual`, `script`).
  Production harnesses do not currently expose deterministic record/replay
  through this service contract.

## Test-Only Execute Harnesses

`virtual` and `script` are explicit test-only harnesses. The router and profile
routing never choose them implicitly; callers must set `ExecuteRequest.Harness`
explicitly to opt in.

`Harness="virtual"` accepts either:

- `Metadata["virtual.response"]`: an inline deterministic final response.
  Optional keys: `virtual.prompt_match`, `virtual.input_tokens`,
  `virtual.output_tokens`, `virtual.total_tokens`, `virtual.delay_ms`,
  `virtual.model`.
- `Metadata["virtual.dict_dir"]`: a virtual-provider dictionary directory keyed
  by normalized prompt hash.

`Harness="script"` accepts a pinned script definition through metadata:
`script.stdout` is required; optional keys are `script.stderr`,
`script.exit_code`, and `script.delay_ms`. This is intentionally data-driven and
does not require fake `claude`, `codex`, `opencode`, `gemini`, or `pi` binaries.
Both harnesses emit the normal `routing_decision` → progress/text → `final`
sequence and can be consumed through `DrainExecute`.

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
  "final_text": "user-facing final response text, stripped of harness stream envelopes",
  "duration_ms": 12345,
  "usage": {
    "input_tokens": 7996,
    "output_tokens": 267,
    "cache_read_tokens": 1200,
    "reasoning_tokens": 41,
    "total_tokens": 8263,
    "source": "native_stream",
    "fresh": true,
    "sources": [
      {"source": "native_stream", "fresh": true, "usage": {"input_tokens": 7996, "output_tokens": 267, "total_tokens": 8263}}
    ]
  },
  "warnings": [
    {
      "code": "usage_source_disagreement",
      "message": "token usage sources disagree; selected source by documented precedence",
      "sources": [
        {"source": "native_stream", "usage": {"input_tokens": 7996, "output_tokens": 267, "total_tokens": 8263}},
        {"source": "transcript", "usage": {"input_tokens": 7990, "output_tokens": 267, "total_tokens": 8257}}
      ]
    }
  ],
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

### Final Usage Source Policy

Final-event `usage` is optional. A missing `usage` object means per-run token
usage was unavailable, not zero. When a harness explicitly reports zero tokens,
the relevant fields are present with value `0`. Token dimensions that the
harness did not expose are omitted rather than serialized as fabricated zeros.

The normalized token vocabulary is `input_tokens`, `output_tokens`,
`cache_read_tokens`, `cache_write_tokens`, `cache_tokens`,
`reasoning_tokens`, and `total_tokens`. Harness-specific terms are normalized
into this vocabulary when exposed by Claude, Codex, or native provider streams.

When more than one source is available, precedence is:

1. `native_stream`
2. `transcript`
3. `status_output`
4. `fallback`

The selected source is copied to `usage.source`; every valid source considered
is listed in `usage.sources` with its `fresh`/`captured_at` metadata when
available. If multiple sources report different overlapping token fields, the
service still selects by precedence but records a final warning with
`code=usage_source_disagreement` and the source values. Malformed or changed
usage shapes are recorded as `code=usage_malformed` warnings and do not cause
missing fields to be filled with zero.

## Typed Event Decoding

Consumers should not redefine local copies of final/tool/routing payload
structs. `DecodeServiceEvent` returns a typed view for one event, and
`DrainExecute` consumes an `Execute` channel into a `DrainExecuteResult` with
the terminal fields consumers usually need: final status, normalized final text,
usage, cost, routing actual, tool calls/results, session log path, and terminal
error text.

Before:

```go
type serviceFinalData struct {
    Status string `json:"status"`
    FinalText string `json:"final_text"`
}

for ev := range events {
    if ev.Type != "final" {
        continue
    }
    var final serviceFinalData
    _ = json.Unmarshal(ev.Data, &final)
}
```

After:

```go
result, err := agent.DrainExecute(ctx, events)
if err != nil {
    return err
}
verdictText := result.FinalText
status := result.FinalStatus
actualModel := result.RoutingActual.Model
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

## Route Attempt Feedback

`RecordRouteAttempt` is deterministic, process-local routing feedback. It does
not persist across process restarts. The active TTL is `ServiceConfig`
`HealthCooldown`; when that is unset the default is 30 seconds.

Candidate keying uses the tuple `(Harness, Provider, Model, Endpoint)`.
Consumers should provide every field they know. A non-success `Status` records
an active failure and future `ResolveRoute` calls demote matching candidates
inside the same process until the TTL expires. `Status="success"` clears
matching active failures so a recovered candidate is eligible without waiting
for TTL expiry. `RouteStatus` reports active route-attempt cooldowns on matching
candidates with `Reason`, `LastError`, `LastAttempt`, and `Until` timestamps.

## Harness Integration Testing

Real subprocess harness support uses versioned PTY cassettes as golden-master
evidence. The transport decision is [ADR-002: PTY Cassette Transport for
Harness Golden Masters](/Users/erik/Projects/agent/docs/helix/02-design/adr/ADR-002-pty-cassette-transport.md).
The runnable replay/record workflow is documented in
[Harness Golden-Master Integration](/Users/erik/Projects/agent/docs/helix/02-design/harness-golden-integration.md).

ADR-002 selects direct PTY ownership inside DDX Agent as the canonical service
and cassette transport for live execution, record mode, replay mode,
cancellation, quota probes, model-list probes, and inspection. tmux is not part
of the core harness/cassette design, and tmux-only evidence must not promote a
capability to final `supported` status. Replay-mode tests can prove parser,
event, cleanup, timing, and transport behavior, but a harness capability is not
promoted to or retained as `supported` without fresh record-mode evidence from
the real authenticated harness when that capability depends on an external
binary or subscription. PTY cassette record/replay is part of the `internal/pty`
library boundary, with version-1 cassette timestamps quantized to 100ms by
default and replay supporting realtime, scaled, and collapsed timing modes.

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

- **Service-owned native routing and provider construction.** For the embedded
  `agent` harness, `Execute` resolves configured provider candidates,
  constructs the concrete provider adapter, and performs failover internally.
  Callers express intent with `Harness`, `Provider`, `Model`, `ModelRef`, or
  `Profile`, plus the optional `EstimatedPromptTokens`/`RequiresTools`
  auto-selection inputs; they do not pass provider instances, private
  candidate tables, or pre-resolved `RouteDecision` values. `ResolveRoute`
  results are informational only — `Execute` always re-resolves on its own
  inputs (idempotent for the same caller intent, modulo health changes).

- **Route-reason attribution.** The start-event `routing_decision` and
  final-event `routing_actual` together capture why each candidate was
  tried/picked.

- **Stall detection.** Per `StallPolicy`. Default policy (when caller passes
  `nil`) uses conservative limits matching today's circuit-breaker thresholds.

- **Compaction-stuck breaking.** Implicit in the `StallPolicy` default;
  callers don't configure separately.

- **OS-level subprocess cleanup.** On `ctx.Done()`, agent reaps PTY and
  orphan processes for subprocess harnesses. Tested and guaranteed.

- **Session-log persistence ownership.** When session logging is enabled,
  service-owned execution writes the lifecycle and terminal records for that
  session. Consumers may choose where logs are stored via `SessionLogDir`, but
  they do not recreate internal session start/end records from the event
  stream.

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
