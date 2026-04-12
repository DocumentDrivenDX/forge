package agent

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		transient bool
	}{
		{"nil error", nil, false},
		{"socket hang up", errors.New("socket hang up"), true},
		{"socket hangup no space", errors.New("socket hangup"), true},
		{"connection refused", errors.New("connection refused"), true},
		{"connection error", errors.New("connection error: dial tcp"), true},
		{"429 Too Many Requests", errors.New("429 Too Many Requests"), true},
		{"503 Service Unavailable", errors.New("503 Service Unavailable"), true},
		{"502 Bad Gateway", errors.New("502 Bad Gateway"), true},
		{"500 Internal Server Error", errors.New("500 Internal Server Error"), true},
		{"504 Gateway Timeout", errors.New("504 Gateway Timeout"), true},
		{"rate limit exceeded", errors.New("rate limit exceeded"), true},
		{"rate-limit", errors.New("rate-limit hit"), true},
		{"overloaded", errors.New("model is overloaded"), true},
		{"service unavailable", errors.New("service unavailable"), true},
		{"server error", errors.New("server error occurred"), true},
		{"internal error", errors.New("internal error"), true},
		{"network error", errors.New("network error"), true},
		{"EOF", errors.New("EOF"), true},
		{"io timeout", errors.New("i/o timeout"), true},
		{"timeout", errors.New("request timeout"), true},
		{"timed out", errors.New("request timed out"), true},
		{"connection reset", errors.New("connection reset by peer"), true},
		{"broken pipe", errors.New("broken pipe"), true},
		{"other side closed", errors.New("other side closed"), true},
		{"ended without", errors.New("stream ended without complete response"), true},
		{"fetch failed", errors.New("fetch failed"), true},
		{"too many requests", errors.New("too many requests"), true},

		// Fatal errors — must NOT retry
		{"401 Unauthorized", errors.New("401 Unauthorized"), false},
		{"403 Forbidden", errors.New("403 Forbidden"), false},
		{"unauthorized", errors.New("unauthorized"), false},
		{"forbidden", errors.New("forbidden"), false},
		{"invalid api key", errors.New("invalid api key"), false},
		{"invalid_api_key", errors.New("invalid_api_key"), false},
		{"authentication failed", errors.New("authentication failed"), false},

		// Non-transient, non-fatal errors
		{"400 Bad Request", errors.New("400 Bad Request"), false},
		{"404 Not Found", errors.New("404 Not Found"), false},
		{"invalid model", errors.New("invalid model specified"), false},
		{"context length", errors.New("context length exceeded"), false},

		// Wrapped transient errors
		{"wrapped socket hang up", fmt.Errorf("openai: %w", errors.New("socket hang up")), true},
		{"wrapped 503", fmt.Errorf("openai: %w", errors.New("503 Service Unavailable")), true},
		{"wrapped 429", fmt.Errorf("anthropic: %w", errors.New("429 Too Many Requests")), true},
		{"wrapped connection refused", fmt.Errorf("provider error: %w", errors.New("connection refused")), true},

		// Wrapped fatal errors
		{"wrapped 401", fmt.Errorf("openai: %w", errors.New("401 Unauthorized")), false},
		{"wrapped invalid api key", fmt.Errorf("anthropic: %w", errors.New("invalid api key")), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsTransientError(tt.err)
			if got != tt.transient {
				t.Errorf("IsTransientError(%q) = %v, want %v", tt.err, got, tt.transient)
			}
		})
	}
}
