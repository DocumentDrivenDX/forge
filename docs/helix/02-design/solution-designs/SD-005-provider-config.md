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
   tiers/profiles, canonical policy targets, per-surface projections, and
   deprecations.
3. **Simple routing across equivalent providers** — for example choose among
   several local inference servers that should all serve the same requested
   model.

Prompt presets already exist in agent and must remain a separate concern for
system prompt behavior only.

## Design: Three-Layer Resolution Model

DDX Agent keeps three layers above the runtime boundary:

- **Providers** — transport/auth definitions and optional direct pinned models
- **Model catalog** — agent-owned reusable policy/data loaded from an embedded
  snapshot plus an optional external manifest override, with published manifest
  bundles distributed outside binary releases
- **Model routes** — routing entries keyed by requested model or canonical
  target that pick one provider candidate before a run

After resolution, agent still builds exactly one concrete `Provider` and passes
it to `agent.Run()`.

Caller boundary (see CONTRACT-003):

- Callers choose the harness and pass model intent to the embedded harness.
- Embedded `ddx-agent` chooses the concrete provider candidate.
- Callers receive attribution facts from the embedded run, but do not own or
  inspect provider candidate tables.

### Config Format

```yaml
# .agent/config.yaml
model_catalog:
  manifest: ~/.config/ddx-agent/models.yaml   # optional local override of the embedded snapshot

providers:
  vidar:
    type: openai-compat
    base_url: http://vidar:1234/v1
    api_key: lmstudio
    reasoning: off
    flavor: lmstudio

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

  vidar-omlx:
    type: openai-compat
    base_url: http://vidar:1235/v1
    model: Qwen3.5-27B-4bit
    reasoning: off
    flavor: omlx

routing:
  default_model_ref: code-medium
  health_cooldown: 30s

model_routes:
  code-medium:
    strategy: priority-round-robin
    candidates:
      - provider: vidar
        model: gpt-5.4-mini
        reasoning_default: off
        priority: 100
      - provider: bragi
        model: gpt-5.4-mini
        reasoning_default: off
        priority: 100
      - provider: grendel
        model: gpt-5.4-mini
        reasoning_default: off
        priority: 100
      - provider: openrouter
        model: gpt-5.4-mini
        reasoning_default: off
        priority: 10

  code-high:
    strategy: ordered-failover
    candidates:
      - provider: anthropic
        model: opus-4.6
        reasoning_default: high
        priority: 100
      - provider: openrouter
        model: gpt-5.4
        reasoning_default: high
        priority: 50

default: vidar
preset: agent
max_iterations: 20
session_log_dir: .agent/sessions
```

#### Provider Config Fields

Per-provider optional fields (in addition to `type`, `base_url`, `api_key`, `headers`, `model`):

| Field | Type | Description |
|---|---|---|
| `reasoning` | scalar string/int | Single public reasoning control: `auto`, `off`, `low`, `medium`, `high`, supported extended values such as `minimal`, `xhigh` / `x-high`, and `max`, or numeric values such as `0`, `2048`, and `8192` |
| `max_tokens` | int | Max output tokens per turn; `0` = use provider default |
| `context_window` | int | Explicit context window override; `0` = attempt live discovery |
| `flavor` | string | Server flavor hint: `lmstudio`, `omlx`, `openrouter`, `ollama` |

Older split provider config names are rejected with a clear error. Provider-
specific wire terms such as `thinking`, `effort`, `variant`, and token budgets
are adapter implementation details, not public config.

#### Reasoning Values

`reasoning` is intentionally one scalar rather than separate public level and
budget fields.

- Empty or unset means no caller preference.
- `auto` means resolve model, catalog, or provider defaults.
- `off`, `none`, `false`, and numeric `0` mean explicit reasoning off.
- `low`, `medium`, and `high` use portable fallback budgets of 2048, 8192, and
  32768 tokens when provider/catalog metadata does not publish a better map.
- Extended names such as `minimal`, `xhigh`, `x-high`, and `max` are accepted
  only when the selected provider or harness advertises support. `x-high`
  normalizes to `xhigh`; explicit extended requests are never silently
  downgraded.
- Positive integers mean an explicit max reasoning-token budget, or a
  documented provider-equivalent numeric value.

Providers that only accept numeric reasoning controls must map named values to
numeric budgets with capability-aware model metadata and must enforce
model-specific maximum reasoning-token limits. `max` resolves at the provider
or harness boundary to the selected model/provider maximum and is accepted only
when that maximum is known. Auto/default reasoning controls may be dropped for
unsupported providers/models, but explicit unsupported or over-limit values
fail clearly.

The Go public surface should expose the same single scalar as a typed value,
for example `type Reasoning string` with constants and
`ReasoningTokens(n int) Reasoning`. The implementation should put parsing,
normalization, constants, and policy representation in a shared leaf package
such as `internal/reasoning`; root `agent` may re-export the public type and
helpers, while `internal/modelcatalog` imports the leaf package directly to
avoid root-agent/internal-modelcatalog import cycles.

Model catalog metadata uses `reasoning_default`. Below-smart tiers (`cheap`,
`fast`, `standard`, `code-economy`, and `code-medium`) default to
`reasoning=off`, including local/economy Qwen
targets such as Qwen3.6. `smart` and `code-high` default to `reasoning=high`.
Explicit caller values always win when supported, including numeric values and
values above high such as `xhigh`, `x-high`, or `max`.

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
and it owns aliases, tiers/profiles, canonical policy targets, deprecations,
and per-surface projections.

**D2A: Publish catalog bundles independently of binary releases.** The embedded
snapshot remains the safe default, but operators and callers can install a newer
shared manifest from a versioned published bundle via an explicit update flow.

**D2B: Manifest v4 separates concrete models from tier policy.** The model
catalog manifest uses top-level `models` entries for concrete model metadata:
family, display name, parent tier, status, per-million-token costs, cache costs,
context window, benchmark metadata, OpenRouter ID, reasoning budget metadata,
and consumer surface strings. Target entries define only policy: family,
aliases, status/replacement metadata, `context_window_min`, `swe_bench_min`,
ordered `candidates`, and per-surface `reasoning_default`. Older v3 manifests
remain loadable by synthesizing model entries from target surface mappings at
load time.

```yaml
version: 4
models:
  qwen3.5-27b:
    family: qwen
    display_name: Qwen3.5 27B
    tier: code-economy
    status: active
    cost_input_per_m: 0.10
    cost_output_per_m: 0.30
    context_window: 262144
    surfaces:
      agent.openai: qwen3.5-27b
targets:
  code-economy:
    family: coding-tier
    status: active
    context_window_min: 131072
    candidates: [qwen3.5-27b]
    surface_policy:
      agent.openai:
        reasoning_default: off
```

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

**D10: Provider limit discovery is live and flavor-gated.** When
`context_window` or `max_tokens` are zero, the CLI calls `LookupModelLimits`
against the provider's API to discover them. Explicit config values always win.
Discovery is keyed by server flavor:

- **LM Studio** — `GET /api/v0/models/{model}`; prefers `loaded_context_length`
- **omlx** — `GET /v1/models/status`; returns `max_context_window` and
  `max_tokens` per model
- **OpenRouter** — `GET /api/v1/models` (public list)

Undiscoverable values stay zero and the compaction layer uses its own defaults.

**D11: Flavor field replaces fragile port heuristics.** Port-based provider
detection (e.g. 1234 = lmstudio, 1235 = omlx) fails when servers run on
non-default ports (omlx defaults to 8000). The `flavor` field lets operators
declare the server type explicitly. When flavor is absent the system:

1. Tries URL-based detection first (reliable for `openrouter.ai`, ollama on
   11434, etc.)
2. Fires concurrent probes to `/v1/models/status` and `/api/v0/models` with a
   3-second timeout to distinguish omlx vs LM Studio on ambiguous ports
3. Falls back to port heuristics as a last resort

**D12: omlx is a first-class supported provider.** omlx is a local inference
runtime that speaks the OpenAI-compatible chat API and exposes additional
endpoints: `GET /v1/models/status` returns per-model `max_context_window` and
`max_tokens`. Set `flavor: omlx` to use dedicated limit discovery and avoid
probe ambiguity. See the `vidar-omlx` provider entry in the config example
above.

**D13: Protocol capabilities are flavor-keyed and conservative.** The provider
exposes `SupportsTools()`, `SupportsStream()`, and `SupportsStructuredOutput()`
accessors that return the effective capability for the resolved flavor.
Downstream routing consults these before dispatch to avoid dispatch-and-fail on
mismatched prompts (e.g. 80k-token prompt against a 32k-context model, or
tool-using prompt against a flavor without tool translation). Unknown flavors
return `false` for all protocol flags so routing rejects rather than dispatches.
This surface is distinct from the benchmark-based capability scoring used by
smart-routing (`CapabilityScore` / `CapabilityWeight`); the two axes do not
interact.

**D14: `DetectedFlavor()` layers on top of `providerSystem` without replacing
it.** `providerSystem` (URL-heuristic, eager, non-blocking) remains the source
of truth for per-response telemetry and cost attribution because those fire on
every response and cannot afford a network probe. `DetectedFlavor()` is the
probe-confirmed accessor used for pre-dispatch gating (capability flags,
routing tags, introspection). It runs the probe at most once per provider via
`sync.Once`, caches the result, and falls back to `providerSystem` when the
probe is inconclusive. The two accessors serve different audiences by design;
callers of telemetry must not migrate to `DetectedFlavor()` without a
CONTRACT-001 review.

**D15: `reasoning` is the public model-reasoning control.** The public surface
uses one scalar (`reasoning`) for named and numeric values. Config uses
`reasoning`; catalog metadata uses `reasoning_default`; the CLI uses
`--reasoning`. Provider and harness adapters may translate the resolved value
to wire or subprocess knobs named `thinking`, `effort`, `variant`, or numeric
budgets, but those names are not preferred public controls. Unsupported
auto/default controls may be dropped; explicit unsupported or over-limit
values fail clearly.

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
ddx-agent run --provider anthropic --model opus-4.6 "prompt"
ddx-agent run --model-ref code-high "prompt"
ddx-agent run --model-ref code-high --reasoning max "prompt"
ddx-agent run --model-ref code-medium --reasoning off "prompt"
ddx-agent run --provider vidar --reasoning 8192 "prompt"
```

The public CLI flag is `--reasoning <value>`. Do not introduce alternate public
reasoning flags.

### Model-Route Selection

```bash
ddx-agent run --model qwen3.5-27b "prompt"
ddx-agent run --model-ref code-medium "prompt"
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
`docs/helix/02-design/plan-2026-04-08-shared-model-catalog.md`,
`docs/helix/02-design/plan-2026-04-10-model-first-routing.md`, and
`docs/helix/02-design/plan-2026-04-10-catalog-distribution-and-refresh.md`.

Expected package split:

- `internal/config/` — load provider config, route config, and optional
  manifest override path in the current repository layout
- `internal/modelcatalog/` — load, validate, and resolve shared model policy
- `internal/reasoning/` — shared leaf package for the Reasoning scalar,
  parser, normalization, constants, `ReasoningTokens(n)`, and resolved policy
  representation
- `cmd/ddx-agent/` — resolve `--provider`, `--model-ref`, or `--model` into
  one concrete provider/model/reasoning policy

## Traceability

- FEAT-004 defines the ownership split and terminology
- SD-003 reserves `preset` for system prompt behavior
- `plan-2026-04-08-shared-model-catalog.md` defines the catalog package/API,
  manifest format, and consumer examples
- `plan-2026-04-10-model-first-routing.md` captures the converged replacement
- `plan-2026-04-10-catalog-distribution-and-refresh.md` defines published
  manifest bundles, explicit update flow, and the initial reasoning-tier
  baseline of backend pools with model routes
- `agent-94b5d420` covers the shared-catalog design lineage
- D10–D12 (provider limit discovery, flavor detection, omlx support) are
  implemented in `internal/config/config.go`, `internal/provider/openai`, and
  the `LookupModelLimits` call-site in the CLI layer
- D15 (reasoning contract) is implemented through `reasoning`,
  `reasoning_default`, and CLI `--reasoning`
