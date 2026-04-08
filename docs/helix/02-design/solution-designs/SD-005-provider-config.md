---
ddx:
  id: SD-005
  depends_on:
    - FEAT-003
    - FEAT-004
    - FEAT-006
    - SD-001
---
# Solution Design: SD-005 — Provider Registry, Model Catalog, and Backend Pools

## Problem

DDX Agent started with a single flat provider config (`provider`, `base_url`,
`api_key`, `model`). That is sufficient for one local LM Studio instance, but
real users need three separate concerns:

1. **Named providers** — concrete backend definitions for Anthropic,
   OpenRouter, LM Studio hosts, etc.
2. **Shared model policy** — one agent-owned catalog for aliases,
   tiers/profiles, canonical targets, and deprecations.
3. **Simple routing across equivalent backends** — for example rotate among
   several local inference servers that should all serve the same logical model
   reference.

Prompt presets already exist in agent and must remain a separate concern for
system prompt behavior only.

## Design: Three-Layer Resolution Model

DDX Agent keeps three layers above the runtime boundary:

- **Providers** — transport/auth definitions and optional direct pinned models
- **Model catalog** — agent-owned reusable policy/data loaded from an embedded
  snapshot plus an optional external manifest override
- **Backend pools** — routing targets that pick one provider before a run and
  optionally attach a catalog model reference

After resolution, agent still builds exactly one concrete `Provider` and passes
it to `agent.Run()`.

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
    model: qwen/qwen3-coder-next          # optional direct pin / fallback

  bragi:
    type: openai-compat
    base_url: http://bragi:1234/v1
    api_key: lmstudio
    model: qwen/qwen3-coder-next

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

backends:
  code-fast-local:
    model_ref: code-fast
    providers: [vidar, bragi]
    strategy: round-robin

  review-smart:
    model_ref: code-smart
    providers: [anthropic, openrouter]
    strategy: first-available

default: vidar
default_backend: code-fast-local
preset: agent
max_iterations: 20
session_log_dir: .agent/sessions
```

### Resolution Model

1. Load provider config and the agent model catalog.
2. If `--backend` is provided, resolve that backend pool.
3. Else if `default_backend` exists, resolve that backend pool.
4. Else fall back to direct provider selection via `--provider` or `default`.
5. If a backend or explicit `--model-ref` is used, resolve that reference
   through the catalog for the requested consumer surface.
6. If `--model` is provided, treat it as an explicit concrete pin and bypass
   catalog policy for that run.
7. Build exactly one provider with one concrete model string and pass it to
   `agent.Run()`.

This preserves the current architecture while making model policy reusable and
terminology-safe.

## Key Design Decisions

**D1: Keep named providers as the concrete transport unit.** Providers hold
endpoint URLs, credentials, and headers. They are not the canonical source of
alias/profile policy.

**D2: Add a agent-owned model catalog as a first-class layer.** The catalog is
loaded from an embedded manifest snapshot with an optional external override,
and it owns aliases, tiers/profiles, canonical targets, and deprecations.

**D3: Preserve prompt preset terminology for prompts only.** The top-level
`preset` field and CLI `--preset` flag refer to system prompt presets defined in
SD-003. Model policy uses `model_ref`, `alias`, `profile`, or `catalog`, never
`preset`.

**D4: Backend pools resolve providers, not policy.** A backend pool selects one
provider reference and one model reference before the run. It does not replace
the catalog.

**D5: Phase 2A uses simple pre-run selection only.** Supported strategies are:
- `round-robin` — rotate between configured providers per request
- `first-available` — always use the first configured provider

Phase 2A does **not** retry a failed request on another provider.

**D6: Direct concrete model pins remain supported.** `--model` and provider
defaults remain valid for exact control, imports, and back-compat, but catalog
references are the preferred shared-policy surface.

**D7: Environment variable expansion still applies to values.** `${VAR}` is
expanded at config load time. No shell evaluation.

**D8: Backwards compatible with the legacy flat format.** Old flat config still
maps to a single provider named `default`. Users can adopt catalog references
and backend pools only when they need them.

## CLI UX

### Prompt Preset Selection

```bash
ddx-agent -p "prompt" --preset agent
ddx-agent -p "prompt" --preset claude
```

Built-in preset names are defined by SD-003 and the implementation in
`prompt/presets.go`.

### Direct Provider / Model Selection

```bash
ddx-agent -p "prompt" --provider vidar
ddx-agent -p "prompt" --provider anthropic --model claude-sonnet-4-20250514
ddx-agent -p "prompt" --model-ref code-smart
```

### Backend-Pool Selection

```bash
ddx-agent -p "prompt" --backend code-fast-local
ddx-agent -p "prompt"                         # use default_backend if set, else default provider
```

Initial phase-2A scope only requires backend resolution for runs. Provider
listing, checking, and model listing remain provider-oriented commands. Catalog
inspection commands can be added later if needed.

## Library and Package Boundaries

The library runtime boundary does not change: `agent.Run()` still takes a
single `Provider` in the `Request`.

Config and CLI code grow a catalog-aware layer above that boundary. The
detailed package/API shape is defined in
`docs/helix/02-design/plan-2026-04-08-shared-model-catalog.md`.

Expected package split:

- `config/` — load provider config and optional manifest override path
- `modelcatalog/` — load, validate, and resolve shared model policy
- `cmd/ddx-agent/` — resolve `--provider`, `--backend`, `--model-ref`, or `--model`
  into one concrete provider/model pair

## Traceability

- FEAT-004 defines the ownership split and terminology
- SD-003 reserves `preset` for system prompt behavior
- `plan-2026-04-08-shared-model-catalog.md` defines the catalog package/API,
  manifest format, and consumer examples
- `agent-94b5d420` covers the converged design
- `agent-66eef6fe` is the follow-on implementation bead
