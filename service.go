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
)

// DdxAgent is the entire public Go surface of the ddx-agent module.
// See CONTRACT-003 for the full specification.
type DdxAgent interface {
	Execute(ctx context.Context, req ServiceExecuteRequest) (<-chan ServiceEvent, error)
	TailSessionLog(ctx context.Context, sessionID string) (<-chan ServiceEvent, error)
	ListHarnesses(ctx context.Context) ([]HarnessInfo, error)
	ListProviders(ctx context.Context) ([]ProviderInfo, error)
	ListModels(ctx context.Context, filter ModelFilter) ([]ModelInfo, error)
	HealthCheck(ctx context.Context, target HealthTarget) error
	ResolveRoute(ctx context.Context, req RouteRequest) (*RouteDecision, error)
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
	Type    string // "openai-compat" | "anthropic"
	BaseURL string
	APIKey  string
	Model   string // configured default model (may be empty)
}

// ServiceOptions configures a DdxAgent instance.
//
// SeamOptions is embedded so production builds (no testseam tag) get an
// empty struct — making it a compile-time error to set seam fields without
// the build tag. Test builds inherit the four CONTRACT-003 seams
// (FakeProvider, PromptAssertionHook, CompactionAssertionHook,
// ToolWiringHook) automatically.
type ServiceOptions struct {
	SeamOptions

	ConfigPath string    // optional override; default $XDG_CONFIG_HOME/ddx-agent/config.yaml
	Logger     io.Writer // optional; agent writes structured session logs internally regardless

	// ServiceConfig, when non-nil, supplies provider and routing data for
	// ListProviders and HealthCheck. Pass a value wrapping the loaded config.
	// When nil, those methods return an error.
	ServiceConfig ServiceConfig
}

// QuotaState is a live quota snapshot for a harness. Nil means not applicable.
type QuotaState struct {
	Windows    []harnesses.QuotaWindow `json:"windows"`
	CapturedAt time.Time               `json:"captured_at"`
	Fresh      bool                    `json:"fresh"`
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
	ExactPinSupport      bool
	SupportedPermissions []string // subset of {"safe","supervised","unrestricted"}
	SupportedReasoning   []string // values such as {"low","medium","high","xhigh","max"}
	CostClass            string   // "local" | "cheap" | "medium" | "expensive"
	Quota                *QuotaState
}

// CooldownState describes an active routing cooldown for a provider.
type CooldownState struct {
	Reason    string    // "consecutive_failures" | "manual" | etc.
	Until     time.Time // when the cooldown expires
	FailCount int       // number of consecutive failures that triggered the cooldown
	LastError string    // last recorded error message, if available
}

// ProviderInfo describes a provider with live status per CONTRACT-003.
type ProviderInfo struct {
	Name          string
	Type          string // "openai-compat" | "anthropic" | "virtual"
	BaseURL       string
	Status        string // "connected" | "unreachable" | "error: <msg>"
	ModelCount    int
	Capabilities  []string       // e.g. {"tool_use","streaming","json_mode"}
	IsDefault     bool           // matches the configured default_provider
	DefaultModel  string         // per-provider configured default model, if any
	CooldownState *CooldownState // nil if not in cooldown
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
	ID            string
	Provider      string
	Harness       string
	ContextLength int
	Capabilities  []string
	Cost          CostInfo
	PerfSignal    PerfSignal
	Available     bool
	IsConfigured  bool   // matches an explicit model_routes entry
	IsDefault     bool   // matches the configured default model
	CatalogRef    string // canonical catalog reference if recognized
	RankPosition  int    // ordinal in latest discovery rank; -1 if unranked
}

// ModelFilter filters ListModels results.
type ModelFilter struct {
	Harness  string
	Provider string
}

// HealthTarget identifies what to health-check.
type HealthTarget struct {
	Type string // "harness" | "provider"
	Name string
}

// RouteRequest specifies a routing query.
type RouteRequest struct {
	Model       string
	Provider    string
	Harness     string
	ModelRef    string
	Reasoning   Reasoning
	Permissions string
}

// RouteDecision is the result of ResolveRoute.
type RouteDecision struct {
	Harness  string
	Provider string
	Model    string
	Reason   string
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
	WorkDir      string
	Temperature  float32
	Seed         int64
	Reasoning    Reasoning
	Permissions  string

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

	// NativeProvider, when set, overrides provider construction for the
	// native ("agent") path. Required while agent-1a486c2e (ResolveRoute)
	// is unimplemented; supply a constructed Provider directly. Tests
	// also use this together with PreResolved.
	NativeProvider Provider
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
}

// lastDecisionEntry caches the most recent RouteDecision for a route key.
type lastDecisionEntry struct {
	decision *RouteDecision
	at       time.Time
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
	if opts.ServiceConfig == nil && loadServiceConfig != nil {
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
	return &service{
		opts:     opts,
		registry: harnesses.NewRegistry(),
		hub:      newSessionHub(),
	}, nil
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
		return nil
	}
	decision := claudeharness.DecideClaudeQuotaRouting(snap, time.Now(), 0)
	qs := &QuotaState{
		CapturedAt: snap.CapturedAt,
		Fresh:      decision.Fresh,
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
	return qs
}

// ListHarnesses returns metadata for every registered harness.
func (s *service) ListHarnesses(_ context.Context) ([]HarnessInfo, error) {
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
			ExactPinSupport:      cfg.ExactPinSupport,
			SupportedPermissions: supportedPermissions(cfg),
			SupportedReasoning:   supportedReasoning(cfg),
			CostClass:            cfg.CostClass,
		}

		// Populate live Quota for harnesses that have durable quota caches.
		switch name {
		case "claude":
			info.Quota = claudeQuotaState()
		case "codex":
			// Codex quota is only available via PTY/tmux probe (expensive);
			// we don't invoke it inline — leave nil unless a fresh cache exists.
			// A future bead can add a codex quota cache analogous to claude's.
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
