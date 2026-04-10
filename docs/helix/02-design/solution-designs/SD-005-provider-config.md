---
ddx:
  id: SD-005
  depends_on:
    - FEAT-003
    - FEAT-004
    - FEAT-006
    - SD-001
---
# Solution Design: SD-005 — Provider Registry, Model Catalog, and Model-First Routing

## Problem

DDX Agent started with a single flat provider config (`provider`, `base_url`,
`api_key`, `model`). That is sufficient for one local LM Studio instance, but
real users need three separate concerns:

1. **Named providers** — concrete backend definitions for Anthropic,
   OpenRouter, LM Studio hosts, etc.
2. **Shared model policy** — one agent-owned catalog for aliases,
   tiers/profiles, canonical targets, and deprecations.
3. **Simple routing across equivalent providers** — for example choose among
   several local inference servers that should all serve the same requested
   model.

Prompt presets already exist in agent and must remain a separate concern for
system prompt behavior only.

## Design: Three-Layer Resolution Model

DDX Agent keeps three layers above the runtime boundary:

- **Providers** — transport/auth definitions and optional direct pinned models
- **Model catalog** — agent-owned reusable policy/data loaded from an embedded
  snapshot plus an optional external manifest override
- **Model routes** — routing entries keyed by requested model or canonical
  target that pick one provider candidate before a run

After resolution, agent still builds exactly one concrete `Provider` and passes
it to `agent.Run()`.

DDx boundary:

- DDx chooses the harness and passes model intent to the embedded harness.
- Embedded `ddx-agent` chooses the concrete provider candidate.
- DDx records attribution facts from the embedded run, but does not own or
  inspect provider candidate tables.

### Config Format

```yaml
# .agent/config.yaml
model_catalog:
  manifest: ~/.config/agent/models.yaml   # optional local override of the embedded snapshot

providers:
  vidar:
    type: openai-compat
    base_url: http://vidar:1234/v1
    api_key: lmstudio

  bragi:
    type: openai-compat
    base_url: http://bragi:1234/v1
    api_key: lmstudio

  grendel:
    type: openai-compat
    base_url: http://grendel:1234/v1
    api_key: lmstudio

  openrouter:
    type: openai-compat
    base_url: https://openrouter.ai/api/v1
    api_key: ${OPENROUTER_API_KEY}
    headers:
      HTTP-Referer: https://github.com/DocumentDrivenDX/agent
      X-Title: DDX Agent

  anthropic:
    type: anthropic
    api_key: ${ANTHROPIC_API_KEY}

routing:
  default_model_ref: code-fast
  health_cooldown: 30s

model_routes:
  qwen3-coder-next:
    strategy: priority-round-robin
    candidates:
      - provider: vidar
        model: qwen/qwen3-coder-next
        priority: 100
      - provider: bragi
        model: qwen/qwen3-coder-next
        priority: 100
      - provider: grendel
        model: qwen/qwen3-coder-next
        priority: 100
      - provider: openrouter
        model: qwen/qwen3-coder-next
        priority: 10

  claude-sonnet-4:
    strategy: ordered-failover
    candidates:
      - provider: anthropic
        model: claude-sonnet-4-20250514
        priority: 100
      - provider: openrouter
        model: anthropic/claude-sonnet-4
        priority: 50

default: vidar
preset: agent
max_iterations: 20
session_log_dir: .agent/sessions
```

### Resolution Model

1. Load provider config and the agent model catalog.
2. If `--provider` is provided, build that provider directly.
3. Else if `--model` is provided, treat it as the requested model key and
   resolve a model route for it.
4. Else if `--model-ref` is provided, resolve it through the catalog and then
   resolve the corresponding model route.
5. Else if `routing.default_model_ref` or `routing.default_model` exists, use
   that route.
6. Else fall back to direct provider selection via `default`.
7. Build exactly one provider with one concrete model string and pass it to
   `agent.Run()`.

This preserves the current architecture while making model policy reusable and
terminology-safe.

## Key Design Decisions

**D1: Keep named providers as the concrete transport unit.** Providers hold
endpoint URLs, credentials, and headers. They are not the canonical source of
alias/profile policy.

**D2: Add an agent-owned model catalog as a first-class layer.** The catalog is
loaded from an embedded manifest snapshot with an optional external override,
and it owns aliases, tiers/profiles, canonical targets, and deprecations.

**D3: Preserve prompt preset terminology for prompts only.** The top-level
`preset` field and CLI `--preset` flag refer to system prompt presets defined in
SD-003. Model policy uses `model_ref`, `alias`, `profile`, or `catalog`, never
`preset`.

**D4: Model routes resolve providers, not policy.** A model route selects one
provider candidate and one concrete model string before the run. It does not
replace the catalog.

**D5: Model-first routing is the public surface.** Users ask for `--model` or
`--model-ref`; they should not have to invent arbitrary backend labels.

**D6: Passive availability and bounded failover are additive.** Supported
selection strategies are:
- `priority-round-robin` — use the highest-priority healthy tier and rotate
  within that tier
- `ordered-failover` — walk candidates in priority/order when the current one
  is unavailable

Failover applies only to provider-side availability failures.

**D7: Direct concrete model pins remain supported.** `--model` and provider
defaults remain valid for exact control, imports, and back-compat, but catalog
references are the preferred shared-policy surface.

**D8: Environment variable expansion still applies to values.** `${VAR}` is
expanded at config load time. No shell evaluation.

**D9: Backwards compatible with the legacy flat format and backend pools.** Old
flat config still maps to a single provider named `default`. Existing
`backends`/`default_backend` config is translated into internal model routes
during migration and emits a deprecation warning.

## CLI UX

### Prompt Preset Selection

The `--preset` flag (or `preset` in config) selects the system prompt style.
Built-in preset names:

| Preset    | Description                                              |
|-----------|----------------------------------------------------------|
| `agent`   | DDX Agent default — balanced, tool-aware, structured     |
| `minimal` | Bare minimum — one sentence, like pi                     |
| `claude`  | Tracks Claude Code style — thorough, safety-conscious    |
| `codex`   | Tracks OpenAI Codex CLI style — pragmatic, direct        |
| `cursor`  | Tracks Cursor style — fast, action-oriented, edit-heavy  |

```bash
ddx-agent -p "prompt"                  # uses preset from config, or "agent" by default
ddx-agent -p "prompt" --preset agent
ddx-agent -p "prompt" --preset claude
ddx-agent -p "prompt" --preset codex
```

The `preset` field may also be set in `.agent/config.yaml`:

```yaml
preset: claude
```

Built-in preset details are defined by SD-003 and implemented in
`prompt/presets.go`.

### Direct Provider / Model Selection

```bash
ddx-agent run --provider vidar "prompt"
ddx-agent run --provider anthropic --model claude-sonnet-4-20250514 "prompt"
ddx-agent run --model-ref code-smart "prompt"
```

### Model-Route Selection

```bash
ddx-agent run --model qwen3-coder-next "prompt"
ddx-agent run --model-ref code-fast "prompt"
ddx-agent run "prompt"                        # use default model route if set, else default provider
```

Compatibility:

```bash
ddx-agent -p "prompt" --backend code-fast-local
```

The compatibility flag remains temporarily, but it is not the preferred UX.

## Library and Package Boundaries

The library runtime boundary does not change: `agent.Run()` still takes a
single `Provider` in the `Request`.

Config and CLI code grow a catalog-aware layer above that boundary. The
detailed package/API shape is defined in
`docs/helix/02-design/plan-2026-04-08-shared-model-catalog.md` and
`docs/helix/02-design/plan-2026-04-10-model-first-routing.md`.

Expected package split:

- `config/` — load provider config, route config, and optional manifest
  override path
- `modelcatalog/` — load, validate, and resolve shared model policy
- `cmd/ddx-agent/` — resolve `--provider`, `--model-ref`, or `--model` into
  one concrete provider/model pair

## Traceability

- FEAT-004 defines the ownership split and terminology
- SD-003 reserves `preset` for system prompt behavior
- `plan-2026-04-08-shared-model-catalog.md` defines the catalog package/API,
  manifest format, and consumer examples
- `plan-2026-04-10-model-first-routing.md` captures the converged replacement
  of backend pools with model routes
- `agent-94b5d420` covers the shared-catalog design lineage
