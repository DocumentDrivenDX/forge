# task-executors contract

Each file under `scripts/benchmark/task-executors/` is an executable shell
script that runs one task for one cell. The benchmark runner streams a
**task-spec.json** to the executor on stdin; the executor writes
**result.json** into the cell directory and exits.

Today there is one executor:

- `harbor` — runs the task inside the `fizeau-harbor-runner` Docker image.
  Harbor handles task setup, agent invocation, grading, and trajectory
  capture; the executor is a thin shell wrapper that mounts the cell dir
  and forwards env.

## Header convention

Every executor file starts with a single `# SUMMARY:` line on line 2
(after the shebang). The benchmark runner's
`./benchmark task-executors` listing greps for this header.

```
#!/usr/bin/env bash
# SUMMARY: <one-line description>
```

## task-spec.json (stdin)

```json
{
  "task_id":         "patch-build-script",
  "tasks_dir":       "/home/erik/Projects/fizeau/external/terminal-bench/tasks",
  "cell_dir":        "/home/erik/.../cells/terminal-bench-2-1/patch-build-script/20260516T103045Z-a4c1",
  "harbor_plugin":   "scripts.benchmark.harbor_agent:FizeauAgent",
  "image":           "fizeau-harbor-runner:latest",
  "env": {
    "FIZEAU_BASE_URL": "http://vidar:1234/v1",
    "FIZEAU_MODEL":    "qwen3.6-27b",
    "FIZEAU_API_KEY":  "${FIZEAU_API_KEY}"
  },
  "secret_env_keys": ["FIZEAU_API_KEY"],
  "extra_args":      ["--jobs-dir", "/output", "--job-name", "20260516T103045Z-a4c1"]
}
```

Fields:

- `task_id` (required) — TerminalBench task id; the executor resolves
  this to a local task directory and passes it to `harbor run --path`.
- `tasks_dir` (required, host path) — directory containing the task
  definitions. Bind-mounted at `/tasks` read-only. The executor probes
  `/tasks/<task_id>` first, then `/tasks/terminal-bench/<task_id>`.
- `cell_dir` (required, host path) — directory the runner already created
  for this cell. Bind-mounted at `/output` read/write. Harbor writes
  `result.json`, logs, and trajectory artifacts here.
- `harbor_plugin` (required) — Python import path passed to
  `harbor run --agent-import-path`. Sourced from
  `harness-adapters/<name> install` output.
- `image` (optional, default `fizeau-harbor-runner:latest`) — container
  image tag. The runner overrides this when running a sha-pinned image.
- `env` (optional) — env vars forwarded with `-e KEY=VALUE` into the
  container. Values containing `${VAR}` are expanded against the
  runner's environment before docker invocation and are also mirrored to
  Harbor with `--ae KEY=VALUE`. Sourced from
  `harness-adapters/<name> command` output.
- `secret_env_keys` (optional) — subset of `env` keys flagged as
  secrets; values are redacted in any record the runner persists.
- `extra_args` (optional) — passthrough trailing args appended after the
  executor's fixed Harbor flags. Used for `--jobs-dir`, `--job-name`,
  `--reps`, harbor-side timeout multipliers, etc.

## Behavior

1. The runner has already created `cell_dir` and may have written a
   `cell-state.json` sentinel.
2. The executor reads stdin, validates the spec, and invokes:

   ```
   docker run --rm \
     -v /var/run/docker.sock:/var/run/docker.sock \
     -v "$cell_dir:/output" \
     -v "$tasks_dir:/tasks:ro" \
     -e KEY1=VAL1 -e KEY2=VAL2 ... \
     <image> run \
       --yes \
       --delete \
       --path /tasks/<task_id> \
       --agent-import-path <plugin> \
       --model <env.FIZEAU_MODEL> \
       --ae KEY1=VAL1 --ae KEY2=VAL2 ... \
       <extra_args...>
   ```

3. Harbor writes job artifacts under `/output`, and the executor writes
   a top-level `result.json` summary into `cell_dir`.
4. The executor exits with docker's exit code. If Harbor crashed without
   producing a usable summary, the executor writes a stub
   `{task_id, image, harbor_plugin, exit_code, status:"missing_result"}`
   so the runner can classify the cell.

## Dry-run mode

Setting `HARBOR_TASK_EXECUTOR_DRY_RUN=1` skips `docker run` entirely;
the executor writes the would-be docker argv to `cell_dir/result.json`
as `{dry_run:true, image, task_id, harbor_plugin, docker_argv}` and
echoes the same JSON to stdout. Used by `./benchmark --plan` and
shellcheck-driven smokes.

## Validation

`./benchmark validate` calls every executor with a synthetic task-spec
under `HARBOR_TASK_EXECUTOR_DRY_RUN=1` and rejects:

- non-zero exit;
- a `cell_dir/result.json` that is not valid JSON;
- missing required spec fields surfaced as `exit 2`.

`shellcheck` (CI gate) must pass on every file in this directory.
