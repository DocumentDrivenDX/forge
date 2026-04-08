---
ddx:
  id: SD-006
  depends_on:
    - FEAT-001
    - SD-001
---
# Solution Design: SD-006 — Conversation Compaction

## Problem

The agent loop appends every message and tool result to the conversation
history. For tasks requiring many tool-call rounds, the history will exceed
the model's context window and the provider will return an error. Local models
have especially small windows (8K-32K) making this a practical blocker.

## Research Summary

### Pi's Approach
- **Trigger**: `contextTokens > contextWindow - reserveTokens` (reserve: 16K default)
- **Keep recent**: ~20K tokens of recent messages preserved verbatim
- **Summarize**: Everything before the cut point summarized by the LLM
- **Format**: Structured markdown — Goal, Progress (checkboxes), Key Decisions, Next Steps, Critical Context
- **Update mode**: When prior summary exists, merges new info rather than re-summarizing
- **File tracking**: Accumulates read/modified file lists in XML tags on the summary
- **Tool result truncation**: 2K chars max in summarization input
- **Split turn handling**: Separate "turn prefix summary" when cut falls mid-turn

### Codex's Approach
- **Trigger**: `total_usage_tokens >= auto_compact_token_limit` (per-model, e.g., 200K)
- **Two modes**: Local (LLM-based) and remote (OpenAI server-side `/responses/compact` API)
- **Prompt**: Short — "Create a handoff summary for another LLM that will resume the task"
- **Summary injection**: "Another language model started to solve this problem..."
- **User messages**: Keeps up to 20K tokens of recent user messages alongside summary
- **Timing**: Pre-turn (before user input) and mid-turn (between tool call rounds)
- **Fallback**: Trims oldest history items if compaction prompt itself exceeds context
- **Warning**: Alerts user that multiple compactions degrade accuracy
- **Cache preservation**: When trimming oversize compaction input, trims from the
  *beginning* to preserve prefix-based prompt cache hits. The comment explicitly
  states: "Trim from the beginning to preserve cache (prefix-based) and keep
  recent messages intact."
- **Initial context reinjection**: Two modes — `DoNotInject` (pre-turn: clears
  `reference_context_item` so the next regular turn reinjects system context
  fresh) and `BeforeLastUserMessage` (mid-turn: injects system context into the
  replacement history just above the last user message, because the model expects
  the compaction summary as the last item)
- **Window generation**: After compaction, advances a `window_generation` counter
  that invalidates the websocket session / prompt cache, forcing the provider to
  re-process the compacted history from scratch
- **Ghost snapshots**: Preserves undo/redo state snapshots across compaction by
  copying them into the replacement history

## Design: DDX Agent Compaction

### Strategy

DDX Agent follows pi's structured approach (richer summaries, file tracking) with
Codex's pragmatism (mid-turn compaction, configurable thresholds, graceful
fallback). The compaction is a library feature — not just CLI — so embedders
can control it.

### Configuration

```go
type CompactionConfig struct {
    // Enabled controls whether automatic compaction runs. Default: true.
    Enabled bool

    // ContextWindow is the model's context window in tokens. If zero,
    // the provider is queried or a conservative default (8192) is used.
    ContextWindow int

    // ReserveTokens is the token budget reserved for the model's response
    // and the next prompt. Compaction triggers when conversation tokens
    // exceed ContextWindow - ReserveTokens. Default: 8192.
    ReserveTokens int

    // KeepRecentTokens is how many tokens of recent messages to preserve
    // verbatim after compaction. Default: 8192.
    KeepRecentTokens int

    // MaxToolResultChars is the max characters per tool result included in
    // the summarization input. Longer results are truncated with a
    // "[... N more characters truncated]" marker. Default: 2000 (matching pi).
    MaxToolResultChars int

    // SummarizationModel overrides the model used for summarization.
    // If empty, uses the same model as the agent loop. Useful for using
    // a faster/cheaper model for compaction (e.g., local model for
    // summarization even when the agent uses a cloud model).
    SummarizationModel string

    // SummarizationProvider overrides the provider for summarization.
    // If nil, uses the same provider as the agent loop.
    SummarizationProvider Provider

    // SummarizationFocus is optional caller-provided text appended to
    // the summarization prompt as "Additional focus: {text}". Lets
    // embedders influence what the summary emphasizes.
    SummarizationFocus string

    // EffectivePercent is the percentage of ContextWindow to actually use.
    // Default: 95. Provides safety margin since models may fail slightly
    // below their advertised limit.
    EffectivePercent int
}
```

Added to `Request`:

```go
type Request struct {
    // ...existing fields...

    // Compaction configures automatic conversation compaction.
    // If nil, compaction is enabled with defaults.
    Compaction *CompactionConfig
}
```

### Trigger Logic

```
effectiveWindow = contextWindow * effectivePercent / 100  (default 95%)
shouldCompact = estimatedTokens > (effectiveWindow - reserveTokens)
```

The `effectivePercent` (default 95%, following Codex) applies a safety margin
because models may fail slightly below their advertised context limit due to
internal overhead. For a 32K model: effective = 30720, trigger at 30720 - 8192 = 22528.

Token estimation: use the provider's reported usage from the last response
(accurate), plus chars/4 heuristic for messages added since. For the trigger
check, use the full context footprint:
```
effectiveTokens = usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
```
This matches pi's `calculateContextTokens()`. All four components contribute
to context window consumption — output tokens from the current turn become
input tokens on the next turn, and cache tokens represent context the model
processed regardless of billing discount.

**Non-text estimation**: Tool call arguments are estimated by JSON byte length / 4.
Images (if supported) use a fixed estimate of 1200 tokens (following pi's 4800
chars / 4). Thinking/reasoning blocks are included in char count.

Checked at two points:
1. **Pre-iteration**: Before sending the next prompt to the model
2. **Mid-iteration**: After tool results are appended (a large bash output
   can push over the limit between iterations)

### What Gets Compacted

1. Walk backwards from newest messages, accumulating token estimates
2. Stop when `keepRecentTokens` is reached — everything after this point is kept
3. Everything before the cut point is serialized and summarized
4. The cut point must be at a valid turn boundary. Valid boundaries are:
   user messages, assistant messages, and bash tool executions (which are
   natural turn breaks like pi's `bashExecution` entries). Tool result
   messages are NOT valid cut points — they must follow their tool call.
5. **Re-compaction guard**: If the most recent entry is already a compaction
   summary, skip compaction (following pi — prevents compacting a summary
   that was just created)
5. **Previous compaction entries are excluded** from the messages-to-summarize
   (the previous summary is passed separately via `<previous-summary>` tags)
6. **Session prefix messages are excluded** from the preserved user messages
   (AGENTS.md injections, environment context, system prompt wrappers are not
   "real" user messages — following Codex's `collect_user_messages` filter)
7. **Overlong individual user messages are truncated** to fit within the
   `keepRecentTokens` budget (following Codex's `build_compacted_history_with_limit`)

### Serialization for Summarization

Tool calls serialized compactly:
```
[User]: Read main.go and fix the bug
[Assistant → read(path="main.go")]: package main...
[Assistant → edit(path="main.go", old="bug", new="fix")]: Replaced 1 occurrence
[Assistant]: Fixed the bug by replacing...
```

Tool results truncated to `MaxToolResultChars` (default 2000 characters,
matching pi). Truncation keeps the beginning and appends
`[... N more characters truncated]`.

### Summarization Call

The summarization uses a **separate system prompt** (following pi) to prevent
the model from continuing the conversation instead of summarizing:

**System prompt** (for the summarization LLM call):
```
You are a context summarization assistant. Your task is to read a
conversation between a user and an AI coding assistant, then produce
a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions
in the conversation. ONLY output the structured summary.
```

**User prompt** (initial summarization):
```
<conversation>
{serialized conversation text}
</conversation>

You are performing a CONTEXT CHECKPOINT COMPACTION. Create a structured
handoff summary for another LLM that will resume this task.

Use this EXACT format:

## Goal
[What the user is trying to accomplish]

## Constraints & Preferences
- [Requirements, conventions, or preferences mentioned]

## Progress
### Done
- [x] [Completed work with file paths]

### In Progress
- [ ] [Current work]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [What should happen next]

## Critical Context
- [Data, error messages, or references needed to continue]

### Blocked
- [Issues preventing progress, if any, or "(none)"]

Keep each section concise. Preserve exact file paths, function names,
and error messages.
```

After summarization, the file tracking module appends file lists as XML tags
outside the structured summary (following pi's format):

```xml
<read-files>
path/to/file1.go
path/to/file2.go
</read-files>

<modified-files>
path/to/edited.go
path/to/created.go
</modified-files>
```

These XML tags are machine-parseable and carried forward through subsequent
compactions. They are separate from the summary body so the LLM doesn't need
to maintain them — the compaction code manages them programmatically.

**Max tokens for summary response**: `0.8 * ReserveTokens`. For the default
8192 reserve, the summary can be at most ~6500 tokens. This prevents the
summary itself from filling the context window.

**Custom instructions**: The caller can provide a `SummarizationFocus` string
(e.g., "focus on which spec requirements were completed") that is appended
to the prompt as `"Additional focus: {instructions}"`. This lets embedders
like HELIX influence summary content.

**Reasoning effort**: If the provider/model supports reasoning levels, use
high effort for summarization (better summaries justify the cost).

### Update Mode

When a previous compaction summary exists, the prompt wraps both the
conversation and previous summary in XML tags:

```
<conversation>
{serialized NEW conversation since last compaction}
</conversation>

<previous-summary>
{previous compaction summary}
</previous-summary>

The messages above are NEW conversation since the last compaction.
Update the existing summary by merging new information.

RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context
- UPDATE Progress: move completed items from In Progress to Done
- UPDATE Next Steps based on what was accomplished
- PRESERVE exact file paths and error messages
- If something is no longer relevant, you may remove it
- UPDATE Files section with any new reads or modifications
```

**Previous compaction entries are excluded from the messages-to-summarize.**
The previous summary text is passed separately via `<previous-summary>` tags,
not re-serialized as conversation messages (following pi's
`getMessageFromEntryForCompaction` pattern).

### Summary Injection

The summary replaces all compacted messages as a user-role message:

```
The conversation history before this point was compacted into the
following summary:

<summary>
{structured summary}
</summary>
```

### File Tracking

Like pi, agent tracks which files were read and modified across compactions.
The file lists are appended to the summary in XML tags and carried forward
through subsequent compactions.

### Prompt Cache Preservation

Both Anthropic and OpenAI support prefix-based prompt caching — the provider
caches the tokenized prefix of the conversation, and subsequent requests that
share the same prefix get a cache hit (faster, cheaper). Compaction destroys
the prefix, but we can minimize the damage.

**Post-compaction message ordering** — two variants depending on timing:

**Pre-iteration compaction** (between turns):
```
1. System prompt                    ← injected by loop on next Chat() call (not in history)
2. Recent user messages (preserved) ← real user messages, truncated to budget
3. Compaction summary (user msg)    ← LAST in history
```
The system prompt is always first and never changes — maximum cache prefix
stability. The summary is last because it's the most recent context for the
model. On the next turn, the loop reinjects system context fresh.

**Mid-turn compaction** (during tool call rounds):
```
1. System prompt                    ← stable prefix
2. Initial context (reinjected)     ← permissions, personality, developer instructions
3. Recent user messages (preserved) ← real user messages, truncated to budget
4. Compaction summary (user msg)    ← LAST (model trained to see summary last — per Codex)
```
Mid-turn compaction must inject initial context into the replacement history
because the loop won't reinject it naturally. Context is placed before the
last real user message (following Codex's
`insert_initial_context_before_last_real_user_or_summary` pattern).

**Key rules** (learned from Codex):

1. **Trim from the front when compaction overflows.** If the compaction
   prompt itself exceeds the context window, trim the oldest messages from
   the summarization input (not the newest). This preserves the prefix
   cache for the summarization call itself.

2. **System prompt reinjection.** After compaction replaces the history,
   the system prompt must remain at position 0. Two strategies:
   - **Pre-iteration compaction**: Replace history with
     `[summary, recent_messages]`. The system prompt is injected by the
     agent loop as usual on the next `Chat()` call — it's not part of
     the history.
   - **Mid-iteration compaction**: The loop is already mid-conversation.
     Inject system context above the last user message in the replacement
     history so the model sees it in the expected position.

3. **Invalidate provider-side cache after compaction.** The conversation
   prefix has fundamentally changed. If the provider uses sticky sessions
   or incremental request tracking, signal that the window changed.
   DDX Agent exposes this via `EventCompactionEnd` — providers that maintain
   session state should listen for it.

### Token Counting — Cache-Aware

Pi counts `cacheRead` and `cacheWrite` tokens in its usage calculation:
```
totalTokens = input + output + cacheRead + cacheWrite
```

DDX Agent should do the same. The `TokenUsage` type already has `Input` and
`Output` — add `CacheRead` and `CacheWrite` fields so compaction triggers
account for the full context footprint, not just the billed tokens.

```go
type TokenUsage struct {
    Input      int `json:"input"`
    Output     int `json:"output"`
    CacheRead  int `json:"cache_read,omitempty"`
    CacheWrite int `json:"cache_write,omitempty"`
    Total      int `json:"total"`
}
```

For compaction trigger purposes:
```
effectiveTokens = usage.Input + usage.CacheRead
```

The `CacheRead` tokens represent context the model processed from cache —
they still count against the context window even though they're cheaper.

### Token Counting

Three approaches, in preference order:
1. **Provider-reported usage**: From the last `Response.Usage` — most accurate
2. **Chars/4 heuristic**: For messages added since last response — conservative
3. **Configured context window**: From `CompactionConfig.ContextWindow` or
   provider metadata

### Events

New event types:
```go
EventCompactionStart EventType = "compaction.start"
EventCompactionEnd   EventType = "compaction.end"
```

The `compaction.end` event data includes the summary text, tokens before/after,
and file lists.

### Split Turn Handling

Following pi: if the cut point falls in the middle of a multi-message turn
(e.g., between a user message and its assistant response with tool calls),
generate a separate **turn prefix summary** with a smaller token budget
(`0.5 * ReserveTokens`). Both summaries are generated **concurrently**
(following pi's `Promise.all` pattern — in Go, use goroutines + errgroup).
The turn prefix summary is appended to the main compaction summary as:

```
---

**Turn Context (split turn):**

## Original Request
[What the user asked]

## Early Progress
[Work done in the prefix]

## Context for Suffix
[Info needed to understand the kept suffix]
```

### Quality Degradation Warning

Following Codex: after every compaction, emit a warning event:

```
"Long conversations and multiple compactions can cause the model to be
less accurate. Consider starting a new session when possible."
```

This is emitted via `EventCallback` as an `EventCompactionEnd` with a
`warning` field, not printed to stderr (library, not CLI concern).

### Graceful Degradation

If the compaction prompt itself exceeds the context window:
1. Trim oldest messages from the summarization input **(from the front,
   to preserve prefix cache)** — this is the local/inline compaction path
2. If still too large, fall back to aggressive truncation (keep only the
   most recent messages, drop the summarization attempt)
3. Log a warning via callback

If the summarization LLM returns an empty response, use the fallback string
`"(no summary available)"` (following Codex) rather than leaving the summary
empty. This ensures downstream code always has a non-empty summary to work with.

## Implementation Plan

| # | Bead | Depends |
|---|------|---------|
| 1 | Token estimation (chars/4 + provider usage, cache-aware) | — |
| 2 | Conversation serialization for summarization | — |
| 3 | Compaction config types and trigger logic | 1 |
| 4 | Summarization prompt and summary injection (cache-optimized ordering) | 2, 3 |
| 5 | File tracking across compactions | 4 |
| 6 | Mid-turn compaction in agent loop (with system context reinjection) | 4 |
| 7 | Update mode (merge with previous summary) + split turn handling | 4 |
| 8 | Integration test: multi-round task with compaction | 6 |

## Design Decisions Not Taken

- **Remote server-side compaction** (Codex has this for OpenAI's `/responses/compact`
  endpoint). Not included — agent is provider-agnostic and shouldn't depend on
  one provider's server-side API. If OpenAI or others expose this, it can be added
  as a provider-specific optimization later.
- **Branch summarization** (pi has this for conversation tree navigation). Not
  included — agent is headless with linear conversations, no branching.
- **Ghost snapshots / undo** (Codex preserves these across compaction). Not
  included — agent has no undo mechanism. If added later, the compaction should
  preserve snapshot items similarly to Codex.

## Risks

| Risk | Prob | Impact | Mitigation |
|------|------|--------|------------|
| Local model produces poor summaries | M | H | Allow dedicated summarization model; structured format constrains output |
| Token estimation inaccurate | M | M | Conservative estimate (chars/4 overestimates); triggers early rather than late |
| Multiple compactions degrade quality | M | M | Warn after compaction; update mode preserves prior summary content |
| Summarization adds latency | L | M | Use faster model for summarization; only triggers when needed |
| Compaction destroys prompt cache | H | M | Cache-optimized ordering (system prompt first, summary second); accepted cost |
| Cache token accounting wrong | M | M | Include CacheRead in effective token count; use provider-reported usage |
| Split turn summary inaccurate | L | L | Smaller token budget; separate focused prompt |
