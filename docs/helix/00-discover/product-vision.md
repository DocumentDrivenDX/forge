# Product Vision

## Mission Statement

DDX Agent is an embeddable Go agent runtime that gives build orchestrators like
DDx/HELIX a native, in-process coding agent — optimized for local models via
LM Studio, cost-efficient use of cloud models, and the tight feedback loops
that specification-driven development demands.

## Positioning

For **build-loop orchestrators and CI systems** that need an AI coding agent
without the overhead of shelling out to standalone CLIs,
**DDX Agent** is an **embeddable agent runtime library** that provides a
pi-style tool-calling agent loop as a Go package.
Unlike Claude Code, Codex, pi, or Aider — which are standalone processes
invoked via subprocess — DDX Agent runs in-process with direct access to the
host's state, native multi-provider support (LM Studio, Ollama, cloud APIs),
and model routing that picks the cheapest capable model for each task.

## Vision

Build orchestrators dispatch agent work as naturally as they dispatch shell
commands — no process boundaries, no output parsing, no wasted tokens
re-establishing context. Local models handle routine tasks at zero marginal
cost. Cloud models engage only when local capability is insufficient.
Every agent invocation is logged, metered, and auditable by the host.

**North Star**: A HELIX build pass that completes a bead using a local 7B
model for scaffolding and a cloud model only for the final review — at 10%
of the cost of a full-cloud run, with no loss in quality. Every LLM
interaction and tool call is logged, replayable, and cost-tracked.

## Design Philosophy

DDX Agent follows the ghostty model: build a great library, then prove it works
with a usable standalone app. The library (`ddx-agent` Go package) is the product.
The CLI (`ddx-agent` binary) is the showcase — a thin porcelain that demonstrates
the library works end-to-end and serves as the DDx harness backend.

## User Experience

A developer runs `helix run` on a project with detailed specs. HELIX claims
a bead, reads the acceptance criteria, and calls DDX Agent in-process. DDX Agent
selects a local Qwen 3.5 model via LM Studio for the implementation pass —
reading files, editing code, running tests. When tests pass, DDX Agent switches
to Claude Sonnet for a review pass. The whole cycle takes 90 seconds, costs
$0.02, and the developer sees a clean commit with passing tests. No agent
CLI was spawned. No context was lost between steps.

## Target Market

| Attribute | Description |
|-----------|-------------|
| Who | Developers and teams using DDx/HELIX or similar spec-driven build orchestrators |
| Pain | Shelling out to agent CLIs is slow, lossy, and expensive — process overhead, context re-establishment, cloud-only pricing |
| Current Solution | `ddx agent run --harness=claude` (subprocess), or manual agent CLI invocation |
| Why They Switch | In-process agent with local model support cuts cost 5-10x and eliminates subprocess overhead |

## Key Value Propositions

| Value Proposition | Customer Benefit |
|-------------------|------------------|
| Embeddable Go library — not a CLI | Zero subprocess overhead, direct state sharing with host |
| Local-model-first via LM Studio/Ollama | Zero marginal cost for routine tasks, air-gap capable |
| Pi-style minimal tool set (read/bash/edit/write) | Simple, auditable, no permission complexity |
| Structured I/O (prompt envelope in, result out) | Native DDx integration, no output parsing |
| First-class logging and replay | Every LLM turn and tool call recorded, inspectable, replayable |
| Cost tracking and metering | Per-model pricing, session cost roll-ups, budget awareness |
| Standalone CLI proving the library | Usable app that validates the library works end-to-end |

## Success Definition

| Metric | Target |
|--------|--------|
| Embed in DDx as `ddx agent` backend | Replace subprocess harness for at least one provider |
| Local model task completion | 70%+ of routine bead tasks completed by local 7B+ model |
| Cost per bead (blended) | <$0.05 average across local+cloud |
| Latency overhead vs subprocess | <10ms added per agent call (no process spawn) |

## Why Now

Local models crossed the capability threshold for routine coding tasks in
2025-2026 — Qwen 3.5, Llama 3.x, and GLM-4.7 reliably handle file edits,
test fixes, and scaffolding with tool calling. LM Studio's headless daemon
(`lms daemon up`) makes local inference a stable platform service. Meanwhile,
cloud model costs remain high enough that running every agent task through
Claude or GPT-4 is wasteful when 70% of tasks are mechanical. The DDx agent
service (FEAT-006) already defines the harness abstraction — DDX Agent fills the
gap between "shell out to a CLI" and "run the agent loop natively."
