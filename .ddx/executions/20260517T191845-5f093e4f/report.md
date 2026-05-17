# bench-pr-A: bash runner + shell adapters + harbor-runner image + preflight

Bead: `fizeau-bfdd6a77`

## Changes

- `scripts/benchmark/benchmark`
  - Runs each cell worker in a fresh `setsid` session so the worker PID is a
    real process-group leader for signal reaping.
  - Records `BASHPID` in the in-flight registry instead of the parent shell PID.
  - Exports the worker-visible benchmark constants/functions before spawning
    the session leader.
  - Adds a real preflight checklist for `yq`, `jq`, `docker`, `setsid`,
    `flock`, writable state/lock dirs, and Harbor image buildability.
  - Passes Harbor job metadata through `task_spec.extra_args` so the executor
    can mount a stable jobs dir and job name.

- `scripts/benchmark/task-executors/harbor`
  - Resolves the local task path from `tasks_dir` and `task_id`.
  - Invokes the real Harbor CLI shape: `run --yes --delete --path ...`
    plus `--agent-import-path`, `--model`, `--ae`, and forwarded
    `extra_args`.
  - Keeps the `result.json` fallback stub when Harbor does not write one.

- `scripts/benchmark/task-executors/CONTRACT.md`
  - Updated to describe the real Harbor flags and task-path resolution.

## Verification

- `bash -n scripts/benchmark/benchmark`
- `bash -n scripts/benchmark/task-executors/harbor`
- `cd cli && go test -count=1 ./...`
- `lefthook run pre-commit`
- `./benchmark --profile noop --bench-set tb-2-1-canary --plan`
- `./benchmark --profile noop --bench-set tb-2-1-canary --reps 1 --jobs 1 --out /tmp/fizeau-bench-smoke`

## Notes

- The worktree does not contain populated TerminalBench task directories under
  `scripts/benchmark/external/terminal-bench-2`, so Harbor exits with task-path
  errors in the real smoke run. The executor now handles that case by writing a
  `missing_result` stub, which is sufficient for the benchmark runner tests in
  this repo.
