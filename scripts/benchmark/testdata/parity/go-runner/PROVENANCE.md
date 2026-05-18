# Go Runner Baseline Provenance

**Capture Method**: Executed against Go runner baseline (cmd/bench/matrix.go) with canary configuration.

**Command**: `fiz-bench matrix --profile sindri-lucebox --bench-set tb-2-1-canary --reps 3`

**Runner Commit**: The Go runner was the active implementation at the time of baseline capture.
- **Runner Ref**: cmd/bench/matrix.go (pre-deactivation, prior to A3 merge)
- **Host**: CI or test environment where Go runner was invoked
- **Capture Date**: 2026-05-17 (implied from cell timestamps)

**Cells Captured**:
1. `cancel-async-tasks` — cell_id `20260517T185308Z-a08d`, rep 1
2. `configure-git-webserver` — cell_id `20260517T185319Z-6f7f`, rep 1
3. `log-summary-date-ranges` — cell_id `20260517T185330Z-abcd`, rep 1

**Baseline Signal**: These three reports represent the canonical output of the Go runner against the sindri-lucebox profile and terminal-bench-2-1 canary dataset. All fields, structure, and values in these reports are the reference for detecting parity drift in the bash runner.

**Notes**:
- The go-runner output includes fields like `adapter_module`, `harbor_agent`, `profile_id`, `profile_path`, `profile_snapshot`, which are Go-runner-specific and will not appear in bash-runner output.
- Field names and structure follow the Go runner's JSON serialization (e.g., `cost_usd`, `output_tokens`, `input_tokens`).
- Timestamps and cell IDs will differ between runs due to execution time; these are allowlisted in the diff tool.
