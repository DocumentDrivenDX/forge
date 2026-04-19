//go:build testseam

// Package agent provides test seam types for injection during testing.
// These types are ONLY compiled when the testseam build tag is set.
// Production binaries cannot construct or reference these types.
package agent

// FakeProvider supports three patterns for intercepting provider Chat calls
// during tests:
//   - Static script: sequence of pre-recorded responses, consumed in order.
//   - Dynamic callback: function called per request returning a response.
//   - Error injection: per-call status override.
type FakeProvider struct {
	Static      []FakeResponse
	Dynamic     func(req FakeRequest) (FakeResponse, error)
	InjectError func(callIndex int) error
}

// FakeRequest is the request value delivered to a FakeProvider callback.
type FakeRequest struct {
	Messages  []Message
	Tools     []string
	Model     string
	Reasoning Reasoning
}

// FakeResponse is the response value returned by a FakeProvider.
type FakeResponse struct {
	Text      string
	ToolCalls []ToolCall
	Usage     TokenUsage
	Status    string // "success" by default
}

// PromptAssertionHook is called once per Execute with the system+user prompt
// the agent actually sent to the model. Used by tests that verify prompt
// construction/compaction without running a real provider.
type PromptAssertionHook func(systemPrompt, userPrompt string, contextFiles []string)

// CompactionAssertionHook is called whenever a real compaction runs. No-op
// compactions are NOT delivered (they don't fire compaction events either).
type CompactionAssertionHook func(messagesBefore, messagesAfter int, tokensFreed int)

// ToolWiringHook is called once per Execute with the resolved tool list and
// the harness that received it. Used by tests that verify the right tools
// land at the right harness given the request's permission level.
type ToolWiringHook func(harness string, toolNames []string)
