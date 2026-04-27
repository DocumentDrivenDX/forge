# Changelog

All notable changes to ddx-agent are recorded here.
Dates use the repo convention (`YYYY-MM-DD`); versions follow semver.

## [Unreleased]

## [v0.9.13] — 2026-04-26

Iteration-cap fix for the execute-bead service path. Live Claude Opus
baselines for medium-difficulty agent tasks land in the 29–99 tool-call
range, which the prior implicit cap of 50 was truncating mid-loop. The
operator-config `max_iterations` was also being silently ignored on the
execute-bead path, so the configured ceiling never reached the service.

### Changed

- `service.Execute` decouples its iteration ceiling from the
  read-only stall threshold. New explicit `defaultMaxIterations = 200`
  applies when the caller leaves `ServiceExecuteRequest.MaxIterations`
  unset (was: implicit `MaxReadOnlyToolIterations × 2 = 50`). The
  read-only stall detector is unchanged. (`service_execute.go`)
- `config.Defaults().MaxIterations` raised from 20 to 100 to match
  the observed tool-call envelope and avoid silently truncating
  legitimate long-loop work in the agent CLI path.
  (`internal/config/config.go`, `internal/config/config_test.go`)
- Beadbench manifest gains harness-parity arms on the Qwen 3.5 27B
  family across local/cloud server backings
  (`agent-omlx-vidar-qwen35-27b`, `agent-bragi-lmstudio-qwen35-27b`,
  `agent-openrouter-qwen35-27b`) plus reference tasks
  `agent-beadbench-preflight` (Claude Opus baseline 31 tool calls)
  and `agent-pi-local-providers` (62 tool calls — stretch canary
  that exceeds the prior 50-iteration cap).
  (`scripts/beadbench/manifest-v1.json`)

## [v0.9.12] — 2026-04-26

Operational/research release — scaffolds the harness-parity iteration loop
that compares native agent vs pi on shared backings. Benchmark execution
itself is in progress at tag time (long-running; ~3-8h wall-clock); results
+ winner declaration + top-3 gaps land in v0.9.13.

### Added

- **Beadbench arms for harness-parity comparison** on the same omlx vidar
  Qwen3.6-27B-MLX-8bit backing, both with codex/gpt-5.5 reviewer pin:
  `agent-omlx-vidar-qwen36` (native agent harness) and
  `pi-omlx-vidar-qwen36` (pi harness via pi's anthropic-messages
  config to vidar omlx). Pairs in the manifest so the runner produces
  apples-to-apples comparison output. (`agent-bea748e7`)
- **Research AR scaffolding for the parity benchmark.**
  `docs/research/AR-2026-04-26-agent-vs-pi-omlx-vidar-qwen36.md` lands
  with sections 1 (methodology — paired design, N≥8, reviewer pin, win
  condition, tiebreaker) and 2 (pi-config evidence — the trailing-comma
  JSON syntax fix and the openai-completions → anthropic-messages
  switch that unblocked pi against omlx). Sections 3–5 (per-task
  pairwise table, aggregate metrics, top-3 gaps) marked TBD pending the
  benchmark run. (`agent-1bffdd79`)

### Changed

- **Harness-parity bead split into four children** matching the
  iteration-loop structure: manifest arms (filed v0.9.12), doc skeleton
  (filed v0.9.12), benchmark execution (in-flight at tag time), reactive
  iteration tweak beads (filed after data lands).

## [v0.9.11] — 2026-04-26

ADR-006 chain ships: manual overrides become first-class auto-routing
failure signals. Plus the codex-design reasoning-variants chain
(catalog fields → provider-wire enforcement → auto-resolution),
selection-precedence spec amendments, and the AGENTS.md discipline
sections that operationalize this session's learnings.

### Added

- **Override-as-failure-signal telemetry (ADR-006).** `Execute` now
  emits `override` and `rejected_override` events when callers pin any
  of `Harness`/`Provider`/`Model`. The override event captures both the
  user-pinned decision and the unconstrained auto-decision (computed
  via a second in-process `ResolveRoute` with override axes stripped),
  so future routing improvements have data on where users disagree
  with the optimizer. Per-axis tracking; coincidental agreement still
  fires the event. (`agent-9fc2633c`, `agent-017b043f`,
  `agent-79aedde2`)
- **Routing-quality metrics on `RouteStatus` and `UsageReport`.**
  `auto_acceptance_rate`, `override_disagreement_rate`, and
  `override_class_breakdown` exposed as first-class metrics distinct
  from per-(provider, model) provider reliability. UsageReport reads
  from session logs over the `--since` window; RouteStatus uses the
  in-memory ring. Both honor the new metric-vs-reliability separation
  in their UI. (`agent-017b043f`)
- **`ddx agent route-status --overrides --since DURATION` operator
  surface.** Prints the override-class breakdown over the selected
  window, with `--axis harness|provider|model` filter and `--json`
  output. Help text references ADR-006. (`agent-79aedde2`)
- **Catalog reasoning capability fields.** `ModelEntry` gains
  `reasoning_levels []string`, `reasoning_control string`
  (`tunable | fixed | none`), and `reasoning_wire string`
  (`provider | model_id | none`). Eight manifest entries populated
  covering all three control/wire cases. Captures the codex-design
  distinction between tunable-effort and reasoning-variant models.
  (`agent-a0160cde`)
- **`Reasoning=auto` resolves to surface-policy default before
  capability gate.** Routing engine looks up the target's
  `reasoning_default` for the requested tier and uses it as the
  resolved level for gating. Empty/unset `Reasoning` still bypasses
  the gate. Forward-protection against future non-thinking variants
  landing in higher tiers. (`agent-ba467996`)
- **Spec selection-precedence section in CONTRACT-003.** Documents
  pin precedence (Harness → Provider → Model → ModelRef → Profile)
  with explicit hard/soft semantics and Profile's role as
  cost-vs-time intent. Demoted to "implementation reference" per
  ADR-006 — the user-facing surface is profile + auto, pins are
  override hatches. (`9a04ad4`)
- **AGENTS.md discipline sections.** Three new sections operationalize
  this session's learnings: Review and Verification Discipline, Spec
  Amendment Discipline, Bead Sizing and Cross-Repo Triage. Locks in
  the local-verify-before-accept loop that caught real defects this
  session. (`d7f2a1c`)
- **AR-2026-04-25-routing-and-overrides alignment review.** Captures
  the v0.9.9 / v0.9.10 / ADR-006 narrative through-line with seven
  findings driving the discipline updates.

### Fixed

- **`rejected_override` events now persist to session log.** Pre-
  dispatch validation failures (orphan model, unknown provider) emit
  rejected_override events to both the channel and the session log so
  UsageReport's window aggregation sees them. Prior implementation
  broadcast-only, missing the persistent record. (`6c4340f`)
- **Provider-wire enforcement for fixed-variant models.** The
  openai-compat dispatch consults the resolved model's
  `reasoning_wire` field. `model_id` mode strips the reasoning field
  on the wire (model name is the encoding); `none` mode rejects
  pre-flight when `Reasoning` is non-off. Closes a silent
  OpenRouter-emits-reasoning-for-fixed-variant-Qwen bug codex flagged.
  (`agent-b6b15fb0`)
- **Pi+provider-pin model validation duplicated on engine path.** The
  routing engine's `pinValidation` rejected pi+qwen with
  `ErrHarnessModelIncompatible` even though `service_execute` had a
  bypass for pi-with-explicit-provider. Mirrored the bypass in the
  engine. (`605729c`)
- **`TestListHarnesses_GeminiAccountAndUsageWindows` UTC-midnight
  flake.** Test now uses an injected deterministic clock; passes 10/10
  in repeat without time manipulation. (`0b22784`)

### Changed

- **Selection-precedence section in CONTRACT-003 demoted to
  implementation reference.** Pin precedence prose stays accurate but
  is no longer presented as the primary user-facing surface. ADR-006
  reframes pins as override hatches; routine pinning for a given
  prompt class is a routing-quality issue to file. (`9a04ad4`,
  `c687caf`)

## [v0.9.10] — 2026-04-25

Prompt-caching support lands across the public surface, the Anthropic provider,
the openai-compat regression gate, and cost attribution. No breaking changes.

### Added

- **`ServiceExecuteRequest.CachePolicy` and `RouteRequest.CachePolicy`.**
  Public opt-out for callers who must disable caching (deterministic eval,
  privacy-sensitive prompts, one-shot benchmark runs). Values: `""` and
  `"default"` mean "cache as designed"; `"off"` suppresses provider-side
  cache markers and emits explicit-zero cache-amounts in cost attribution.
  Unknown values rejected at the boundary. (`agent-cccc2df7`)
- **Anthropic provider emits `cache_control: {type: "ephemeral"}` on the
  last tool definition and the last system block.** Multi-turn native
  Anthropic sessions now hit Anthropic's prompt cache (~10× discount on
  cache reads). System-block construction extracted to a shared
  `buildSystemBlocks` helper consumed by both `Chat` and `ChatStream` so
  streaming and non-streaming paths cache identically. `CachePolicy="off"`
  suppresses both markers. Wire-body assertions cover both paths.
  (`agent-3bc67e94`)
- **openai-compat prefix-stability regression gate.** New wire-level test
  via `httptest.NewServer` captures actual HTTP request bodies across two
  `Chat` calls with identical tools+system but a differing trailing user
  message; asserts byte-equality on the prefix. A negative test
  (`TestOpenAIRequestPreservesToolOrder`) ensures tool order is preserved.
  No behavior change — this protects auto-prefix-caching on OpenAI,
  LM Studio, oMLX, and OpenRouter against silent regressions.
  (`agent-50658a65`)
- **Cache-aware cost attribution at the native loop.** `core.ModelPricing`
  carries `CacheReadPerM` / `CacheWritePerM`; `modelcatalog.PricingFor`
  preserves them from manifest v4 (`cost_cache_read_per_m`,
  `cost_cache_write_per_m`); `loop.go:303` configured-cost computation
  now adds cache-read and cache-write costs and populates
  `CostAttribution.CacheReadAmount` / `CacheWriteAmount`. `CachePolicy="off"`
  emits explicit `*float64(0.0)`; absence of cache reporting from the
  harness/provider remains nil. (`agent-6e2ebcdb`)
- **`gpt-5.5` model entry in the embedded catalog.** Available for explicit
  `--model gpt-5.5` pinning on the codex harness. The code-high candidate
  list keeps `gpt-5.4` as the default first pick to preserve existing
  routing test expectations.
- **Architecture doc gains a Caching section.** Documents the prefix-order
  invariant (tools → system → conversation → trailing user), two-marker
  placement, and the compaction and tool-mutation caveats.

### Changed

- Existing telemetry parsing of `cache_read_input_tokens`,
  `cache_creation_input_tokens`, and `prompt_tokens_details.cached_tokens`
  is unchanged but now feeds cost attribution that actually uses those
  numbers. Operators reading `ddx-agent usage` get accurate cost figures
  for cache-supporting providers.

### Fixed

- **Cache-amount nil-vs-zero ambiguity in cost attribution.** Reviewer
  caught that the initial cost-attribution wiring set
  `CacheReadAmount` / `CacheWriteAmount` pointers unconditionally during
  configured-cost computation, collapsing two distinct conventions:
  `CachePolicy="off"` (explicit zero) and "harness reports zero cache
  tokens" (nil). Fixed in `6cdfdc5`; the nil leg is now covered by
  `TestCacheAttributionNilWhenHarnessReportsZeroAndPolicyDefault`.

## [v0.9.9] — 2026-04-25

This release lands ADR-005 (smart routing replaces `model_routes`) plus the
preceding service-boundary work that made it possible. The agent now picks
`(harness, provider, model)` automatically from the catalog, configured
provider liveness, and per-(provider, model) signal — no
`model_routes:` YAML required. Native Anthropic still does not write
`cache_control`; that work is staged separately.

### Breaking

- **`ServiceExecuteRequest.PreResolved` removed.** Callers no longer round-trip
  a `RouteDecision` from `ResolveRoute` into `Execute`. `ResolveRoute` is
  informational only; `Execute` always re-resolves on its own inputs.
  (`agent-ddcc903b`, ADR-005)
- **`ServiceConfig.ModelRouteConfig` / `ModelRouteNames` removed from the
  primary config surface.** Legacy `model_routes:` YAML still parses for one
  release with a deprecation warning that names the offending key path; the
  next release rejects it outright. The deprecation surface lives in
  `internal/config/legacy_model_routes.go`. A boundary test forbids
  reintroduction in `internal/config/config.go`. (`agent-21a521fc`, ADR-005)
- **`cmd/agent/routing_provider.go` `routeProvider` type and
  `cmd/agent/provider_build.go` deleted.** Provider construction and
  per-Chat failover are owned by the service-side smart routing engine.
  Route-status display helpers remain. (`agent-21a521fc`)

### Added

- **Smart routing auto-selection inputs.** `ServiceExecuteRequest` and
  `RouteRequest` carry `EstimatedPromptTokens` and `RequiresTools`. When the
  caller pins nothing, the service filters candidates by context window and
  tool support before scoring. Explicit `--profile` / `--model` /
  `--model-ref` / `--provider` always wins. (`agent-ddcc903b`, ADR-005)
- **Per-candidate component scores on `RouteCandidate`.** Routing-decision
  events expose `Components{Cost, LatencyMS, SuccessRate, Capability}` and a
  typed `FilterReason` (`context_too_small`, `no_tool_support`,
  `reasoning_unsupported`, `unhealthy`, `scored_below_top`,
  `eligible`) set at the rejection site in `internal/routing.CheckGating`,
  not parsed from free-form text. (`agent-ddcc903b`, `agent-2c55b8a4`)
- **Liveness escalation in `ResolveRoute`.** When every candidate at the
  requested tier is filtered out, the service walks the profile tier ladder
  (cheap → standard → smart) before failing. Exhaustion surfaces a precise
  `no live provider supports prompt of N tokens with tools=B at tier ≥ X`
  error, replacing the engine's generic "tiers exhausted" jargon.
  (`agent-99433b19`)
- **`ContextWindows` wired from the catalog into every `ProviderEntry`.** The
  engine's context-window gate now has data to act on; previously
  `EstimatedPromptTokens` reached `routing.Request` but the filter saw an
  empty context-window map. (`agent-c953a473`)
- **Route-status redesigned around `ResolveRoute` candidate trace.**
  `ddx agent route-status --profile smart` returns the full ranked candidate
  list with score components and `filter_reason` per candidate, replacing the
  old `model_routes:` enumeration. (`agent-9c9cc191`)
- **Per-(provider, model) routing signal.** `routeMetricSignals` keys
  success/latency on `(provider, model)` rather than per-tier; one bad model
  no longer locks out its whole tier. (`agent-934fb8a2`)
- **Service-owned session-log persistence.** Native and subprocess execution
  write authoritative lifecycle records from inside the service execution
  path; cmd/agent no longer synthesizes them. Final results still expose the
  session-log path; replay/usage flows continue to work against
  service-owned logs. (`agent-7faa0edf`, `agent-b9bd700f`, `agent-99549438`)
- **`ServiceFinalUsage` distinguishes zero from unknown.** Token-count fields
  are `*int`. Nil = harness did not emit; explicit zero = upstream provider
  reported zero. Consumers MUST NOT treat nil as zero. CONTRACT-003 also now
  documents that `success` final events with empty `final_text` are valid
  outcomes — consumers must not retry on empty text alone. (`agent-a8cbdb87`)
- **`cmd/agent` boundary allowlist tightened.** Production cmd/agent files
  may import only seven internal packages: `config`, `modelcatalog`,
  `observations`, `productinfo`, `prompt`, `reasoning`, `safefs`. A denylist
  and symbol-level checks reject `internal/core`, `internal/provider/*`,
  `internal/session`, `internal/tool`, `internal/compaction`,
  `internal/harnesses`, `internal/routing`, plus the surfaces that have
  public replacements (`agentcore.Run`, `compaction.NewCompactor`,
  `tool.BuiltinToolsForPreset`, `session.NewLogger`, etc.).
  (`agent-1023f072`)
- **Beadbench: reasoning-control sweep manifest entries.** Added
  `agent-vidar-omlx-qwen36-27b-high` and `agent-openrouter-sonnet46` for
  isolating reasoning budget vs. tool-loop quality. Plus a Qwen
  reasoning-control sweep research note. (Earlier in the release window.)
- **Bash benchmark-mode policy.** The `bash` tool blocks shell `find` and
  recursive `ls -R` in benchmark preset, surfacing a policy violation that
  steers the agent toward the typed `find` and `ls` tools. Non-benchmark
  presets remain unrestricted. (Earlier.)
- **Pi local-provider pins + `--provider` flag.** (`agent-9dbfad9c`)
- **Gemini PTY quota probe + routing guard for `/model manage`.**
  (`agent-37659612`)
- **LM Studio adopts Qwen reasoning wire format.** With server blocker
  documented. (`agent-b79ecf22`)
- **Beadbench reasoning-probe streaming, separability honor for
  `model_comparison_valid`, partial-output preservation on timeout, and
  preflight/phase-specific verification status.**
  (`agent-74fc7a51`, `agent-39128ccf`, `agent-52529ba7`,
  `agent-37aeb88e`)

### Changed

- **`ResolveRoute` semantics.** Returns ranked candidates without
  short-circuiting on configured `model_routes`; consumers cannot inject
  `RouteDecision` back into `Execute`. The previous short-circuit landed
  briefly under `agent-6dd4ad97` and was effectively reverted.
- **Per-tier adaptive min-tier window removed.** The trailing-success-rate
  lockout that locked out `cheap` after 0.06 trailing-success over 17
  attempts is gone; the per-(provider, model) signal lets individual models
  recover without dragging their whole tier.
- **Provider verification of reasoning control.** `low`/`medium`/`high` are
  rejected when the provider has not been verified for request-level
  reasoning control; previously some non-verified providers silently
  ignored the request. (`agent-2168979d`)
- **Status surfaces endpoint-down reasons for local providers.**
  (`agent-90344fdc`)

### Fixed

- **`model_routes:` no longer required for same-tier failover.** Local LM
  Studio hosts coordinate failover automatically via liveness + tier
  escalation. (ADR-005)
- **Beadbench timeouts preserve partial output.** (`agent-52529ba7`)

## [v0.7.0] — 2026-04-20

### Fixed
- **Route HTTP provider-backed native harnesses by concrete provider type.**
  Service execution now resolves native harness/provider dispatch through the
  concrete provider identity instead of the configured provider name, so
  renamed `lmstudio`, `omlx`, `openrouter`, `ollama`, and `openai` providers
  route correctly after the v0.6.0 provider split. (`agent-117a0868`)

### Changed
- **Profile-owned placement policy replaced public provider preference
  routing.** Service callers use profiles such as `cheap`, `standard`,
  `smart`, or user-defined profiles as the public routing knob. Catalog
  `surface_policy` can carry placement order, cost ceilings, failure policy,
  and reasoning defaults; subscription quota health and burn trend still
  influence same-tier scoring internally. (`agent-117a0868`)

## [v0.6.0] — 2026-04-20

### Breaking
- **Removed runtime provider `flavor` behavior from the OpenAI-compatible
  provider.** Concrete provider packages now own provider identity,
  capabilities, discovery, limit lookup, and cost attribution. Direct
  `openai.Provider` construction defaults to OpenAI identity unless callers
  explicitly pass provider metadata.
- **Removed deprecated prompt preset aliases.** Harness-flavored names such as
  `agent`, `worker`, `cursor`, `claude`, and `codex` now fail clearly instead
  of warning and resolving to canonical presets.

### Added
- **Concrete provider identity split.** `openai`, `openrouter`, `lmstudio`,
  `omlx`, `ollama`, and `anthropic` are provider identities; shared
  OpenAI-compatible protocol plumbing lives below them in
  `internal/sdk/openaicompat`.
- **Provider preference routing.** Service and routing requests can express
  local-first, subscription-first, local-only, and subscription-only policy,
  with subscription quota health and burn trend influencing same-tier scoring.

### Changed
- Provider routing and tool contract docs were refreshed to reflect the
  concrete-provider model and bounded tool-output behavior.

## [v0.5.0] — 2026-04-19

### Breaking
- **Removed the legacy `agent.Run` API from the public module surface.**
  The former `Request`, `Result`, `Provider`, `StreamingProvider`, `Tool`,
  `Message`, `ToolDef`, `Event`, session-log DTO, compaction callback, pricing,
  and provider-conformance exports now live behind `internal/` for agent-owned
  code only. External consumers must use `agent.New(...).Execute(...)` and the
  DdxAgent service contract.
- **Removed public compatibility packages for the old provider/tool/session
  stack.** Provider implementations, compaction, prompt building, built-in
  tools, session replay/logging, and provider conformance helpers are no longer
  importable outside this module; Go `internal/` enforcement now blocks
  consumers that have not migrated.

### Changed
- The standalone `ddx-agent` binary continues to use the internal native loop,
  but that loop is no longer part of the exported Go API.

### Changed
- **Removed harness-flavored prompt preset names.** The old `agent`, `worker`,
  `cursor`, `claude`, and `codex` names now return clear errors. Use the
  canonical names (`default`, `smart`, `cheap`, `minimal`, `benchmark`)
  instead. (`agent-ff9c0289`)
- **Renamed the file-discovery tool from `glob` to `find`.** The built-in tool
  catalog now exposes only `find`; there is no `glob` compatibility alias.
  (`agent-1b00b3ea`)

## [v0.3.14] — 2026-04-18

### Fixed
- **Filter SSE comment frames before the `ssestream` decoder.**
  `openai-go`'s SSE decoder dispatches an event on any blank line —
  including the terminator of a comment-only frame like
  `: keep-alive\n\n`. `Stream.Next` then `json.Unmarshal`s empty bytes
  and surfaces `unexpected end of JSON input`, aborting the stream.
  `omlx` and other servers emit these comment frames during
  reasoning-model warmup. Per the WHATWG SSE spec, empty-data events
  must be silently ignored. Fix adds `sseCommentFilter` +
  `sseFilterMiddleware` to `provider/openai` that strips comment lines
  and suppresses the blank-line dispatch when the current frame has not
  yet seen a field line, so the decoder never observes an empty-event
  dispatch. Flavor-agnostic — applies to all openai-compat
  counterparties. Upstream removal triggers (`openai/openai-go` PRs
  #555 / #643, issues #556 / #618) are referenced in the filter source
  so the shim can be deleted once the SDK ships a fix.
  (`agent-f237e07b`)

### Added
- **`AGENT_DEBUG_WIRE_STREAM_FULL=1`** — opt-in env var that disables
  the 64 KB cumulative cap on `teeBody`, so the entire SSE stream is
  captured for post-mortem analysis. Default behavior unchanged.
  (`agent-f237e07b`, acceptance item 5)

### Tests
- `TestChatStream_SurvivesSSECommentFramesAndLongSilence` —
  regression test asserting that a frame sequence of (keep-alive
  comment, role delta, keep-alive comment, content delta, done)
  completes without error and delivers content.

## [v0.3.13] — 2026-04-18

### Fixed
- **Strip `thinking` field from non-Anthropic openai-compat requests.**
  Wire capture (via `AGENT_DEBUG_WIRE=1`) from DocumentDrivenDX/ddx
  `ddx-6a5dfe35` confirmed that `provider/openai/openai.go` was
  unconditionally injecting the non-standard `thinking` body field
  whenever a provider-level positive reasoning budget was configured,
  regardless of destination flavor.
  omlx silently terminates the SSE stream after the first delta when
  `thinking` is present — client-side OpenAI Go SDK then surfaces
  `unexpected end of JSON input`. Fix gates the field injection on
  a new `Provider.SupportsThinking()` capability flag, backed by a
  flavor-keyed table in `protocol_support.go`. Stripping is automatic
  for `omlx`, `openrouter`, `openai`, and `ollama`; `lmstudio`
  (original target of the field) is unchanged. (`agent-04639431`)

### Added
- **`Provider.SupportsThinking()`** — capability accessor matching the
  existing `SupportsTools` / `SupportsStream` /
  `SupportsStructuredOutput` pattern. Returns `false` for unknown
  flavors (conservative). Extends `protocolCapabilities` with a
  `Thinking bool` field. (`agent-04639431`)

### Specs evolved
- SD-005 (flavor-keyed protocol capability) — add `Thinking` to the
  capability matrix.

## [v0.3.12] — 2026-04-18

### Added
- **`AGENT_DEBUG_WIRE` env-var** — opt-in HTTP request/response dump at the
  openai-go transport boundary, for diagnosing integration defects at the
  `ddx-agent ↔ provider` boundary. Default off. Authorization Bearer tokens
  redacted. `AGENT_DEBUG_WIRE_FILE=<path>` routes JSONL output to a file.
  (`agent-941e7e42`)
- **`Provider.DetectedFlavor()`** — cached accessor that returns the effective
  server flavor (`lmstudio` / `omlx` / `openrouter` / `ollama`). Uses
  `Config.Flavor` when set, otherwise runs a one-time probe, otherwise falls
  back to the URL-heuristic `providerSystem`. (`agent-92f0f324`)
- **Protocol capability flags** — `Provider.SupportsTools()`,
  `SupportsStream()`, `SupportsStructuredOutput()`. Flavor-keyed; unknown
  flavors return `false` conservatively. Consumed by downstream routing (DDx
  `ddx-4817edfd`) to gate dispatch on what the provider+flavor can honor.
  (`agent-767549c7`)

### Notes
- `DetectedFlavor()` does **not** replace the existing `providerSystem` field
  used on the per-response telemetry hot path. See SD-005 D14 for the
  intentional layering.
- `SupportsTools` for omlx is set to `true` per vendor docs. If
  `ddx-6a5dfe35` produces wire evidence showing otherwise, the flavor table in
  `provider/openai/protocol_support.go` will be revised.

### Specs evolved
- FEAT-003 requirements 24–27 (protocol capability, debug observability)
- SD-005 decisions D13 (flavor-keyed protocol capability) and D14
  (`DetectedFlavor` vs `providerSystem` layering)

## [v0.3.11] — 2026-04-17

### Added
- **omlx provider support** — new flavor recognized by the OpenAI-compatible
  provider. Uses `GET /v1/models/status` for per-model context window and
  output token limits. `flavor: omlx` config field plus port-1235 URL
  heuristic.
- **`Flavor` config field** — explicit server-type hint on `ProviderConfig`
  (`lmstudio` / `omlx` / `openrouter` / `ollama`). Bypasses URL-based
  detection and probing when set.
- **Catalog `context_window` fallback** — `ModelEntry.ContextWindow` is now
  parsed from the v4 manifest; `Catalog.ContextWindowForModel(id)` exposes it
  for the CLI's three-tier limit cascade (explicit config → live API →
  catalog → package default). Fixes LM Studio servers that omit
  `context_length` from `/v1/models`.

### Specs evolved
- FEAT-003 requirements 14–23 + ACs 06–08
- SD-005 decisions D10 (live flavor-gated limit discovery), D11 (flavor
  replaces port heuristics), D12 (omlx as first-class provider)
- SD-006 CLI Context Window Resolution section
