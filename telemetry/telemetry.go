package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const (
	// InstrumentationName identifies this package in OTel tracer/meter setup.
	InstrumentationName = "github.com/DocumentDrivenDX/agent/telemetry"

	operationInvokeAgent = "invoke_agent"
	operationChat        = "chat"
	operationExecuteTool = "execute_tool"

	// DDX identity keys.
	KeyHarnessName           = "ddx.harness.name"
	KeyHarnessVersion        = "ddx.harness.version"
	KeySessionID             = "ddx.session.id"
	KeyParentSessionID       = "ddx.parent.session.id"
	KeyRequestedModelRef     = "ddx.request.model_ref"
	KeyTurnIndex             = "ddx.turn.index"
	KeyAttemptIndex          = "ddx.attempt.index"
	KeyToolExecutionIndex    = "ddx.tool.execution.index"
	KeyProviderSystem        = "ddx.provider.system"
	KeyProviderRoute         = "ddx.provider.route"
	KeyAttemptedProviders    = "ddx.routing.attempted_providers"
	KeyFailoverCount         = "ddx.routing.failover_count"
	KeyProviderModelResolved = "ddx.provider.model_resolved"
	KeyCostSource            = "ddx.cost.source"
	KeyCostCurrency          = "ddx.cost.currency"
	KeyCostAmount            = "ddx.cost.amount"
	KeyCostInputAmount       = "ddx.cost.input_amount"
	KeyCostOutputAmount      = "ddx.cost.output_amount"
	KeyCostCacheReadAmount   = "ddx.cost.cache_read_amount"
	KeyCostCacheWriteAmount  = "ddx.cost.cache_write_amount"
	KeyCostReasoningAmount   = "ddx.cost.reasoning_amount"
	KeyCostPricingRef        = "ddx.cost.pricing_ref"
	KeyCostRaw               = "ddx.cost.raw"
	KeyTimingFirstTokenMS    = "ddx.timing.first_token_ms"
	KeyTimingQueueMS         = "ddx.timing.queue_ms"
	KeyTimingPrefillMS       = "ddx.timing.prefill_ms"
	KeyTimingGenerationMS    = "ddx.timing.generation_ms"
	KeyTimingCacheReadMS     = "ddx.timing.cache_read_ms"
	KeyTimingCacheWriteMS    = "ddx.timing.cache_write_ms"

	// Standard OTel GenAI keys.
	KeyConversationID  = "gen_ai.conversation.id"
	KeyAgentName       = "gen_ai.agent.name"
	KeyAgentVersion    = "gen_ai.agent.version"
	KeyAgentID         = "gen_ai.agent.id"
	KeyOperationName   = "gen_ai.operation.name"
	KeyProviderName    = "gen_ai.provider.name"
	KeyRequestModel    = "gen_ai.request.model"
	KeyResponseModel   = "gen_ai.response.model"
	KeyToolName        = "gen_ai.tool.name"
	KeyToolType        = "gen_ai.tool.type"
	KeyToolCallID      = "gen_ai.tool.call.id"
	KeyUsageInput      = "gen_ai.usage.input_tokens"
	KeyUsageOutput     = "gen_ai.usage.output_tokens"
	KeyUsageCacheRead  = "gen_ai.usage.cache_read.input_tokens"
	KeyUsageCacheWrite = "gen_ai.usage.cache_creation.input_tokens"
	// #nosec G101 -- OTel semantic convention key, not a credential.
	KeyTokenType     = "gen_ai.token.type"
	KeyServerAddress = "server.address"
	KeyServerPort    = "server.port"
	KeyErrorType     = "error.type"
)

// Usage captures token counts for metric recording without depending on the
// agent package.
type Usage struct {
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
	Total      int
}

// ChatMetrics carries the completion-specific values needed to emit metrics.
type ChatMetrics struct {
	ResponseModel string
	ResolvedModel string
	Usage         Usage
	Duration      time.Duration
	Err           error
}

// ChatMetricsRecorder records completed chat attempts into OTel metrics.
type ChatMetricsRecorder interface {
	RecordChatMetrics(ctx context.Context, attrs ChatSpan, metrics ChatMetrics)
}

// Telemetry exposes the runtime-facing span scaffolding used by the agent loop.
// The returned context carries the started span and, for root spans, the run
// identity that child spans should inherit.
type Telemetry interface {
	StartInvokeAgent(ctx context.Context, attrs InvokeAgentSpan) (context.Context, trace.Span)
	StartChat(ctx context.Context, attrs ChatSpan) (context.Context, trace.Span)
	StartExecuteTool(ctx context.Context, attrs ExecuteToolSpan) (context.Context, trace.Span)
	// ResolveCost returns the configured runtime-specific cost for an exact
	// provider system / resolved model match.
	ResolveCost(providerSystem, resolvedModel string) (Cost, bool)
	// Shutdown is best-effort. Exporter or flush failures are swallowed so
	// telemetry cannot break the agent loop.
	Shutdown(ctx context.Context)
}

// Cost captures runtime-specific configured pricing for an exact model match.
// When Amount is set it represents a pre-computed total cost in Currency. When
// only InputPerMTok / OutputPerMTok are set, callers must multiply by the
// actual token usage to derive the total cost.
type Cost struct {
	Source         string   `json:"source,omitempty" yaml:"-"`
	Amount         *float64 `json:"amount,omitempty"`
	Currency       string   `json:"currency,omitempty"`
	PricingRef     string   `json:"pricing_ref,omitempty"`
	InputPerMTok   float64  `json:"input_per_mtok,omitempty"    yaml:"input_per_mtok,omitempty"`
	OutputPerMTok  float64  `json:"output_per_mtok,omitempty"   yaml:"output_per_mtok,omitempty"`
	CacheReadPerM  float64  `json:"cache_read_per_m,omitempty"  yaml:"cache_read_per_m,omitempty"`
	CacheWritePerM float64  `json:"cache_write_per_m,omitempty" yaml:"cache_write_per_m,omitempty"`
}

// RuntimePricing maps provider system -> resolved model -> exact configured
// cost for that runtime/model pair.
type RuntimePricing map[string]map[string]Cost

// Config controls how a telemetry runtime is constructed.
type Config struct {
	// Enabled is a configuration switch that callers can use to decide whether
	// to wire a real exporter-backed runtime or a no-op runtime.
	Enabled bool `yaml:"enabled,omitempty"`
	// Pricing contains exact runtime-specific cost entries keyed by provider
	// system and resolved model.
	Pricing        RuntimePricing       `yaml:"pricing,omitempty"`
	TracerProvider trace.TracerProvider `yaml:"-"`
	MeterProvider  metric.MeterProvider `yaml:"-"`
	// Shutdown is called during best-effort shutdown if provided.
	Shutdown func(context.Context) error `yaml:"-"`
}

// InvokeAgentSpan carries the attributes for the root run span.
type InvokeAgentSpan struct {
	HarnessName     string
	HarnessVersion  string
	SessionID       string
	ParentSessionID string
	ConversationID  string
	AgentName       string
	AgentVersion    string
	AgentID         string
}

// ChatSpan carries the attributes for a provider attempt span.
type ChatSpan struct {
	HarnessName     string
	HarnessVersion  string
	SessionID       string
	ConversationID  string
	ParentSessionID string
	TurnIndex       int
	AttemptIndex    int
	StartTime       time.Time
	ProviderName    string
	ProviderSystem  string
	ProviderRoute   string
	RequestedModel  string
	ResponseModel   string
	ResolvedModel   string
	ServerAddress   string
	ServerPort      int
}

// ExecuteToolSpan carries the attributes for a tool execution span.
type ExecuteToolSpan struct {
	HarnessName        string
	HarnessVersion     string
	SessionID          string
	ConversationID     string
	ParentSessionID    string
	TurnIndex          int
	ToolExecutionIndex int
	ToolName           string
	ToolType           string
	ToolCallID         string
}

type runState struct {
	invoke InvokeAgentSpan
}

type ctxKey struct{}

type runtime struct {
	tracer        trace.Tracer
	meter         metric.Meter
	chatDuration  metric.Float64Histogram
	chatTokenUsed metric.Int64Histogram
	pricing       RuntimePricing
	shutdown      func(context.Context) error
}

// New constructs a telemetry runtime. Nil providers fall back to no-op
// providers, making this safe to use as the default runtime.
func New(cfg Config) Telemetry {
	tp := cfg.TracerProvider
	if tp == nil {
		tp = trace.NewNoopTracerProvider()
	}

	mp := cfg.MeterProvider
	var m metric.Meter
	var chatDuration metric.Float64Histogram
	var chatTokenUsed metric.Int64Histogram
	if mp != nil {
		m = mp.Meter(InstrumentationName)
		chatDuration, _ = m.Float64Histogram(
			"gen_ai.client.operation.duration",
			metric.WithDescription("GenAI operation duration."),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92),
		)
		chatTokenUsed, _ = m.Int64Histogram(
			"gen_ai.client.token.usage",
			metric.WithDescription("Number of input and output tokens used."),
			metric.WithUnit("{token}"),
			metric.WithExplicitBucketBoundaries(1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864),
		)
	}

	return &runtime{
		tracer:        tp.Tracer(InstrumentationName),
		meter:         m,
		chatDuration:  chatDuration,
		chatTokenUsed: chatTokenUsed,
		pricing:       cfg.Pricing,
		shutdown:      cfg.Shutdown,
	}
}

// NewNoop returns the default no-op telemetry runtime.
func NewNoop() Telemetry {
	return New(Config{})
}

func (r *runtime) Shutdown(ctx context.Context) {
	if r.shutdown == nil {
		return
	}
	if err := r.shutdown(ctx); err != nil {
		slog.Warn("telemetry: shutdown failed", "err", err)
	}
}

func (r *runtime) StartInvokeAgent(ctx context.Context, attrs InvokeAgentSpan) (context.Context, trace.Span) {
	merged := mergeInvokeAgent(ctx, attrs)
	spanName := spanName(operationInvokeAgent, merged.HarnessName)
	ctx, span := r.tracer.Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(runAttributes(merged)...),
	)
	ctx = context.WithValue(ctx, ctxKey{}, runState{invoke: merged})
	return ctx, span
}

func (r *runtime) StartChat(ctx context.Context, attrs ChatSpan) (context.Context, trace.Span) {
	merged := mergeChat(ctx, attrs)
	spanName := spanName(operationChat, merged.RequestedModel)
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(chatAttributes(merged)...),
	}
	if !merged.StartTime.IsZero() {
		opts = append(opts, trace.WithTimestamp(merged.StartTime))
	}
	ctx, span := r.tracer.Start(ctx, spanName, opts...)
	return ctx, span
}

func (r *runtime) StartExecuteTool(ctx context.Context, attrs ExecuteToolSpan) (context.Context, trace.Span) {
	merged := mergeTool(ctx, attrs)
	spanName := spanName(operationExecuteTool, merged.ToolName)
	ctx, span := r.tracer.Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(toolAttributes(merged)...),
	)
	return ctx, span
}

func (r *runtime) RecordChatMetrics(ctx context.Context, attrs ChatSpan, metrics ChatMetrics) {
	if r == nil {
		return
	}

	metricAttrs := chatMetricAttributes(attrs, metrics)
	if r.chatDuration != nil {
		r.chatDuration.Record(ctx, metrics.Duration.Seconds(), metric.WithAttributes(metricAttrs...))
	}
	if r.chatTokenUsed == nil {
		return
	}

	if metrics.Usage.Input > 0 {
		r.chatTokenUsed.Record(ctx, int64(metrics.Usage.Input), metric.WithAttributes(tokenMetricAttributes(metricAttrs, "input")...))
	}
	if metrics.Usage.Output > 0 {
		r.chatTokenUsed.Record(ctx, int64(metrics.Usage.Output), metric.WithAttributes(tokenMetricAttributes(metricAttrs, "output")...))
	}
}

func (r *runtime) ResolveCost(providerSystem, resolvedModel string) (Cost, bool) {
	if r == nil {
		return Cost{}, false
	}

	systemPricing, ok := r.pricing[providerSystem]
	if !ok {
		return Cost{}, false
	}

	cost, ok := systemPricing[resolvedModel]
	if !ok {
		return Cost{}, false
	}

	// Require at least a pre-computed amount or per-MTok rates to be meaningful.
	if cost.Amount == nil && cost.InputPerMTok == 0 && cost.OutputPerMTok == 0 {
		return Cost{}, false
	}

	if cost.Source == "" {
		cost.Source = "configured"
	}
	if cost.Currency == "" {
		cost.Currency = "USD"
	}
	if cost.PricingRef == "" {
		cost.PricingRef = fmt.Sprintf("%s/%s", providerSystem, resolvedModel)
	}
	return cost, true
}

func mergeInvokeAgent(ctx context.Context, attrs InvokeAgentSpan) InvokeAgentSpan {
	state := stateFromContext(ctx)
	merged := state.invoke
	merged.HarnessName = firstNonEmpty(attrs.HarnessName, merged.HarnessName)
	merged.HarnessVersion = firstNonEmpty(attrs.HarnessVersion, merged.HarnessVersion)
	merged.SessionID = firstNonEmpty(attrs.SessionID, merged.SessionID)
	merged.ParentSessionID = firstNonEmpty(attrs.ParentSessionID, merged.ParentSessionID)
	merged.ConversationID = firstNonEmpty(attrs.ConversationID, merged.ConversationID)
	merged.AgentName = firstNonEmpty(attrs.AgentName, merged.AgentName)
	merged.AgentVersion = firstNonEmpty(attrs.AgentVersion, merged.AgentVersion)
	merged.AgentID = firstNonEmpty(attrs.AgentID, merged.AgentID)
	if merged.ConversationID == "" {
		merged.ConversationID = merged.SessionID
	}
	return merged
}

func mergeChat(ctx context.Context, attrs ChatSpan) ChatSpan {
	state := stateFromContext(ctx)
	merged := attrs
	merged.HarnessName = firstNonEmpty(attrs.HarnessName, state.invoke.HarnessName)
	merged.HarnessVersion = firstNonEmpty(attrs.HarnessVersion, state.invoke.HarnessVersion)
	merged.SessionID = firstNonEmpty(attrs.SessionID, state.invoke.SessionID)
	merged.ConversationID = firstNonEmpty(attrs.ConversationID, state.invoke.ConversationID)
	merged.ParentSessionID = firstNonEmpty(attrs.ParentSessionID, state.invoke.ParentSessionID)
	if merged.ConversationID == "" {
		merged.ConversationID = merged.SessionID
	}
	return merged
}

func mergeTool(ctx context.Context, attrs ExecuteToolSpan) ExecuteToolSpan {
	state := stateFromContext(ctx)
	merged := attrs
	merged.HarnessName = firstNonEmpty(attrs.HarnessName, state.invoke.HarnessName)
	merged.HarnessVersion = firstNonEmpty(attrs.HarnessVersion, state.invoke.HarnessVersion)
	merged.SessionID = firstNonEmpty(attrs.SessionID, state.invoke.SessionID)
	merged.ConversationID = firstNonEmpty(attrs.ConversationID, state.invoke.ConversationID)
	merged.ParentSessionID = firstNonEmpty(attrs.ParentSessionID, state.invoke.ParentSessionID)
	if merged.ConversationID == "" {
		merged.ConversationID = merged.SessionID
	}
	return merged
}

func stateFromContext(ctx context.Context) runState {
	state, _ := ctx.Value(ctxKey{}).(runState)
	return state
}

func runAttributes(attrs InvokeAgentSpan) []attribute.KeyValue {
	out := []attribute.KeyValue{
		attribute.String(KeyOperationName, operationInvokeAgent),
	}
	out = appendString(out, KeyHarnessName, attrs.HarnessName)
	out = appendString(out, KeyHarnessVersion, attrs.HarnessVersion)
	out = appendString(out, KeySessionID, attrs.SessionID)
	out = appendString(out, KeyParentSessionID, attrs.ParentSessionID)
	out = appendString(out, KeyConversationID, attrs.ConversationID)
	out = appendString(out, KeyAgentName, attrs.AgentName)
	out = appendString(out, KeyAgentVersion, attrs.AgentVersion)
	out = appendString(out, KeyAgentID, attrs.AgentID)
	return out
}

func chatAttributes(attrs ChatSpan) []attribute.KeyValue {
	out := []attribute.KeyValue{
		attribute.String(KeyOperationName, operationChat),
	}
	out = appendString(out, KeyHarnessName, attrs.HarnessName)
	out = appendString(out, KeyHarnessVersion, attrs.HarnessVersion)
	out = appendString(out, KeySessionID, attrs.SessionID)
	out = appendString(out, KeyParentSessionID, attrs.ParentSessionID)
	out = appendString(out, KeyConversationID, attrs.ConversationID)
	out = appendInt(out, KeyTurnIndex, attrs.TurnIndex)
	out = appendInt(out, KeyAttemptIndex, attrs.AttemptIndex)
	out = appendString(out, KeyProviderName, attrs.ProviderName)
	out = appendString(out, KeyProviderSystem, attrs.ProviderSystem)
	out = appendString(out, KeyProviderRoute, attrs.ProviderRoute)
	out = appendString(out, KeyRequestModel, attrs.RequestedModel)
	out = appendString(out, KeyResponseModel, attrs.ResponseModel)
	out = appendString(out, KeyProviderModelResolved, attrs.ResolvedModel)
	out = appendString(out, KeyServerAddress, attrs.ServerAddress)
	out = appendInt(out, KeyServerPort, attrs.ServerPort)
	return out
}

func chatMetricAttributes(attrs ChatSpan, metrics ChatMetrics) []attribute.KeyValue {
	out := []attribute.KeyValue{
		attribute.String(KeyOperationName, operationChat),
	}
	out = appendString(out, KeyProviderName, attrs.ProviderName)
	out = appendString(out, KeyProviderSystem, attrs.ProviderSystem)
	out = appendString(out, KeyProviderRoute, attrs.ProviderRoute)
	out = appendString(out, KeyRequestModel, attrs.RequestedModel)
	out = appendString(out, KeyResponseModel, firstNonEmpty(metrics.ResponseModel, attrs.ResponseModel))
	out = appendString(out, KeyProviderModelResolved, firstNonEmpty(metrics.ResolvedModel, attrs.ResolvedModel))
	out = appendString(out, KeyServerAddress, attrs.ServerAddress)
	out = appendInt(out, KeyServerPort, attrs.ServerPort)
	if metrics.Err != nil {
		out = appendString(out, KeyErrorType, fmt.Sprintf("%T", metrics.Err))
	}
	return out
}

func tokenMetricAttributes(base []attribute.KeyValue, tokenType string) []attribute.KeyValue {
	out := append([]attribute.KeyValue(nil), base...)
	out = append(out, attribute.String(KeyTokenType, tokenType))
	return out
}

func toolAttributes(attrs ExecuteToolSpan) []attribute.KeyValue {
	out := []attribute.KeyValue{
		attribute.String(KeyOperationName, operationExecuteTool),
	}
	out = appendString(out, KeyHarnessName, attrs.HarnessName)
	out = appendString(out, KeyHarnessVersion, attrs.HarnessVersion)
	out = appendString(out, KeySessionID, attrs.SessionID)
	out = appendString(out, KeyParentSessionID, attrs.ParentSessionID)
	out = appendString(out, KeyConversationID, attrs.ConversationID)
	out = appendInt(out, KeyTurnIndex, attrs.TurnIndex)
	out = appendInt(out, KeyToolExecutionIndex, attrs.ToolExecutionIndex)
	out = appendString(out, KeyToolName, attrs.ToolName)
	out = appendString(out, KeyToolType, attrs.ToolType)
	out = appendString(out, KeyToolCallID, attrs.ToolCallID)
	return out
}

func appendString(dst []attribute.KeyValue, key, value string) []attribute.KeyValue {
	if value == "" {
		return dst
	}
	return append(dst, attribute.String(key, value))
}

func appendInt(dst []attribute.KeyValue, key string, value int) []attribute.KeyValue {
	if value == 0 {
		return dst
	}
	return append(dst, attribute.Int(key, value))
}

func spanName(prefix, suffix string) string {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		suffix = "unknown"
	}
	return fmt.Sprintf("%s %s", prefix, suffix)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
