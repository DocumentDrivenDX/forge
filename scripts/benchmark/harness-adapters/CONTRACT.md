# harness-adapters contract

Each file under `scripts/benchmark/harness-adapters/` is an executable shell
script that translates a benchmark profile into:

1. an **install** description — how to stage the agent binary into the Harbor
   task container, and which Harbor adapter class to load;
2. a **command** description — the agent argv and the env (with secret keys
   flagged) that the benchmark runner forwards to the task executor.

The current adapters are:

```
fiz          claude       codex        opencode     pi
cost-probe   noop         dumb-script
```

`fiz` is the native harness. `claude`, `codex`, `opencode`, `pi` are
wrapper harnesses; their `command` returns env consumed by the matching
`scripts.benchmark.harbor_adapters.<name>:<Name>Agent` class inside the
`fizeau-harbor-runner` container. `cost-probe`, `noop`, `dumb-script` are
calibration adapters that do not call any model.

## Header convention

Every adapter file starts with a single `# SUMMARY:` line on line 2 (after
the shebang). The summary is one sentence that names the adapter and
states what its `install` and `command` subcommands emit. The benchmark
runner's `./benchmark harness-adapters` listing greps for this header.

```
#!/usr/bin/env bash
# SUMMARY: <one-line description>
```

## Subcommands

```
<name> install <agent-artifact-path>     -> install-spec.json on stdout
<name> command                           -> command-spec.json on stdout
                                            (reads profile JSON on stdin)
<name> --help                            -> usage on stderr, exit 2
```

`install` is invoked once per adapter per sweep; `command` is invoked once
per cell (profile × task × rep) on the runner's hot path, so it must be
fast and side-effect-free.

## install-spec.json

```json
{
  "install_command":  "install -m 0755 /path/to/fiz /installed-agent/fiz",
  "artifact_source":  "/path/to/fiz",
  "binary_path":      "/installed-agent/fiz",
  "harbor_plugin":    "scripts.benchmark.harbor_agent:FizeauAgent"
}
```

- `install_command` — single shell command, executed once on the host (or
  inside the harbor-runner container, depending on the executor) to stage
  the artifact. May be `"true"` for calibration adapters.
- `artifact_source` — the host path that was passed as
  `<agent-artifact-path>`. Echoed back for provenance.
- `binary_path` — the in-container path where the binary lands. Empty for
  calibration adapters.
- `harbor_plugin` — Python import path Harbor will load via
  `--agent-import-path`. `null` for calibration adapters that bypass
  Harbor (they emit a `command` directly runnable inside the task).

## command-spec.json

Input is profile JSON on stdin with at least the following keys (other
keys are passed through and may be referenced by individual adapters):

```json
{
  "id":       "vidar-ds4",
  "provider": {"type": "...", "model": "...", "base_url": "...", "api_key_env": "..."},
  "sampling": {"temperature": 0.6, "reasoning": "low", "planning_mode": true, "top_p": 0.95, "top_k": 20, "min_p": 0},
  "limits":   {"max_output_tokens": 65536, "context_tokens": 180000},
  "metadata": {"runtime": "...", ...}
}
```

`metadata` is optional in the checked-in profile catalog; adapters use it
when present but do not require it for command-spec generation.

`sampling` field schema (all keys optional; adapters omit env vars for
unset keys):

| key              | type    | default | meaning                                                            |
| ---------------- | ------- | ------- | ------------------------------------------------------------------ |
| `temperature`    | float   | unset   | sampler temperature                                                |
| `top_p`          | float   | unset   | nucleus-sampling cutoff                                            |
| `top_k`          | int     | unset   | top-k cutoff                                                       |
| `min_p`          | float   | unset   | minimum-probability cutoff                                         |
| `reasoning`      | string  | `""`    | reasoning effort hint (`""`, `low`, `medium`, `high`)              |
| `planning_mode`  | bool    | `false` | run one pre-execution decomposition pass before the main tool loop |

`planning_mode` is an explicit, per-profile opt-in. The `fiz` adapter
appends `--plan` to the agent argv and exports `FIZEAU_PLANNING_MODE=1`
when this field is `true`. There is no implicit coupling to any tool
preset (e.g. `benchmark`); profiles that want planning must set
`sampling.planning_mode: true`.

Output:

```json
{
  "command":         ["/installed-agent/fiz", "--json", "--preset", "default", "-p", "$HARBOR_INSTRUCTION"],
  "env":             {"FIZEAU_BASE_URL": "http://vidar:1234/v1", "FIZEAU_MODEL": "qwen3.6-27b", "FIZEAU_API_KEY": "${FOO_KEY}", "...": "..."},
  "secret_env_keys": ["FIZEAU_API_KEY"]
}
```

- `command` — argv list. `$HARBOR_INSTRUCTION` and `${VAR}` strings remain
  unexpanded; the task executor (or harbor adapter) handles substitution
  at run time. For wrapper harnesses this argv is informational; Harbor
  invokes the agent via its plugin and uses `env`.
- `env` — env vars to forward into the container. Values containing
  `${VAR}` reference host env to be expanded by the task executor.
- `secret_env_keys` — subset of `env` keys whose values are secrets and
  must be redacted in any logged/recorded form. Order-insensitive,
  duplicate-free.

## Determinism rules

- `command` and `install_command` must be deterministic given the same
  profile and artifact path — no random suffixes, no timestamps, no
  filesystem temp-dir mkdtemp on the host. The runner relies on stable
  command-specs to compute cell content hashes.
- Calibration adapters (`cost-probe`, `noop`, `dumb-script`) must drain
  stdin (`cat >/dev/null`) before emitting their fixed JSON so callers
  can stream a profile in without backpressure.

## Validation

`./benchmark validate` calls every adapter's `command` against a synthetic
profile and rejects:

- non-zero exit;
- output that is not valid JSON;
- output missing one of `{command, env, secret_env_keys}`;
- `secret_env_keys` entries not present in `env`.

Adapters reject malformed or schema-invalid profile JSON on stdin before
emitting a command spec.

`shellcheck` (CI gate) must pass on every file in this directory.
