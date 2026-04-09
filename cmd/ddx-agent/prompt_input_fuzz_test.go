package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func FuzzParsePromptInput(f *testing.F) {
	seeds := []string{
		`Read main.go and tell me the package name`,
		`{"prompt":"not an envelope"}`,
		`{"kind":"note","title":"hello"}`,
		`{"kind":"prompt","title":"Inspect main"}`,
		`{"kind":1,"title":"Inspect main"}`,
		`{"kind":"prompt","id":"task-42","prompt":"Read main.go"}`,
		`{"kind":"prompt","id":"task-42","title":"Inspect main","prompt":"Read main.go","inputs":{"paths":["main.go"]},"response_schema":{"type":"object"},"callback":{"url":"https://example.com/callback"}}`,
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		promptText, metadata, err := parsePromptInput(raw)

		if strings.TrimSpace(raw) == "" {
			if err != nil {
				t.Fatalf("expected blank input to parse cleanly, got %v", err)
			}
			if promptText != "" {
				t.Fatalf("expected blank input to normalize to empty prompt, got %q", promptText)
			}
			if metadata != nil {
				t.Fatalf("expected nil metadata for blank input, got %#v", metadata)
			}
			return
		}

		switch classifyPromptInput(raw) {
		case promptInputPlain:
			if err != nil {
				t.Fatalf("expected passthrough, got error for %q: %v", raw, err)
			}
			if promptText != raw {
				t.Fatalf("expected prompt passthrough %q, got %q", raw, promptText)
			}
			if metadata != nil {
				t.Fatalf("expected nil metadata for passthrough, got %#v", metadata)
			}
		case promptInputValidEnvelope:
			if err != nil {
				t.Fatalf("expected valid envelope, got error for %q: %v", raw, err)
			}
			if promptText == "" {
				t.Fatalf("expected prompt text for valid envelope %q", raw)
			}
			if metadata == nil {
				t.Fatalf("expected metadata for valid envelope %q", raw)
			}
			if metadata["prompt.kind"] == "" || metadata["prompt.id"] == "" {
				t.Fatalf("expected required prompt metadata for %q, got %#v", raw, metadata)
			}
		case promptInputMalformedEnvelope:
			if err == nil {
				t.Fatalf("expected malformed envelope error for %q", raw)
			}
			if promptText != "" {
				t.Fatalf("expected empty prompt text for malformed envelope %q, got %q", raw, promptText)
			}
			if metadata != nil {
				t.Fatalf("expected nil metadata for malformed envelope %q, got %#v", raw, metadata)
			}
			if !strings.Contains(err.Error(), "prompt envelope") {
				t.Fatalf("expected prompt envelope error for %q, got %v", raw, err)
			}
		}
	})
}

type promptInputClassification int

const (
	promptInputPlain promptInputClassification = iota
	promptInputValidEnvelope
	promptInputMalformedEnvelope
)

func classifyPromptInput(raw string) promptInputClassification {
	if strings.TrimSpace(raw) == "" {
		return promptInputPlain
	}

	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return promptInputPlain
	}

	kindRaw, kindOK := probe["kind"]
	if !kindOK {
		return promptInputPlain
	}

	if !isPromptEnvelopeProbe(probe) {
		var kindValue any
		if err := json.Unmarshal(kindRaw, &kindValue); err == nil {
			if _, ok := kindValue.(string); !ok && hasPromptEnvelopeFields(probe) {
				return promptInputMalformedEnvelope
			}
		} else if hasPromptEnvelopeFields(probe) {
			return promptInputMalformedEnvelope
		}
		return promptInputPlain
	}

	var env ddxPromptEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return promptInputMalformedEnvelope
	}
	if env.Kind == "" || env.ID == "" || env.Prompt == "" {
		return promptInputMalformedEnvelope
	}

	return promptInputValidEnvelope
}
