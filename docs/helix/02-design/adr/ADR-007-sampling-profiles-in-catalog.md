---
ddx:
  id: ADR-007
  depends_on:
    - ADR-005
    - ADR-006
---
# ADR-007: Sampling Profiles Belong in the Model Catalog

| Date | Status | Deciders | Related | Confidence |
|------|--------|----------|---------|------------|
| 2026-04-27 | Proposed | DDX Agent maintainers | `ADR-005`, `ADR-006` | High |

## Context

The native agent harness sends no sampling fields (`temperature`, `top_p`, `top_k`, `min_p`, `repetition_penalty`) to providers by default. The wire path at `internal/provider/openai/openai.go:227-256` honors any non-nil values on `agent.Options` and silently omits the rest, but the only source feeding those options today is `providers.<name>.sampling` in user config (`cmd/agent/main.go:241-258`). For a fresh install, every request is field-omitted.

Field-omitted means the server picks. On oMLX (vidar, grendel) and on most OpenAI-compat local servers, an omitted `temperature` defaults to **0.0 — greedy / argmax decoding**. This was confirmed empirically against `Qwen3.6-27B-MLX-8bit` on both vidar:1235 and grendel:1235 on 2026-04-27: identical bodies produced bit-identical outputs across two runs, and the server's own "code" preset did not override client values when the client supplied them.

Greedy decoding combined with reasoning-mode Qwen3.x models causes deterministic tool-call loops — the same tool invocation with the same arguments emits repeatedly until the harness's loop-detector aborts. This is the failure mode visible in the 2026-04-27 harness-parity run (`benchmark-results/beadbench/run-20260427T122221Z-1549045/`): four of five local arms failed before producing output, with the one that produced output failing on `agent: identical tool calls repeated, aborting loop`.

The empirical fix is a non-greedy sampler bundle. Qwen3 model cards recommend roughly `T=0.6, top_p=0.95, top_k=20, repetition_penalty=1.05` for code generation. The plumbing to deliver this exists. What is missing is **policy** for where the values come from.

## Decision

### 1. Sampling is catalog policy, not user configuration

Per ADR-006, manual overrides are routing-failure signals. The same logic applies to sampling: a user reaching for `--temperature` or editing `providers.*.sampling` to make a model decode usefully is a signal that the catalog failed to carry the right defaults. The right home for sampler policy is the model catalog, exactly where `reasoning_levels` and `reasoning_control` already live.

### 2. Three resolution layers

Sampling values resolve through a precedence chain. Each layer can set any subset of fields; nil at all layers means the wire field is omitted and the server's own default applies (the "let the server handle it" case is preserved as a first-class outcome).

1. **L1 — Catalog `sampling_profiles`** (manifest top level). Named bundles keyed by use-case: `code`, eventually `creative`, `tool-loop`, `review`. The active profile is selected by the caller; v1 hardcodes `code`.

2. **L2 — Provider config (`providers.*.sampling`)** (existing). Per-(user, provider) override. Already implemented; semantics unchanged.

3. **L3 — CLI flags** (deferred, not in v1). Per-request override.

Higher layers stomp lower layers **per field, not per bundle**. If L1 sets `{T:0.6, top_p:0.95, top_k:20}` and L2 sets `{temperature: 0}`, the resolved bundle is `{T:0, top_p:0.95, top_k:20}`.

A per-`(model, profile)` override layer between L1 and L2 is **explicitly out of scope for v1**. It is recoverable additively if a real model demands a profile-divergent bundle; until then it is speculation.

### 3. Sampling composes with reasoning at the provider seam

The resolver does not know what reasoning encoding will be sent on the wire, and reasoning-mode models often want different sampler bundles than non-thinking-mode runs of the same model. (Qwen3 thinking-mode behavior degrades under the same low-temperature sampler that is correct for non-thinking code generation.)

To avoid the resolver and `reasoningRequestOptions` (`internal/provider/openai/openai.go:286`) silently fighting on the same request body, **the openai-compat provider is the single owner of final wire-field composition**. The provider receives the resolved sampling profile *and* the reasoning policy and is responsible for any clipping or substitution when reasoning is active.

This keeps the catalog flat (one `sampling_profiles.code` bundle, not a `code` × thinking-state matrix) and concentrates the interaction logic at one site.

### 4. Wrapped harnesses do not honor catalog sampling

Pi, codex, and claude-code drive their own SDKs and pin samplers internally. The catalog's `sampling_profiles` field is **descriptive metadata only** for runs that resolve to those harnesses; nothing flows on the wire. The resolver returns a zero-value profile when `selection.Harness != "agent"`, by contract.

`ModelEntry.sampling_control` records this state:

- `client_settable` (default) — provider honors all five fields.
- `harness_pinned` — provider drops everything; resolver short-circuits.
- `partial` (reserved, not implemented in v1) — provider honors a subset (e.g., Anthropic Messages API: `temperature`, `top_p`, `top_k` only). The honored-field list is part of the catalog entry when this state is used.

The Anthropic-direct path is `partial` in principle but ships in v1 as `client_settable` with its provider-side filter unchanged; deferring the field-list enforcement is an acceptable v1 simplification until a sampling profile is seeded that includes Anthropic-incompatible fields.

### 5. Telemetry: source-of-truth per request

`LLMRequestData` (`internal/session/event.go:31`) gains a `sampling_source` field. Values: `catalog`, `provider_config`, `cli`, or comma-separated when fields come from multiple layers. This is the routing-failure signal for sampling, mirroring ADR-006's override telemetry. Without it, post-hoc analysis cannot tell whether a degraded run used catalog defaults or a stale per-provider override.

### 6. No client-side range validation

Out-of-range values (`top_p=2.5`, `temperature=-1`) pass through unchanged. The server is the authoritative checker and will reject. We do not maintain per-server ranges.

## Consequences

**Positive:**

- Native agent on Qwen3.x stops hitting greedy-decoding tool-call loops. The 2026-04-27 harness-parity failure mode is addressed by a single seeded `code` profile.
- ADR-006 invariant (overrides are signals) extends cleanly to sampling: any `provider_config` or `cli` source on `sampling_source` is a candidate datum for catalog-default tightening.
- Per-(model × profile) overrides remain available as an additive change without rewrite.

**Negative:**

- One new manifest dimension and one new struct field add catalog complexity.
- The reasoning × sampling composition rule lives at the provider seam, not in the resolver. Anyone touching `reasoningRequestOptions` must remember it co-owns wire fields with sampling. The provider seam is documented but the coupling is real.
- Wrapped-harness rows carry a sampling field that is not enforced on the wire — descriptive metadata invites confusion in catalog audits.

**Mitigations:**

- The implementation bead carries a table-driven test of the per-field merge and a reasoning-active wire test pinning Qwen3 thinking-mode composition behavior before any catalog values ship.
- `sampling_control` defaults to `client_settable`; the `harness_pinned` short-circuit is exercised in the resolver tests so the wrapped-harness contract is enforceable, not aspirational.

## Out of scope

- CLI flags for sampler fields (`--temperature`, etc.).
- Per-`(model, profile)` overrides on `ModelEntry`.
- Per-request profile selection (v1 hardcodes `code`).
- `partial` sampling_control enforcement (Anthropic field-list clipping).
- Range validation.
- Server-side `seed` plumbing — empirically ignored by oMLX as of 2026-04-27 (`/tmp/probe_samplers.py` log).
