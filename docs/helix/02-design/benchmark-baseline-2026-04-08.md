---
ddx:
  id: benchmark-baseline-2026-04-08
  bead: agent-1192db7b
  captured: 2026-04-08
  model: anthropic/claude-4.5-haiku-20251001 (via OpenRouter)
  provider: openai-compat (openrouter)
  ddx-agent-version: dev (commit from master, 2026-04-08)
---
# DDX Agent Benchmark Baseline — 2026-04-08

## Purpose

This document captures the first baseline measurement of ddx-agent's task-solving
behavior on a pilot task sample. It is the primary input for calibrating thresholds
in the benchmark evaluation plan (agent-82042311) and for identifying which
tool/harness gaps matter most before Terminal-Bench integration work begins.

---

## Pilot Task Sample

Six representative coding tasks were run against ddx-agent using the `cheap` preset
and `anthropic/claude-4.5-haiku-20251001` via OpenRouter. Each task ran in an isolated
temporary working directory. No context was shared between tasks.

### Task Descriptions

| ID | Type | Description |
|----|------|-------------|
| T1 | Read + describe | Read a Go file, identify package name and function |
| T2 | Rename symbol | Rename a Go function using the edit tool |
| T3 | Add error handling | Add zero-division guard + import to a Go function |
| T4 | Bash refactoring | Rewrite a shell script to remove anti-patterns |
| T5 | Add feature | Add an HTTP endpoint handler to a Go web server |
| T6 | Test + implement | Run existing tests, diagnose failure, add new test case |

---

## Results Summary

| Task | Status | Tool Calls | Bash Calls | Edit Calls | Clarification Qs | Wall Time (s) | Input Tokens | Output Tokens |
|------|--------|-----------|-----------|-----------|------------------|--------------|-------------|--------------|
| T1   | ✅ pass | 1         | 0         | 0         | 0                | 5.9          | 3,997       | 97           |
| T2   | ✅ pass | 2         | 0         | 1         | 0                | 10.1         | 6,276       | 258          |
| T3   | ✅ pass | 3         | 0         | 1         | 0                | 7.3          | 9,033       | 515          |
| T4   | ✅ pass | 2         | 0         | 0         | 0                | 12.9         | 6,399       | 438          |
| T5   | ✅ pass | 3         | 0         | 1         | 0                | 9.4          | 9,185       | 533          |
| T6   | ✅ pass | 10        | 4         | 1 (fail)  | 0                | 26.0         | 34,998      | 1,884        |
| **Total** | **6/6** | **21** | **4** | **4** | **0** | **71.6** | **69,888** | **3,725** |

### Aggregated Metrics

| Metric | Value | Notes |
|--------|-------|-------|
| Resolved-task rate | 6/6 (100%) | Pilot tasks are representative but not adversarial |
| Clarification-question rate | 0/6 (0%) | No tasks required asking the user |
| Shell anti-pattern usage | 2 patterns in T6 | `ls -la` + `find ... \| head` before reading files |
| Structured-edit success rate | 3/4 attempts (75%) | 1 edit tool failure in T6 (wrong format, recovered) |
| Avg wall-clock time | 11.9s | Range 5.9–26.0s |
| Avg input tokens | 11,648 | T6 outlier (35k) due to multi-round with bash |
| Avg output tokens | 621 | |

---

## Failure Modes Observed

### FM-1: duration_ms reporting bug (harness)
**Severity**: Medium — observable in all JSON outputs  
**Description**: The `duration_ms` field in the `--json` output is incorrectly large
(e.g., 5,869,220,250 ms for a 5.9-second task). The actual wall clock time is correct.
The field appears to be reporting nanoseconds instead of milliseconds.  
**Impact**: Any downstream tool consuming `Result.DurationMs` for benchmarking gets
wrong data. Wall clock time from the OS (`time`) is accurate.  
**Downstream bead**: File a separate bug bead.

### FM-2: cost_usd = -1 (harness)
**Severity**: Low for benchmarking — expected behavior  
**Description**: Cost tracking returns `-1` when the model is not in ddx-agent's
pricing table. OpenRouter models and new model versions not yet in the embedded catalog
return `-1` instead of an estimated cost.  
**Impact**: Cost-based success metrics cannot use `Result.CostUSD` for OpenRouter runs.
Use token counts + external pricing table for benchmarking cost.  
**Note**: This is expected and acceptable for benchmark use; the token counts are accurate.

### FM-3: Edit tool format inconsistency (agent behavior)
**Severity**: Medium  
**Description**: In T6, the model attempted to use an `edits[]` array format
(`{"edits": [{"oldText": "...", "newText": "..."}]}`) instead of the actual tool
interface (`{"old_string": "...", "new_string": "..."}`). The tool returned an error.
The model recovered correctly by using the `write` tool on the next turn.  
**Impact**: Increases turn count and token usage on complex tasks. The edit tool
description in the system prompt may not be clear enough about the expected format.  
**Downstream bead**: `agent-4dde1671` (navigation tools / anti-pattern micro-evals)
should include an edit-format micro-eval.

### FM-4: Unnecessary exploratory bash (agent behavior)
**Severity**: Low  
**Description**: In T6, the model used `find . -name "*.py" | head -20` and
`ls -la <dir>` before reading files with the read tool. These are shell anti-patterns
when a read tool is available.  
**Impact**: Adds 2 unnecessary bash calls per complex task. Minor token overhead,
but violates the "structured navigation > bash" convention that good benchmark
performance requires.  
**Pattern**: Anti-pattern appears primarily when the task involves a directory the
model hasn't seen yet. First-task context establishment seems to trigger shell exploration.

### FM-5: No-module bash recovery (agent behavior — positive)
**Severity**: N/A — positive finding  
**Description**: In T6, `go test` failed because there was no `go.mod` file.
The model correctly diagnosed the error and ran `go mod init` to fix it before retrying.  
**Impact**: Good diagnostic recovery capability. No clarification questions asked.

---

## Tool Usage Patterns

### Read Tool
- **Usage**: Consistent, correct. Always reads before editing.
- **Pattern**: Most tasks (3/6) show a post-edit verification re-read. This adds tokens
  but increases confidence in edits.
- **Anti-pattern**: T6 attempted to read a directory path (returned an error). The model
  recovered by reading individual files.

### Edit Tool
- **Usage**: 4 invocations across 3 tasks; 3 succeeded, 1 failed (FM-3).
- **Pattern**: Multi-edit batching observed in T5 (2 edits in one call) — efficient.
- **Anti-pattern**: Wrong input schema attempted once.

### Write Tool
- **Usage**: Used for full-file rewrites (T4 script rewrite, T6 test file).
- **Pattern**: Correct — write used when the change is a full replacement, edit used
  for targeted changes.

### Bash Tool
- **Usage**: 4 calls total, all in T6.
- **Pattern**: 2 legitimate (go test, go mod init); 2 anti-pattern (ls, find).
- **Key gap**: Model uses bash for directory exploration when the read tool is the right choice.

---

## Harness / Runtime Failures

| Type | Count | Description |
|------|-------|-------------|
| Config loading | 0 | Provider config loaded cleanly after setup |
| Tool execution errors | 1 | Edit tool format error in T6 (recovered) |
| Provider errors | 0 | OpenRouter connectivity stable |
| Timeout / cancellation | 0 | No tasks exceeded iteration or time limits |
| Session log | 6 created | JSONL session logs created in `.agent/` in each task dir |

---

## What This Baseline Tells Us

### What works well
1. **Core read/edit/write task pattern**: All 5 single-type tasks (T1–T5) resolved on
   first attempt with correct tool usage.
2. **Error recovery**: Strong. The agent diagnoses and recovers from tool errors and
   environment issues without asking for help.
3. **Multi-edit batching**: T5 correctly batched 2 related edits in one edit tool call.
4. **No clarification leakage**: 0 clarification questions on 6 tasks. The model stays
   on-task even when environment setup is missing (T6: no go.mod).

### What to improve before Terminal-Bench evaluation
1. **Shell anti-pattern rate is non-zero**: T6 showed 2/4 bash calls were anti-patterns.
   A benchmark-mode preset should explicitly discourage `ls`, `find`, and `cat` when
   read/glob tools are available. Track via bead `agent-5f35fdeb`.
2. **Edit tool format confusion**: The cheap preset's tool description needs to be
   unambiguous about `old_string`/`new_string` vs array format. Track via `agent-4dde1671`.
3. **duration_ms bug**: Affects observability of task timing in JSON output. File and fix
   before automated baseline capture (agent-78c86322).
4. **Navigation tools now exist, but benchmark prompts still need steering**: T6 used
   bash for file discovery before the navigation surface was fully exposed. Keep the
   benchmark-mode prompt explicit about preferring structured tools (`glob`, `grep`,
   `ls`, `patch`, `task`) over shell discovery. Track navigation anti-patterns via
   `agent-4dde1671`.

---

## Calibration Notes for Downstream Thresholds

These observations should anchor the thresholds in the benchmark evaluation plan
(agent-82042311) rather than invented numbers:

- **Resolved-task rate ≥ 70%** is achievable for mechanical tasks on haiku-class models.
  The pilot shows 100% on 6 simple tasks; adversarial Terminal-Bench tasks will lower this.
  70% is a reasonable floor for the fixed benchmark subset.
- **Clarification-question rate < 10%** is achievable (0% observed). Terminal-Bench
  tasks don't expect interactive clarification, so this must stay near 0.
- **Shell anti-pattern rate < 20% of bash calls** is the current baseline (50% in T6,
  but T6 was the only task with bash). With a benchmark-mode preset, this should drop
  toward 0 for navigation-type anti-patterns.
- **Structured-edit success rate ≥ 80%** is achievable (75% current without prompt fix).
  With the edit tool description clarified, this should reach 90%+.

---

## Methodology

- **Run date**: 2026-04-08
- **ddx-agent build**: dev/master (commit f3b980c)
- **Model**: `anthropic/claude-4.5-haiku-20251001` via OpenRouter
- **Preset**: `cheap`
- **Working directories**: Isolated temp dirs per task (`/tmp/ddx-bench/taskN`)
- **Invocation**: `ddx-agent --json --preset cheap -p "<prompt>"`
- **Provider config**: OpenRouter via `~/.config/agent/config.yaml`
- **Timing**: Wall clock via `time` (not `duration_ms` in JSON, which has a bug)
- **Token counts**: From `--json` output `.tokens.input` / `.tokens.output`

### Replication

To reproduce this baseline:

```bash
# Configure a provider
mkdir -p ~/.config/agent && cat > ~/.config/agent/config.yaml <<EOF
providers:
  openrouter:
    type: openai-compat
    base_url: https://openrouter.ai/api/v1
    api_key: "${OPENROUTER_API_KEY}"
    model: anthropic/claude-haiku-4-5
    headers:
      HTTP-Referer: https://github.com/DocumentDrivenDX/agent
default_provider: openrouter
EOF

# Run T1 as a smoke test
mkdir -p /tmp/bench-smoke
echo 'package main; import "fmt"; func main() { fmt.Println("hello") }' > /tmp/bench-smoke/main.go
./ddx-agent --json --preset cheap --work-dir /tmp/bench-smoke \
  -p "Read main.go and tell me the package name."
```

Expected: `status: success`, 1 read tool call, correct package name in output.
