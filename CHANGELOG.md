# Changelog

All notable changes to ddx-agent are recorded here.
Dates use the repo convention (`YYYY-MM-DD`); versions follow semver.

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
