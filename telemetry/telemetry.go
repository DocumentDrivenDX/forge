package telemetry

import (
	"context"
	"fmt"
	"strings"

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
	KeyTurnIndex             = "ddx.turn.index"
	KeyAttemptIndex          = "ddx.attempt.index"
	KeyToolExecutionIndex    = "ddx.tool.execution.index"
	KeyProviderSystem        = "ddx.provider.system"
	KeyProviderRoute         = "ddx.provider.route"
	KeyProviderModelResolved = "ddx.provider.model_resolved"

	// Standard OTel GenAI keys.
	KeyConversationID = "gen_ai.conversation.id"
	KeyAgentName      = "gen_ai.agent.name"
	KeyAgentVersion   = "gen_ai.agent.version"
	KeyAgentID        = "gen_ai.agent.id"
	KeyOperationName  = "gen_ai.operation.name"
	KeyProviderName   = "gen_ai.provider.name"
	KeyRequestModel   = "gen_ai.request.model"
	KeyResponseModel  = "gen_ai.response.model"
	KeyToolName       = "gen_ai.tool.name"
	KeyToolType       = "gen_ai.tool.type"
	KeyToolCallID     = "gen_ai.tool.call.id"
	KeyServerAddress  = "server.address"
	KeyServerPort     = "server.port"
)

// Telemetry exposes the runtime-facing span scaffolding used by the agent loop.
// The returned context carries the started span and, for root spans, the run
// identity that child spans should inherit.
type Telemetry interface {
	StartInvokeAgent(ctx context.Context, attrs InvokeAgentSpan) (context.Context, trace.Span)
	StartChat(ctx context.Context, attrs ChatSpan) (context.Context, trace.Span)
	StartExecuteTool(ctx context.Context, attrs ExecuteToolSpan) (context.Context, trace.Span)
	// Shutdown is best-effort. Exporter or flush failures are swallowed so
	// telemetry cannot break the agent loop.
	Shutdown(ctx context.Context)
}

// Config controls how a telemetry runtime is constructed.
type Config struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	// Shutdown is called during best-effort shutdown if provided.
	Shutdown func(context.Context) error
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
	tracer   trace.Tracer
	meter    metric.Meter
	shutdown func(context.Context) error
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
	if mp != nil {
		m = mp.Meter(InstrumentationName)
	}

	return &runtime{
		tracer:   tp.Tracer(InstrumentationName),
		meter:    m,
		shutdown: cfg.Shutdown,
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
	_ = r.shutdown(ctx)
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
	ctx, span := r.tracer.Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(chatAttributes(merged)...),
	)
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
