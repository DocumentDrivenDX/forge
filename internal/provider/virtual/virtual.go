// Package virtual implements a agent.Provider that replays recorded responses
// from a dictionary. This enables deterministic testing without live LLM calls.
//
// Ported from ddx cli/internal/agent/virtual.go with adaptation to the
// agent.Provider interface — responses are structured (content + tool calls +
// token usage) rather than raw text.
package virtual

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

// Entry is a recorded message→response pair stored on disk.
type Entry struct {
	PromptHash string          `json:"prompt_hash"`
	Messages   []agent.Message `json:"messages,omitempty"`
	Response   agent.Response  `json:"response"`
	DelayMS    int             `json:"delay_ms,omitempty"`
	RecordedAt string          `json:"recorded_at,omitempty"`
}

// InlineResponse matches prompts by pattern and returns a fixed response.
type InlineResponse struct {
	PromptMatch string         `json:"prompt_match"` // substring or /regex/
	Response    agent.Response `json:"response"`
	DelayMS     int            `json:"delay_ms,omitempty"`
}

// NormalizePattern is a regex→replacement pair for prompt normalization.
type NormalizePattern struct {
	Pattern string `json:"pattern" yaml:"pattern"`
	Replace string `json:"replace" yaml:"replace"`
}

// Config configures the virtual provider.
type Config struct {
	// DictDir is the directory containing recorded dictionary entries.
	DictDir string

	// InlineResponses are checked before file-based lookup.
	InlineResponses []InlineResponse

	// NormalizePatterns are applied to message content before hashing.
	NormalizePatterns []NormalizePattern
}

// Provider replays recorded responses from a dictionary.
type Provider struct {
	cfg Config
}

// New creates a virtual provider.
func New(cfg Config) *Provider {
	return &Provider{cfg: cfg}
}

// Chat looks up a recorded response matching the messages. It checks inline
// responses first, then falls back to file-based dictionary lookup by hash.
func (p *Provider) Chat(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (agent.Response, error) {
	prompt := extractPromptText(messages)

	// Check inline responses first (pattern-based matching).
	for _, ir := range p.cfg.InlineResponses {
		if matchPattern(ir.PromptMatch, prompt) {
			if ir.DelayMS > 0 {
				sleepWithContext(ctx, ir.DelayMS)
			}
			return ir.Response, nil
		}
	}

	// Fall back to file-based dictionary lookup.
	if p.cfg.DictDir == "" {
		return agent.Response{}, fmt.Errorf("virtual: no matching inline response and no dictionary directory configured")
	}

	normalized := NormalizePrompt(prompt, p.cfg.NormalizePatterns)
	hash := PromptHash(normalized)
	path := filepath.Join(p.cfg.DictDir, hash+".json")

	data, err := safefs.ReadFile(path)
	if os.IsNotExist(err) {
		return agent.Response{}, fmt.Errorf("virtual: no recorded response for prompt (hash %s, dir %s)", hash, p.cfg.DictDir)
	}
	if err != nil {
		return agent.Response{}, fmt.Errorf("virtual: reading dictionary entry: %w", err)
	}

	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return agent.Response{}, fmt.Errorf("virtual: parsing dictionary entry %s: %w", path, err)
	}

	if entry.DelayMS > 0 {
		sleepWithContext(ctx, entry.DelayMS)
	}

	return entry.Response, nil
}

// SessionStartMetadata reports a stable identity for replay-driven runs.
func (p *Provider) SessionStartMetadata() (string, string) {
	return "virtual", ""
}

// RecordEntry saves a message→response pair to the dictionary directory.
func RecordEntry(dictDir string, messages []agent.Message, response agent.Response, patterns []NormalizePattern) error {
	if err := safefs.MkdirAll(dictDir, 0o750); err != nil {
		return fmt.Errorf("virtual: creating dictionary dir: %w", err)
	}

	prompt := extractPromptText(messages)
	normalized := NormalizePrompt(prompt, patterns)
	hash := PromptHash(normalized)

	entry := Entry{
		PromptHash: hash,
		Messages:   messages,
		Response:   response,
		RecordedAt: time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("virtual: marshaling entry: %w", err)
	}

	path := filepath.Join(dictDir, hash+".json")
	if err := safefs.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("virtual: writing entry: %w", err)
	}
	return nil
}

// PromptHash computes a truncated SHA-256 hash of a prompt string.
// Returns 16 hex characters (64 bits).
func PromptHash(prompt string) string {
	h := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(h[:8])
}

// NormalizePrompt applies regex→replacement patterns before hashing.
// This allows prompts with dynamic content (temp paths, timestamps, IDs)
// to produce stable hashes across runs.
func NormalizePrompt(prompt string, patterns []NormalizePattern) string {
	for _, p := range patterns {
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			continue
		}
		prompt = re.ReplaceAllString(prompt, p.Replace)
	}
	return prompt
}

// extractPromptText concatenates all user message content for matching/hashing.
func extractPromptText(messages []agent.Message) string {
	var parts []string
	for _, m := range messages {
		if m.Role == agent.RoleUser {
			parts = append(parts, m.Content)
		}
	}
	return strings.Join(parts, "\n")
}

// matchPattern checks if prompt matches a pattern. Patterns wrapped in /.../ are
// treated as regex; otherwise substring matching is used.
func matchPattern(pattern, prompt string) bool {
	if len(pattern) >= 2 && pattern[0] == '/' && pattern[len(pattern)-1] == '/' {
		re, err := regexp.Compile(pattern[1 : len(pattern)-1])
		if err != nil {
			return false
		}
		return re.MatchString(prompt)
	}
	return strings.Contains(prompt, pattern)
}

func sleepWithContext(ctx context.Context, ms int) {
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(ms) * time.Millisecond):
	}
}

var _ agent.Provider = (*Provider)(nil)
