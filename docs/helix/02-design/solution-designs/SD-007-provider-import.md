---
ddx:
  id: SD-007
  depends_on:
    - SD-005
---
# Solution Design: SD-007 — Provider Import from Pi and OpenCode

## Problem

Users of pi and opencode have already configured their LLM providers — API
keys, LM Studio endpoints, custom model definitions. DDX Agent shouldn't require
them to duplicate this work. But runtime coupling to other tools' config
formats creates fragile dependencies and maintenance burden.

## Design: Import-Time Translation

DDX Agent reads other tools' configs at **import time** (explicit user action),
translates them to agent-native `.agent/config.yaml` (per SD-005 schema),
and records the import source so it can detect drift later.

### CLI Commands

```
ddx-agent import pi              # import from ~/.pi/agent/{auth,settings,models}.json
ddx-agent import opencode         # import from ~/.local/share/opencode/auth.json + opencode.json
ddx-agent import pi --diff        # show what pi has that agent doesn't (dry run)
ddx-agent import opencode --diff  # same for opencode
ddx-agent import pi --merge       # merge new providers without overwriting existing
```

### Zero-Config Discovery

When agent starts with no providers configured (no `.agent/config.yaml`, no
`~/.config/agent/config.yaml`, no `AGENT_*` env vars, no standard API key env
vars), it checks for importable configs and shows a notice:

```
agent: no providers configured. Found pi config at ~/.pi/agent/ — run 'ddx-agent import pi' to import.
```

This is a one-line stderr notice, not an error. DDX Agent still runs if env vars
provide a usable provider.

### Import Sources

#### Pi (`ddx-agent import pi`)

**Reads:**
- `~/.pi/agent/auth.json` — OAuth tokens and API keys per provider
- `~/.pi/agent/settings.json` — defaultProvider, defaultModel
- `~/.pi/agent/models.json` — custom provider definitions (LM Studio, Ollama)

**Two-source merge algorithm:**

Pi splits config across auth.json (credentials) and models.json (endpoints).
The import merges them:

1. Start with models.json providers — these have `baseUrl` and model IDs
2. For each models.json provider, look up the matching auth.json entry for
   credentials. If models.json has its own `apiKey`, use that (local providers
   like LM Studio use placeholder keys like `"lmstudio"`)
3. For auth.json entries with NO matching models.json provider (e.g.,
   `anthropic`, `openai-codex`, `openrouter`), create ddx-agent providers using
   well-known defaults (built-in URL, type mapping)
4. Apply settings.json `defaultProvider` + `defaultModel` for the `default:`
   field, mapping pi provider names to the agent provider name

**Pi provider name → agent provider mapping:**

| Pi auth name | DDX Agent name | Type | Default URL | Notes |
|-------------|------------|------|-------------|-------|
| `anthropic` | `anthropic` | `anthropic` | (SDK default) | OAuth access token as API key |
| `openai-codex` | `openai` | `openai-compat` | `https://api.openai.com/v1` | OAuth access token as bearer |
| `openrouter` | `openrouter` | `openai-compat` | `https://openrouter.ai/api/v1` | API key from auth.json |
| `google-gemini-cli` | skipped | — | — | Not yet supported |
| `github-copilot` | skipped | — | — | Proprietary auth flow |
| Custom (models.json) | pi provider name | mapped from `api` field | from `baseUrl` | `openai-completions` → `openai-compat` |

**Output uses SD-005 field names exactly:**

```yaml
providers:
  anthropic:
    type: anthropic          # SD-005: type field
    api_key: sk-ant-oat01-...  # SD-005: api_key field
    model: claude-sonnet-4-20250514

  vidar:
    type: openai-compat      # SD-005: type field
    base_url: http://vidar:1234/v1  # SD-005: base_url field
    api_key: lmstudio         # SD-005: api_key field (placeholder for local)
    model: qwen/qwen3-coder-next

default: anthropic
```

**What gets skipped with warnings:**
- `!command` API key values → warning: "provider X uses shell-resolved key,
  set AGENT_API_KEY or add api_key manually"
- Providers with `api` field that doesn't map to `openai-compat` or `anthropic`
- `headers` values that use `!command` resolution

**Empty model lists:** When models.json has `models: []`, the import queries
the provider's `/v1/models` endpoint to discover available models. If
unreachable, omits the `model` field (agent will use whatever the provider
defaults to).

#### OpenCode (`ddx-agent import opencode`)

**Reads:**
- `~/.local/share/opencode/auth.json` — `{type: "api", key: "..."}`
- `opencode.json` (project) or `~/.config/opencode/opencode.json` (global)

**Translation uses SD-005 field names:**
- `options.baseURL` → `base_url`
- `options.apiKey` or auth.json key → `api_key`
- `npm: "@ai-sdk/openai-compatible"` → `type: openai-compat`
- `options.headers` → `headers`

### Secret Handling

**Secrets go to user config, not project config.**

The import writes to `~/.config/agent/config.yaml` (user-global) by default,
NOT `.agent/config.yaml` (project-level). This prevents accidental commits of
API keys. The `--project` flag writes to `.agent/config.yaml` but requires
explicit confirmation and warns:

```
agent: warning: writing API keys to project config (.agent/config.yaml)
agent: ensure .agent/config.yaml is in .gitignore before committing
Proceed? [y/N]
```

**OAuth tokens are never stored alongside refresh tokens.** The import only
persists the `access` token. Refresh tokens in auth.json are ignored.

**File permissions:** Config files with API keys are written with `0600`
(owner read/write only).

**Diff output redacts secrets:** API keys shown as `sk-ant...4f2a` (first 6 +
last 4 chars). Full keys never printed to stdout.

### Drift Detection

The generated config includes a metadata field:

```yaml
imported_from:
  source: pi
  timestamp: "2026-04-07T15:30:00Z"
  source_hash: a1b2c3d4  # SHA-256 of auth.json + models.json concatenated, truncated to 8 hex
```

**Hash covers file content, not individual secrets.** This is acceptable
because the hash is one-way and truncated — it detects "something changed"
without revealing what.

**Check logic:**
- On `ddx-agent providers` or `ddx-agent -p`, if `imported_from` exists and source
  files have a different hash, emit once per day:
  ```
  agent: pi config changed since import — run 'ddx-agent import pi --diff' to review
  ```
- Debounced by checking mtime of `~/.config/agent/.import-check-{source}`
- Per-source, so pi and opencode drift are tracked independently

**Token expiry:** OAuth tokens have an `expires` field in epoch milliseconds.
On import, if a token expires within 24 hours, warn:
```
agent: warning: anthropic token expires in 3h — use pi to refresh, then re-import
```
If already expired, warn but still import (the token might still work briefly,
or the user may want the endpoint config without the token).

### Merge Mode

`ddx-agent import pi --merge`:
- Adds new providers that don't exist in agent config
- For existing providers: updates `api_key` only (credentials refresh)
- Never overwrites `base_url`, `model`, `headers` (user may have customized)
- Reports what was added, what was updated, what was skipped

`ddx-agent import pi` (no `--merge`):
- Replaces the entire `providers:` section
- Preserves non-provider config (`max_iterations`, `session_log_dir`, `preset`)
- Preserves `imported_from` metadata
- Warns before overwriting if existing config has providers not from the source

### Standard Env Var Fallback

Independent of import, `config.Load()` creates implicit providers from
standard env vars as a last resort (only when NO explicit provider of that
type is configured):

| Env var | Provider name | Type | Default URL |
|---------|--------------|------|-------------|
| `ANTHROPIC_API_KEY` | `anthropic` | `anthropic` | (SDK default) |
| `OPENAI_API_KEY` | `openai` | `openai-compat` | `https://api.openai.com/v1` |
| `OPENROUTER_API_KEY` | `openrouter` | `openai-compat` | `https://openrouter.ai/api/v1` |

These implicit providers have lower precedence than any explicit config.
They don't create a `default:` — the user must specify `--provider` or set
`AGENT_PROVIDER` to use them.

### Config Schema Additions to SD-005

SD-005's Config struct gains:

```go
type Config struct {
    // ...existing fields from SD-005...

    // ImportedFrom records the last import source for drift detection.
    ImportedFrom *ImportMetadata `yaml:"imported_from,omitempty"`
}

type ImportMetadata struct {
    Source     string `yaml:"source"`      // "pi" or "opencode"
    Timestamp string `yaml:"timestamp"`   // RFC3339
    SourceHash string `yaml:"source_hash"` // truncated SHA-256 of source files
}
```

The config loader ignores `imported_from` — it's metadata, not provider config.

## Implementation Plan

| # | Task | Depends |
|---|------|---------|
| 1 | Pi auth.json reader (picompat/auth.go) | — |
| 2 | Pi models.json reader (picompat/models.go) | — |
| 3 | Pi settings.json reader + translate to agent config | 1, 2 |
| 4 | OpenCode auth + config reader (occompat/) | — |
| 5 | `ddx-agent import` CLI command with diff/merge/redaction | 3, 4 |
| 6 | Zero-config discovery notice in CLI startup | 5 |
| 7 | Drift detection (hash check + daily debounce) | 5 |
| 8 | Standard env var fallback in config.Load() | — |

## Package Structure

```
agent/
  picompat/           # pi config readers
    auth.go           # reads auth.json → map[provider]credential
    models.go         # reads models.json → map[provider]providerDef
    settings.go       # reads settings.json → default provider/model
    translate.go      # merges auth+models+settings → []config.ProviderConfig
    picompat_test.go

  occompat/           # opencode config readers
    auth.go           # reads auth.json
    config.go         # reads opencode.json
    translate.go      # translates to agent config
    occompat_test.go
```

## Risks

| Risk | Prob | Impact | Mitigation |
|------|------|--------|------------|
| OAuth tokens stale after import | H | M | Warn on <24h expiry; drift detection; merge refreshes keys |
| Pi models.json schema changes | M | L | Ignore unknown fields; only extract baseUrl/apiKey/models/api |
| `!command` API keys unsupported | M | L | Skip with warning + guidance to set env var |
| Empty model lists from local providers | M | L | Query /v1/models at import time; omit if unreachable |
| Accidental key commit | M | H | Default to user config; warn on --project; 0600 permissions |
| Large model lists from OpenRouter | L | L | Import all; user can prune |
