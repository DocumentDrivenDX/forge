//go:build testseam

package agent

import (
	"context"
)

// Test builds expose the four CONTRACT-003 seams via embedded SeamOptions.
// Each helper returns the configured hook (possibly nil) and the native
// provider resolver consults FakeProvider to bypass real HTTP.

// PromptAssertionHookFn / CompactionAssertionHookFn / ToolWiringHookFn
// are the function-typed aliases shared by service_execute.go. In test
// builds they alias the seam types from testseam_types.go.
type PromptAssertionHookFn = PromptAssertionHook
type CompactionAssertionHookFn = CompactionAssertionHook
type ToolWiringHookFn = ToolWiringHook

func (s *service) promptAssertionHook() PromptAssertionHookFn {
	return s.opts.PromptAssertionHook
}

func (s *service) compactionAssertionHook() CompactionAssertionHookFn {
	return s.opts.CompactionAssertionHook
}

func (s *service) toolWiringHook() ToolWiringHookFn {
	return s.opts.ToolWiringHook
}

// resolveNativeProvider returns FakeProvider when set on the service
// options (the standard test seam) and otherwise honors any
// caller-supplied req.NativeProvider. Real provider construction is
// deferred to the routing bead (agent-1a486c2e).
func (s *service) resolveNativeProvider(req ServiceExecuteRequest) Provider {
	if s.opts.FakeProvider != nil {
		return &fakeProviderAdapter{fp: s.opts.FakeProvider}
	}
	return req.NativeProvider
}

// fakeProviderAdapter wraps a *FakeProvider so it satisfies the
// agent.Provider interface. Static responses are consumed in order;
// Dynamic is invoked per call when set; InjectError fires per-call to
// optionally return an error before the response.
type fakeProviderAdapter struct {
	fp        *FakeProvider
	callIndex int
}

func (a *fakeProviderAdapter) Chat(ctx context.Context, messages []Message, tools []ToolDef, opts Options) (Response, error) {
	defer func() { a.callIndex++ }()
	if a.fp == nil {
		return Response{}, nil
	}
	if a.fp.InjectError != nil {
		if err := a.fp.InjectError(a.callIndex); err != nil {
			return Response{}, err
		}
	}
	if a.fp.Dynamic != nil {
		toolNames := make([]string, len(tools))
		for i, t := range tools {
			toolNames[i] = t.Name
		}
		freq := FakeRequest{
			Messages:  messages,
			Tools:     toolNames,
			Model:     opts.Model,
			Reasoning: opts.Reasoning,
		}
		fresp, err := a.fp.Dynamic(freq)
		if err != nil {
			return Response{}, err
		}
		return fakeResponseToResponse(fresp), nil
	}
	if a.callIndex < len(a.fp.Static) {
		return fakeResponseToResponse(a.fp.Static[a.callIndex]), nil
	}
	// Out of static script — return an empty response so the loop
	// terminates with no further tool calls.
	return Response{Content: "", Usage: TokenUsage{}}, nil
}

func fakeResponseToResponse(fr FakeResponse) Response {
	return Response{
		Content:   fr.Text,
		ToolCalls: fr.ToolCalls,
		Usage:     fr.Usage,
	}
}
