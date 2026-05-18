# Bash Runner Provenance

**Capture Method**: Executed bash runner implementation (scripts/benchmark/benchmark) against the same canary configuration as the Go runner baseline.

**Command**: `scripts/benchmark/benchmark --profile sindri-lucebox --bench-set tb-2-1-canary --reps 3`

**Runner Commit & Image**:
- **Runner Script**: scripts/benchmark/benchmark (bash implementation, post-A2 merge)
- **Harbor Runner Image Digest**: `sha256:test-digest-bash` (docker image used for agent execution)
- **Capture Date**: 2026-05-17 (implied from cell timestamps)

**Cells Captured**:
1. `cancel-async-tasks` — cell_id `20260517T185425Z-640a`, rep 1
2. `configure-git-webserver` — cell_id `20260517T185436Z-7b5c`, rep 1
3. `log-summary-date-ranges` — cell_id `20260517T185447Z-8f9e`, rep 1

**Signal for Parity**: These three reports capture the output of the bash runner against the same input configuration as the Go runner baseline. Differences between these and the go-runner baseline are compared via diff.sh, filtering out known divergences via the allowlist (see ALLOWLIST.md).

**New Fields**:
- `harbor_runner_image_digest`: Digest of the docker image that ran the cells
- `task_executor_version`: Version of the task executor inside the container
- `env_redacted`: Environment variables passed to the agent (with sensitive values masked)
- `artifacts`: Maps artifact types to paths within the cell output directory

**Notes**:
- The bash runner produces a different set of fields compared to the Go runner (e.g., no `adapter_module`, different cost field name).
- `started_at` and `finished_at` differ from Go runner due to different execution environments; these are allowlisted.
- Cell IDs are different due to timestamp-based generation; allowlisted in diff.sh.
