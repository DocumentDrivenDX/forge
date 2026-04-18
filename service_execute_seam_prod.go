//go:build !testseam

package agent

// Production builds have no test seams. Each helper returns nil so
// service.Execute compiles identically to the testseam variant but the
// hooks never fire. Production cannot reference FakeProvider or any of
// the assertion hooks because SeamOptions is empty (options_prod.go).

func (s *service) promptAssertionHook() PromptAssertionHookFn         { return nil }
func (s *service) compactionAssertionHook() CompactionAssertionHookFn { return nil }
func (s *service) toolWiringHook() ToolWiringHookFn                   { return nil }

// resolveNativeProvider returns the explicitly-injected provider (when
// the request supplies one) and otherwise returns nil. Production builds
// never see FakeProvider — provider construction lands as part of the
// routing bead (agent-1a486c2e).
func (s *service) resolveNativeProvider(req ServiceExecuteRequest) Provider {
	return req.NativeProvider
}

// PromptAssertionHookFn / CompactionAssertionHookFn / ToolWiringHookFn
// are the function-typed aliases used by the helper signatures above so
// service_execute.go compiles without referencing the testseam-only types
// directly. Their definitions are identical across builds.
type PromptAssertionHookFn func(systemPrompt, userPrompt string, contextFiles []string)
type CompactionAssertionHookFn func(messagesBefore, messagesAfter int, tokensFreed int)
type ToolWiringHookFn func(harness string, toolNames []string)
