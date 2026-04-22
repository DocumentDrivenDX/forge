package agent

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	claudeharness "github.com/DocumentDrivenDX/agent/internal/harnesses/claude"
	codexharness "github.com/DocumentDrivenDX/agent/internal/harnesses/codex"
	geminiharness "github.com/DocumentDrivenDX/agent/internal/harnesses/gemini"
	sessionusage "github.com/DocumentDrivenDX/agent/internal/session"
)

// DdxAgent is the entire public Go surface of the ddx-agent module.
// See CONTRACT-003 for the full specification.
type DdxAgent interface {
	Execute(ctx context.Context, req ServiceExecuteRequest) (<-chan ServiceEvent, error)
	TailSessionLog(ctx context.Context, sessionID string) (<-chan ServiceEvent, error)
	ListHarnesses(ctx context.Context) ([]HarnessInfo, error)
	ListProviders(ctx context.Context) ([]ProviderInfo, error)
	ListModels(ctx context.Context, filter ModelFilter) ([]ModelInfo, error)
	ListProfiles(ctx context.Context) ([]ProfileInfo, error)
	ResolveProfile(ctx context.Context, name string) (*ResolvedProfile, error)
	ProfileAliases(ctx context.Context) (map[string]string, error)
	HealthCheck(ctx context.Context, target HealthTarget) error
	ResolveRoute(ctx context.Context, req RouteRequest) (*RouteDecision, error)
	RecordRouteAttempt(ctx context.Context, attempt RouteAttempt) error
	RouteStatus(ctx context.Context) (*RouteStatusReport, error)
}

// ServiceConfig provides provider and routing data to the service without
// creating an import cycle from the root package into agent/config.
// Callers wrap their loaded *config.Config in a type that satisfies this interface.
type ServiceConfig interface {
	// ProviderNames returns provider names in stable order (default first).
	ProviderNames() []string
	// DefaultProviderName returns the name of the configured default provider.
	DefaultProviderName() string
	// Provider returns the raw config values for a named provider.
	Provider(name string) (ServiceProviderEntry, bool)
	// ModelRouteNames returns configured model-route names.
	ModelRouteNames() []string
	// ModelRouteCandidates returns the provider names referenced by a route.
	ModelRouteCandidates(routeName string) []string
	// ModelRouteConfig returns the full route config for a named route.
	// Returns zero value when the route is not found.
	ModelRouteConfig(routeName string) ServiceModelRouteConfig
	// HealthCooldown returns the configured cooldown duration (0 = use default 30s).
	HealthCooldown() time.Duration
	// WorkDir is the base directory for file-backed health state.
	WorkDir() string
}

// ServiceModelRouteConfig carries the routing strategy and candidates for one route.
type ServiceModelRouteConfig struct {
	Strategy   string                       // "priority-round-robin" | "ordered-failover" | "smart" | ""
	Candidates []ServiceRouteCandidateEntry // ordered candidate list
}

// ServiceRouteCandidateEntry is one candidate inside a model route.
type ServiceRouteCandidateEntry struct {
	Provider string
	Model    string // may be empty (provider default)
	Priority int
}

// ServiceProviderEntry carries the minimal provider data the service needs.
type ServiceProviderEntry struct {
	Type      string // "openai" | "openrouter" | "lmstudio" | "omlx" | "ollama" | "anthropic"
	BaseURL   string
	Endpoints []ServiceProviderEndpoint
	APIKey    string
	Model     string // configured default model (may be empty)
}

// ServiceProviderEndpoint is one configured provider serving endpoint.
type ServiceProviderEndpoint struct {
	Name    string
	BaseURL string
}

// ServiceOptions configures a DdxAgent instance.
//
// seamOptions is embedded so production builds (no testseam tag) get an
// empty struct — making it a compile-time error to set seam fields without
// the build tag. Test builds inherit the four CONTRACT-003 seams
// (FakeProvider, PromptAssertionHook, CompactionAssertionHook,
// ToolWiringHook) automatically.
type ServiceOptions struct {
	seamOptions

	ConfigPath string    // optional override; default $XDG_CONFIG_HOME/ddx-agent/config.yaml
	Logger     io.Writer // optional; agent writes structured session logs internally regardless

	// ServiceConfig, when non-nil, supplies provider and routing data for
	// ListProviders and HealthCheck. Pass a value wrapping the loaded config.
	// When nil, those methods return an error.
	ServiceConfig ServiceConfig

	// QuotaRefreshDebounce is the minimum interval between live quota probes for
	// a primary subscription harness. Zero uses the service default.
	QuotaRefreshDebounce time.Duration
	// QuotaRefreshStartupWait bounds startup waiting when the durable quota
	// cache is missing, stale, or incomplete. Zero uses the service default.
	QuotaRefreshStartupWait time.Duration
	// QuotaRefreshInterval enables periodic refresh for long-running server
	// processes. Zero disables the timer; cache refresh still happens on startup
	// and service activity.
	QuotaRefreshInterval time.Duration
	// QuotaRefreshContext optionally cancels the periodic server refresh worker.
	// When nil, the worker uses context.Background().
	QuotaRefreshContext context.Context

	// LocalCostUSDPer1kTokens is the operator-supplied electricity/operations
	// estimate for local endpoint providers under the embedded agent harness.
	// Zero means local endpoint cost is unknown.
	LocalCostUSDPer1kTokens float64
	// SubscriptionCostCurve optionally overrides the default subscription
	// effective-cost curve used by routing.
	SubscriptionCostCurve *SubscriptionCostCurve
}

// SubscriptionCostCurve tunes effective subscription cost by quota utilization.
// Thresholds are percentages used, and multipliers are applied to the
// equivalent pay-per-token catalog rate.
type SubscriptionCostCurve struct {
	FreeUntilPercent   int
	LowUntilPercent    int
	MediumUntilPercent int
	LowMultiplier      float64
	MediumMultiplier   float64
	HighMultiplier     float64
}

// QuotaState is a live quota snapshot for a harness. Nil means not applicable.
type QuotaState struct {
	Windows    []harnesses.QuotaWindow `json:"windows"`
	CapturedAt time.Time               `json:"captured_at"`
	Fresh      bool                    `json:"fresh"`
	Source     string                  `json:"source,omitempty"`
	Status     string                  `json:"status,omitempty"` // ok|stale|unavailable|unauthenticated|unknown
	LastError  *StatusError            `json:"last_error,omitempty"`
}

// StatusError describes the most recent normalized status error for a harness,
// provider, or endpoint.
type StatusError struct {
	Type      string    // unavailable|unauthenticated|error
	Detail    string    // human-readable detail, safe for diagnostics
	Source    string    // config path, endpoint, cache path, or probe name
	Timestamp time.Time // zero when the source did not include a timestamp
}

// AccountStatus describes authentication/account state without exposing
// provider-specific native files to consumers.
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

// UsageWindow describes normalized usage attribution over a time window.
// Empty token/cost totals mean the service has no historical usage source yet.
type UsageWindow struct {
	Name                string
	Source              string
	CapturedAt          time.Time
	Fresh               bool
	InputTokens         int
	OutputTokens        int
	TotalTokens         int
	CacheReadTokens     int
	CacheWriteTokens    int
	ReasoningTokens     int
	CostUSD             float64
	KnownCostUSD        *float64
	UnknownCostSessions int
}

// EndpointStatus describes one configured provider endpoint probe.
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

// HarnessInfo describes a registered harness as defined in CONTRACT-003.
type HarnessInfo struct {
	Name                 string
	Type                 string // "native" | "subprocess"
	Available            bool
	Path                 string
	Error                string
	IsLocal              bool
	IsSubscription       bool
	AutoRoutingEligible  bool
	TestOnly             bool
	ExactPinSupport      bool
	DefaultModel         string   // built-in default model when no override is supplied
	SupportedPermissions []string // subset of {"safe","supervised","unrestricted"}
	SupportedReasoning   []string // values such as {"low","medium","high","xhigh","max"}
	CostClass            string   // "local" | "cheap" | "medium" | "expensive"
	Quota                *QuotaState
	Account              *AccountStatus
	UsageWindows         []UsageWindow
	LastError            *StatusError
	CapabilityMatrix     HarnessCapabilityMatrix
}

// CooldownState describes an active routing cooldown for a provider.
type CooldownState struct {
	Reason      string    // "consecutive_failures" | "route_attempt_failure" | "manual" | etc.
	Until       time.Time // when the cooldown expires
	FailCount   int       // number of consecutive failures that triggered the cooldown
	LastError   string    // last recorded error message, if available
	LastAttempt time.Time // when the feedback was recorded
}

// ProviderInfo describes a provider with live status per CONTRACT-003.
type ProviderInfo struct {
	Name           string
	Type           string // "openai" | "openrouter" | "lmstudio" | "omlx" | "ollama" | "anthropic" | "virtual"
	BaseURL        string
	Endpoints      []ServiceProviderEndpoint
	Status         string // "connected" | "unreachable" | "error: <msg>"
	ModelCount     int
	Capabilities   []string       // e.g. {"tool_use","streaming","json_mode"}
	IsDefault      bool           // matches the configured default_provider
	DefaultModel   string         // per-provider configured default model, if any
	CooldownState  *CooldownState // nil if not in cooldown
	Auth           AccountStatus
	EndpointStatus []EndpointStatus
	Quota          *QuotaState
	UsageWindows   []UsageWindow
	LastError      *StatusError
}

// CostInfo holds per-token cost metadata for a model.
type CostInfo struct {
	InputPerMTok  float64 // USD per 1M input tokens; 0 = unknown/free
	OutputPerMTok float64 // USD per 1M output tokens; 0 = unknown/free
}

// PerfSignal holds observed performance data for a model.
type PerfSignal struct {
	SpeedTokensPerSec float64 // 0 = unknown
	SWEBenchVerified  float64 // 0 = unknown
}

// ModelInfo describes a model with full metadata per CONTRACT-003.
type ModelInfo struct {
	ID              string
	Provider        string
	ProviderType    string
	Harness         string
	EndpointName    string
	EndpointBaseURL string
	ContextLength   int
	Capabilities    []string
	Cost            CostInfo
	PerfSignal      PerfSignal
	Available       bool
	IsConfigured    bool   // matches an explicit model_routes entry
	IsDefault       bool   // matches the configured default model
	CatalogRef      string // canonical catalog reference if recognized
	RankPosition    int    // ordinal in latest discovery rank; -1 if unranked
}

// ModelFilter filters ListModels results.
type ModelFilter struct {
	Harness  string
	Provider string
}

type ProfileInfo struct {
	Name               string
	Target             string
	AliasOf            string
	ProviderPreference string
	Deprecated         bool
	Replacement        string
	CatalogVersion     string
	ManifestSource     string
	ManifestVersion    int
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
	Name                    string
	Harness                 string
	ProviderSystem          string
	Model                   string
	Candidates              []string
	PlacementOrder          []string
	CostCeilingInputPerMTok *float64
	ReasoningDefault        Reasoning
	FailurePolicy           string
}

// HealthTarget identifies what to health-check.
type HealthTarget struct {
	Type string // "harness" | "provider"
	Name string
}

// RouteRequest specifies a routing query.
type RouteRequest struct {
	Profile     string // optional named policy bundle: cheap|standard|smart|custom
	Model       string
	Provider    string
	Harness     string
	ModelRef    string
	Reasoning   Reasoning
	Permissions string
}

// RouteDecision is the result of ResolveRoute.
type RouteDecision struct {
	// Harness is the selected harness name.
	Harness string
	// Provider is the selected provider for native agent routes.
	Provider string
	// Endpoint is the selected named endpoint when the provider exposes more
	// than one endpoint.
	Endpoint string
	// Model is the selected concrete model.
	Model string
	// Reason summarizes why the selected candidate won.
	Reason string
	// Candidates is the full ranked decision trace, including rejected
	// candidates and their rejection reasons.
	Candidates []RouteCandidate
}

// RouteCandidate is one routing candidate evaluated by ResolveRoute.
type RouteCandidate struct {
	// Harness is the candidate harness name.
	Harness string
	// Provider is the candidate provider name for native agent routes.
	Provider string
	// Endpoint is the provider endpoint name when applicable.
	Endpoint string
	// Model is the candidate concrete model.
	Model string
	// Score is the routing score assigned before final ordering.
	Score float64
	// CostUSDPer1kTokens is the estimated blended USD cost per 1,000 tokens.
	CostUSDPer1kTokens float64
	// CostSource indicates whether cost came from catalog, subscription,
	// user-config, or is unknown.
	CostSource string
	// Eligible reports whether the candidate passed all routing gates.
	Eligible bool
	// Reason is the scoring reason for eligible candidates or the rejection
	// reason for ineligible candidates.
	Reason string
}

// RouteAttempt is caller feedback about one attempted route candidate.
// Status="success" clears matching active failures; any other non-empty status
// records a same-process failure until the service health cooldown expires.
type RouteAttempt struct {
	Harness   string
	Provider  string
	Model     string
	Endpoint  string
	Status    string
	Reason    string
	Error     string
	Duration  time.Duration
	Timestamp time.Time // zero = time.Now()
}

// RouteStatusReport is returned by RouteStatus.
type RouteStatusReport struct {
	Routes          []RouteStatusEntry
	GeneratedAt     time.Time
	GlobalCooldowns []CooldownState
}

// RouteStatusEntry describes one configured model route.
type RouteStatusEntry struct {
	Model          string // route key
	Strategy       string // "priority-round-robin" | "first-available" | etc.
	Candidates     []RouteCandidateStatus
	LastDecision   *RouteDecision // most recent ResolveRoute result for this key (cached)
	LastDecisionAt time.Time
}

// RouteCandidateStatus describes a single candidate within a route.
type RouteCandidateStatus struct {
	Provider          string
	Model             string
	Priority          int
	Healthy           bool
	Cooldown          *CooldownState
	RecentLatencyMS   float64 // observation-derived; 0 when unavailable
	RecentSuccessRate float64 // 0-1; 0 when unavailable
}

// ServiceEvent is a contract-level event (mirrors harnesses.Event).
type ServiceEvent = harnesses.Event

// ServiceExecuteRequest is the public ExecuteRequest type per CONTRACT-003.
// See docs/helix/02-design/contracts/CONTRACT-003-ddx-agent-service.md
// (§"Public types") for the canonical shape; this struct is its in-process
// twin under the agent module.
type ServiceExecuteRequest struct {
	Prompt       string
	SystemPrompt string
	Model        string
	Provider     string
	Harness      string
	ModelRef     string
	Profile      string
	WorkDir      string
	Temperature  float32
	Seed         int64
	Reasoning    Reasoning
	Permissions  string
	// Tools overrides the built-in native agent tool set when Harness is
	// "agent". Nil uses the native built-ins for ToolPreset and WorkDir.
	Tools []Tool
	// ToolPreset selects native built-in tool availability when Tools is nil.
	// Empty means the default preset; "benchmark" excludes the task tool.
	ToolPreset string

	// PreResolved bypasses ResolveRoute when the caller already has a
	// decision. When non-nil, agent uses these values verbatim and does
	// not re-route. Provider/Model/Harness fields above are ignored.
	PreResolved *RouteDecision

	// Three independent timeout knobs:
	//   Timeout         — wall-clock cap on the entire request.
	//   IdleTimeout     — streaming-quiet cap; per-stream gap.
	//   ProviderTimeout — per-HTTP-request cap to the provider.
	Timeout         time.Duration
	IdleTimeout     time.Duration
	ProviderTimeout time.Duration

	// Optional stall policy. When non-nil agent enforces and ends with
	// Status="stalled" if any limit hits. Default policy applies when nil.
	StallPolicy *StallPolicy

	// SessionLogDir overrides the default session-log directory for this
	// request (e.g. an execute-bead per-bundle evidence directory).
	SessionLogDir string

	// Metadata is bidirectional: echoed back in every Event AND stamped
	// onto every line of the session log so external consumers correlate.
	Metadata map[string]string
}

// StallPolicy bounds how long the agent will spin without making progress
// before terminating with Status="stalled". A nil policy resolves to the
// default in service_execute.go.
type StallPolicy struct {
	MaxReadOnlyToolIterations int // 0 = disabled
	MaxNoopCompactions        int // 0 = disabled
}

// service is the concrete DdxAgent implementation.
type service struct {
	opts     ServiceOptions
	registry *harnesses.Registry
	hub      *sessionHub

	// lastDecisionMu guards lastDecisionCache.
	lastDecisionMu sync.RWMutex
	// lastDecisionCache maps route key → (decision, time). Populated by
	// ResolveRoute; read by RouteStatus.
	lastDecisionCache map[string]lastDecisionEntry

	routeAttemptMu sync.RWMutex
	routeAttempts  map[routeAttemptKey]routeAttemptRecord
	routeMetrics   map[routeAttemptKey]routeMetricRecord

	// catalog is the service-scope model-catalog cache. Populated lazily
	// on first use by routing + chat paths; shared across requests so the
	// same endpoint isn't probed per-dispatch during a drain. See
	// service_catalog_cache.go.
	catalog *catalogCache
}

// lastDecisionEntry caches the most recent RouteDecision for a route key.
type lastDecisionEntry struct {
	decision *RouteDecision
	at       time.Time
}

type routeAttemptKey struct {
	Harness  string
	Provider string
	Model    string
	Endpoint string
}

type routeAttemptRecord struct {
	key        routeAttemptKey
	status     string
	reason     string
	err        string
	duration   time.Duration
	recordedAt time.Time
}

type routeMetricRecord struct {
	attempts      int
	successes     int
	totalDuration time.Duration
	recordedAt    time.Time
}

// loadServiceConfig, when non-nil, is called by New to load a ServiceConfig
// from a directory path when opts.ServiceConfig is nil. It is registered by
// the config package via init() to break the import cycle (config imports root).
var loadServiceConfig func(dir string) (ServiceConfig, error)

// RegisterConfigLoader is called by the config package's init() to install the
// config-loading function. Do not call this from application code.
func RegisterConfigLoader(fn func(dir string) (ServiceConfig, error)) {
	loadServiceConfig = fn
}

// New constructs a DdxAgent. When opts.ServiceConfig is nil, New attempts to
// load configuration automatically:
//  1. If opts.ConfigPath is non-empty, load from filepath.Dir(opts.ConfigPath).
//  2. Otherwise, load from the default global config location.
//
// Automatic loading requires the config package to be imported somewhere in the
// binary (it registers the loader via init). If the config package is not
// imported and ServiceConfig is nil, the service starts without provider config
// (ListProviders/HealthCheck will return errors until config is injected).
func New(opts ServiceOptions) (DdxAgent, error) {
	if opts.ServiceConfig == nil && loadServiceConfig != nil && shouldAutoLoadServiceConfig(opts) {
		dir := ""
		if opts.ConfigPath != "" {
			dir = filepath.Dir(opts.ConfigPath)
		}
		sc, err := loadServiceConfig(dir)
		if err != nil {
			return nil, fmt.Errorf("agent.New: load config: %w", err)
		}
		opts.ServiceConfig = sc
	}
	svc := &service{
		opts:         opts,
		registry:     harnesses.NewRegistry(),
		hub:          newSessionHub(),
		catalog:      newCatalogCache(catalogCacheOptions{}),
		routeMetrics: make(map[routeAttemptKey]routeMetricRecord),
	}
	svc.ensurePrimaryQuotaRefresh(context.Background(), quotaRefreshStartup)
	svc.startPrimaryQuotaRefreshWorker()
	return svc, nil
}

// harnessType returns "native" for HTTP/embedded harnesses, "subprocess" for CLI-invoked ones.
func harnessType(cfg harnesses.HarnessConfig) string {
	if cfg.IsHTTPProvider || cfg.IsLocal {
		return "native"
	}
	return "subprocess"
}

// supportedPermissions extracts the permission levels from PermissionArgs keys,
// returning them in canonical order.
func supportedPermissions(cfg harnesses.HarnessConfig) []string {
	if len(cfg.PermissionArgs) == 0 {
		return nil
	}
	order := []string{"safe", "supervised", "unrestricted"}
	var out []string
	for _, level := range order {
		if _, ok := cfg.PermissionArgs[level]; ok {
			out = append(out, level)
		}
	}
	return out
}

func supportedReasoning(cfg harnesses.HarnessConfig) []string {
	return append([]string(nil), cfg.ReasoningLevels...)
}

// claudeQuotaState reads the durable Claude quota cache and converts it to QuotaState.
func claudeQuotaState() *QuotaState {
	snap, ok := claudeharness.ReadClaudeQuota()
	if !ok || snap == nil {
		source, err := claudeharness.ClaudeQuotaCachePath()
		if err != nil {
			source = "claude quota cache"
		}
		return unavailableQuotaState(source, "claude quota cache unavailable")
	}
	now := time.Now()
	decision := claudeharness.DecideClaudeQuotaRouting(snap, now, 0)
	qs := &QuotaState{
		CapturedAt: snap.CapturedAt,
		Fresh:      decision.Fresh,
		Source:     snap.Source,
	}
	if snap.FiveHourLimit > 0 {
		var used float64
		if snap.FiveHourLimit > 0 {
			used = float64(snap.FiveHourLimit-snap.FiveHourRemaining) / float64(snap.FiveHourLimit) * 100
		}
		qs.Windows = append(qs.Windows, harnesses.QuotaWindow{
			Name:          "5h",
			WindowMinutes: 300,
			UsedPercent:   used,
			State:         harnesses.QuotaStateFromUsedPercent(int(used)),
		})
	}
	if snap.WeeklyLimit > 0 {
		var used float64
		if snap.WeeklyLimit > 0 {
			used = float64(snap.WeeklyLimit-snap.WeeklyRemaining) / float64(snap.WeeklyLimit) * 100
		}
		qs.Windows = append(qs.Windows, harnesses.QuotaWindow{
			Name:          "7d",
			WindowMinutes: 10080,
			UsedPercent:   used,
			State:         harnesses.QuotaStateFromUsedPercent(int(used)),
		})
	}
	qs.Status = quotaStatus(qs.Fresh, qs.Windows)
	return qs
}

func claudeAccountStatus() *AccountStatus {
	snap, ok := claudeharness.ReadClaudeQuota()
	if !ok || snap == nil {
		return nil
	}
	decision := claudeharness.DecideClaudeQuotaRouting(snap, time.Now(), 0)
	return accountStatusFromInfo(snap.Account, snap.Source, snap.CapturedAt, decision.Fresh)
}

// codexQuotaState reads the durable Codex quota cache and converts it to QuotaState.
func codexQuotaState() *QuotaState {
	snap, ok := codexharness.ReadCodexQuota()
	if !ok || snap == nil {
		source, err := codexharness.CodexQuotaCachePath()
		if err != nil {
			source = "codex quota cache"
		}
		return unavailableQuotaState(source, "codex quota cache unavailable")
	}
	fresh := codexharness.IsCodexQuotaFresh(snap, time.Now(), 0)
	windows := append([]harnesses.QuotaWindow(nil), snap.Windows...)
	return &QuotaState{
		Windows:    windows,
		CapturedAt: snap.CapturedAt,
		Fresh:      fresh,
		Source:     snap.Source,
		Status:     quotaStatus(fresh, windows),
	}
}

func codexAccountStatus() *AccountStatus {
	snap, ok := codexharness.ReadCodexQuota()
	if !ok || snap == nil {
		return nil
	}
	decision := codexharness.DecideCodexQuotaRouting(snap, time.Now(), 0)
	return accountStatusFromInfo(snap.Account, snap.Source, snap.CapturedAt, decision.Fresh)
}

func geminiQuotaState() *QuotaState {
	snap := geminiharness.ReadAuthEvidence(time.Now())
	qs := &QuotaState{
		CapturedAt: snap.CapturedAt,
		Fresh:      snap.Fresh,
		Source:     snap.Source,
		Status:     "unknown",
	}
	switch {
	case !snap.Authenticated:
		qs.Status = "unauthenticated"
		qs.LastError = &StatusError{
			Type:      "unauthenticated",
			Detail:    snap.Detail,
			Source:    snap.Source,
			Timestamp: time.Now().UTC(),
		}
	case !snap.Fresh:
		qs.Status = "stale"
		qs.LastError = &StatusError{
			Type:      "error",
			Detail:    "Gemini auth evidence is stale; rerun a Gemini auth/status probe before routing automatically",
			Source:    snap.Source,
			Timestamp: snap.CapturedAt,
		}
	default:
		qs.LastError = &StatusError{
			Type:      "unavailable",
			Detail:    "Gemini CLI exposes auth/account evidence but no stable non-interactive quota counter; per-run rate-limit failures remain execution errors",
			Source:    snap.Source,
			Timestamp: snap.CapturedAt,
		}
	}
	return qs
}

func geminiAccountStatus() *AccountStatus {
	snap := geminiharness.ReadAuthEvidence(time.Now())
	if !snap.Authenticated {
		return &AccountStatus{
			Unauthenticated: true,
			Source:          snap.Source,
			CapturedAt:      snap.CapturedAt,
			Fresh:           snap.Fresh,
			Detail:          snap.Detail,
		}
	}
	status := accountStatusFromInfo(snap.Account, snap.Source, snap.CapturedAt, snap.Fresh)
	if status == nil {
		status = &AccountStatus{Authenticated: true, Source: snap.Source, CapturedAt: snap.CapturedAt, Fresh: snap.Fresh}
	}
	status.Detail = snap.Detail
	return status
}

func (s *service) codexUsageWindows() []UsageWindow {
	logDir := s.serviceSessionLogDir()
	if logDir == "" {
		return nil
	}
	now := time.Now().UTC()
	report, err := sessionusage.AggregateUsage(logDir, sessionusage.UsageOptions{Since: "30d", Now: now})
	if err != nil || report == nil {
		return nil
	}
	var total sessionusage.UsageRow
	for _, row := range report.Rows {
		if row.Provider != "codex" {
			continue
		}
		total.Sessions += row.Sessions
		total.SuccessSessions += row.SuccessSessions
		total.FailedSessions += row.FailedSessions
		total.InputTokens += row.InputTokens
		total.OutputTokens += row.OutputTokens
		total.TotalTokens += row.TotalTokens
		total.CacheReadTokens += row.CacheReadTokens
		total.CacheWriteTokens += row.CacheWriteTokens
		total.UnknownCostSessions += row.UnknownCostSessions
		if row.KnownCostUSD == nil || total.UnknownCostSessions > 0 {
			total.KnownCostUSD = nil
		} else {
			if total.KnownCostUSD == nil {
				total.KnownCostUSD = new(float64)
			}
			*total.KnownCostUSD += *row.KnownCostUSD
		}
	}
	if total.Sessions == 0 {
		return nil
	}
	window := UsageWindow{
		Name:                "30d",
		Source:              logDir,
		CapturedAt:          now,
		Fresh:               true,
		InputTokens:         total.InputTokens,
		OutputTokens:        total.OutputTokens,
		TotalTokens:         total.TotalTokens,
		CacheReadTokens:     total.CacheReadTokens,
		CacheWriteTokens:    total.CacheWriteTokens,
		KnownCostUSD:        total.KnownCostUSD,
		UnknownCostSessions: total.UnknownCostSessions,
	}
	if total.KnownCostUSD != nil {
		window.CostUSD = *total.KnownCostUSD
	}
	return []UsageWindow{window}
}

func (s *service) serviceSessionLogDir() string {
	if s == nil || s.opts.ServiceConfig == nil {
		return ""
	}
	workDir := s.opts.ServiceConfig.WorkDir()
	if workDir == "" {
		return ""
	}
	return filepath.Join(workDir, ".agent", "sessions")
}

func unavailableQuotaState(source, detail string) *QuotaState {
	return &QuotaState{
		Source: source,
		Status: "unavailable",
		LastError: &StatusError{
			Type:      "unavailable",
			Detail:    detail,
			Source:    source,
			Timestamp: time.Now().UTC(),
		},
	}
}

// ListHarnesses returns metadata for every registered harness.
func (s *service) ListHarnesses(ctx context.Context) ([]HarnessInfo, error) {
	s.ensurePrimaryQuotaRefresh(ctx, quotaRefreshAsync)
	statuses := s.registry.Discover()

	// Index statuses by name for O(1) lookup.
	statusByName := make(map[string]harnesses.HarnessStatus, len(statuses))
	for _, st := range statuses {
		statusByName[st.Name] = st
	}

	// Emit in registry preference order.
	names := s.registry.Names()
	out := make([]HarnessInfo, 0, len(names))

	for _, name := range names {
		cfg, ok := s.registry.Get(name)
		if !ok {
			continue
		}
		st := statusByName[name]

		info := HarnessInfo{
			Name:                 name,
			Type:                 harnessType(cfg),
			Available:            st.Available,
			Path:                 st.Path,
			Error:                st.Error,
			IsLocal:              cfg.IsLocal,
			IsSubscription:       cfg.IsSubscription,
			AutoRoutingEligible:  cfg.AutoRoutingEligible,
			TestOnly:             cfg.TestOnly,
			ExactPinSupport:      cfg.ExactPinSupport,
			DefaultModel:         cfg.DefaultModel,
			SupportedPermissions: supportedPermissions(cfg),
			SupportedReasoning:   supportedReasoning(cfg),
			CostClass:            cfg.CostClass,
			CapabilityMatrix:     harnessCapabilityMatrix(name, cfg),
		}
		if !st.Available {
			info.LastError = statusError(st.Error, "harness discovery", time.Now())
		}

		// Populate live Quota for harnesses that have durable quota caches.
		switch name {
		case "claude":
			info.Quota = claudeQuotaState()
			info.Account = claudeAccountStatus()
		case "codex":
			info.Quota = codexQuotaState()
			info.Account = codexAccountStatus()
			info.UsageWindows = s.codexUsageWindows()
		case "gemini":
			info.Quota = geminiQuotaState()
			info.Account = geminiAccountStatus()
		}

		out = append(out, info)
	}

	return out, nil
}

// ListProviders and HealthCheck are implemented in service_providers.go.

// ListModels is implemented in service_models.go.

// ResolveRoute is implemented in service_routing.go.

// RouteStatus is implemented in service_routestatus.go.

// Execute is implemented in service_execute.go.

// TailSessionLog is implemented in service_taillog.go.
