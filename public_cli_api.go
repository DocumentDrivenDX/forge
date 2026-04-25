package agent

// public_cli_api.go re-exports the minimal set of types and helpers that the
// `cmd/agent` CLI binary needs. These exist so the binary can stay behind a
// strict service-boundary import allowlist (see
// cmd/agent/service_boundary_test.go) while still using shared building
// blocks. Add re-exports here only when removing one would force the CLI to
// import an internal package directly.

import (
	"context"

	"github.com/DocumentDrivenDX/agent/internal/compaction"
	agentcore "github.com/DocumentDrivenDX/agent/internal/core"
	oaiProvider "github.com/DocumentDrivenDX/agent/internal/provider/openai"
	"github.com/DocumentDrivenDX/agent/internal/session"
	"github.com/DocumentDrivenDX/agent/internal/tool"
)

// Compaction.

type CompactionConfig = compaction.Config

func DefaultCompactionConfig() CompactionConfig { return compaction.DefaultConfig() }

// Built-in tool wiring.

type BashOutputFilterConfig = tool.BashOutputFilterConfig

func BuiltinToolsForPreset(workDir, preset string, bashFilter BashOutputFilterConfig) []Tool {
	return tool.BuiltinToolsForPreset(workDir, preset, bashFilter)
}

// OpenAI-shaped model discovery and ranking.

type ScoredModel = oaiProvider.ScoredModel

func DiscoverModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	return oaiProvider.DiscoverModels(ctx, baseURL, apiKey)
}

func RankModels(candidates []string, knownModels map[string]string, pattern string) ([]ScoredModel, error) {
	return oaiProvider.RankModels(candidates, knownModels, pattern)
}

func NormalizeModelID(requested string, catalog []string) (string, error) {
	return oaiProvider.NormalizeModelID(requested, catalog)
}

// Session log inspection.

type (
	SessionEvent     = agentcore.Event
	SessionEventType = agentcore.EventType
	SessionStatus    = agentcore.Status
	SessionEndData   = session.SessionEndData
	SessionStartData = session.SessionStartData
	TokenUsage       = agentcore.TokenUsage
)

const (
	EventSessionStart = agentcore.EventSessionStart
	EventSessionEnd   = agentcore.EventSessionEnd
	StatusSuccess     = agentcore.StatusSuccess
)

func ReadSessionEvents(path string) ([]SessionEvent, error) {
	return session.ReadEvents(path)
}

// SessionLogger writes session log events. CLI tests construct one to seed
// log fixtures that the running CLI later reads back.
type SessionLogger = session.Logger

func NewSessionLogger(dir, sessionID string) *SessionLogger {
	return session.NewLogger(dir, sessionID)
}

func NewSessionEvent(sessionID string, seq int, eventType SessionEventType, data any) SessionEvent {
	return session.NewEvent(sessionID, seq, eventType, data)
}
