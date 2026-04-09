---
ddx:
  id: SD-008
  bead: agent-a8bf4d0b
  created: 2026-04-08
---
# Solution Design: SD-008 — Terminal-Bench / Harbor Integration Path Audit

**Bead**: agent-a8bf4d0b (Audit Terminal-Bench and Harbor integration path for ddx-agent)
**Type**: Spike / design note
**Status**: Complete — findings checked in, downstream beads should reference this document

---

## Summary

This document records findings from an audit of Terminal-Bench v2.0 and the Harbor
evaluation framework as the target benchmark harness for ddx-agent. It answers
the five acceptance-criteria questions and recommends a concrete integration path.

---

## 1. Confirmed Supported Integration Path

**Terminal-Bench v2.0 uses Harbor as its official evaluation framework.**
The previous ad-hoc harness from v1.x is deprecated.

Harbor supports two agent types:

| Type | Description |
|------|-------------|
| `BaseInstalledAgent` | CLI agent installed inside the task container |
| `BaseDockerAgent` | Agent running in a sidecar container alongside the task env |

**Recommended path for ddx-agent: `BaseInstalledAgent`.**

The `BaseInstalledAgent` adapter is a small Python class that Harbor uses to:
1. Install the agent binary into the task container (`install()` hook)
2. Run the agent with the task instruction (`run()` hook)
3. Collect the trajectory artifact after execution (`populate_context_post_run()` hook)

For ddx-agent, the `run()` hook would invoke something like:
```bash
/usr/local/bin/ddx-agent --json --preset codex -p "<task_instruction>"
```

The Python adapter file lives in a Harbor-compatible agents repo and is referenced
by name in job configs:
```yaml
agents:
  - name: ddx-agent
    version: "1.0"
```

**No modification to ddx-agent's current CLI interface is required** to support
the installed-agent path. The `--json` + `--preset codex` + `-p` invocation already
matches Harbor's expected agent invocation model.

A thin Python wrapper (~80 lines) implementing `BaseInstalledAgent` is the only
new artifact needed for integration. This is tracked in bead `agent-a3ce467a`.

---

## 2. Container / Runtime Constraints

- **Base OS**: Debian Linux (most Terminal-Bench task images derive from `debian:bookworm` or a toolchain image built on it)
- **Architecture**: amd64 — Harbor cloud runtimes (Daytona, Modal, E2B, Runloop) are x86_64. Local Docker evaluation is architecture-native.
- **Agent binary**: The Go binary must be compiled for `linux/amd64`. The static Go binary produced by `GOARCH=amd64 GOOOS=linux go build` drops directly into the container.
- **No internet access from task container**: Task environments are isolated. The agent's LLM calls must route to a provider reachable from inside the container (cloud API via HTTPS, or a forwarded local endpoint).
- **Filesystem**: The task container has a pre-populated workspace directory. File operations are scoped there. ddx-agent's `--work-dir` flag controls the root.
- **Timeout**: Per-task time limits are enforced by Harbor (typically 10–30 minutes). The agent loop's `max_iterations` provides a secondary guard.
- **No persistent state between tasks**: Each trial is an isolated container. ddx-agent session logs written to `/logs/agent/` are collected by Harbor.
- **User context**: Harbor runs agents as an `agent` user (configurable per task.toml). ddx-agent does not require root.

---

## 3. Credential Injection

Harbor injects credentials as environment variables into the task container trial.
The Harbor job config specifies which env vars to pass through:

```python
# In the Python adapter
def get_env(self) -> dict:
    return {
        "ANTHROPIC_API_KEY": os.environ["ANTHROPIC_API_KEY"],
        "OPENROUTER_API_KEY": os.environ.get("OPENROUTER_API_KEY", ""),
    }
```

**ddx-agent config approach for benchmark runs**: ddx-agent currently reads
its provider configuration from a YAML config file (`~/.config/agent/config.yaml`
or `.agent/config.yaml` in the working dir). For benchmark use, the recommended
approach is to ship a minimal config file in the adapter's `install()` hook
that references an env-var-expanded API key:

```yaml
# Installed at ~/.config/agent/config.yaml inside the container
providers:
  benchmark:
    type: anthropic
    api_key: "${ANTHROPIC_API_KEY}"
    model: claude-haiku-4-5-20251001
default_provider: benchmark
```

ddx-agent's config loader already supports `${ENV_VAR}` expansion (confirmed
in `config/config.go`). No changes needed for credential injection.

**Approved approach**: env-var injection via Harbor's `get_env()` adapter method,
with a bootstrapped config file written to the container during `install()`.

---

## 4. Result Artifact Location and Schema

Harbor expects result artifacts in specific container paths:

| Path | Content | Schema |
|------|---------|--------|
| `/logs/verifier/reward.txt` | Task reward: `1` (passed) or `0` (failed) | Single integer |
| `/logs/verifier/ctrf.json` | Test results (pytest CTRF format) | CTRF v1 JSON |
| `/logs/agent/trajectory.json` | Agent trajectory (ATIF v1.4) | ATIF JSON |
| `/logs/verifier/test_output.log` | Raw pytest output | Plain text |

**ddx-agent's session log** (JSONL format in `.agent/session-logs/`) is NOT
the trajectory format Harbor expects. The Python adapter's
`populate_context_post_run()` hook must convert ddx-agent's JSONL session log
to ATIF v1.4 format and write it to `/logs/agent/trajectory.json`.

**ATIF v1.4 minimal schema** (sufficient for Terminal-Bench scoring):
```json
{
  "schema_version": "1.4",
  "session_id": "<uuid>",
  "agent": { "name": "ddx-agent", "version": "1.0", "model_name": "<model>" },
  "steps": [
    {
      "step_id": 1,
      "timestamp": "<RFC3339>",
      "source": "user|agent|system",
      "message": "<content>",
      "tool_calls": [],
      "metrics": { "input_tokens": 0, "output_tokens": 0, "cost": 0 }
    }
  ],
  "final_metrics": { "total_input_tokens": 0, "total_output_tokens": 0, "total_cost": 0 }
}
```

ddx-agent's `--json` output already includes `session_id`, model name, token
counts, and a full account of tool calls. The adapter conversion is
straightforward mapping.

**Reward determination**: Terminal-Bench tasks ship their own test scripts.
The Harbor verifier runs `pytest --ctrf /logs/verifier/ctrf.json tests/test_outputs.py`
against the modified workspace after the agent exits. ddx-agent does not need
to produce the reward — it just needs to complete the task and exit cleanly.
A non-zero exit code from ddx-agent is treated as a trial failure.

---

## 5. Recommended Smoke-Run Command Path

**Local Docker smoke run** (before cloud evaluation):

```bash
# Step 1: Build ddx-agent for Linux amd64
GOOS=linux GOARCH=amd64 go build -o dist/ddx-agent-linux-amd64 ./cmd/ddx-agent

# Step 2: Install Harbor and Terminal-Bench dataset
pip install harbor-framework
harbor dataset pull terminal-bench/terminal-bench-2

# Step 3: Register the ddx-agent adapter
# (adapter lives at scripts/benchmark/harbor_agent.py — tracked in agent-a3ce467a)

# Step 4: Smoke run against a single task
harbor run \
  --dataset terminal-bench/terminal-bench-2 \
  --agent ddx-agent \
  --task-filter "hello-world" \
  --runtime docker

# Step 5: Check results
cat ~/.harbor/jobs/<job-id>/trials/*/verifier/reward.txt
```

**Verification that a run is valid**: A completed run produces:
- `reward.txt` containing `1` or `0`
- `trajectory.json` with at least one step
- `trial_result.json` with `status: passed|failed|timeout`

---

## Key Findings and Decisions

| Question | Finding | Decision |
|----------|---------|----------|
| Integration type | `BaseInstalledAgent` is the right path | Use installed-agent adapter |
| ddx-agent interface changes needed | None — existing CLI flags sufficient | No changes for basic integration |
| Credential injection | Harbor env-var passthrough + bootstrapped config file | Env-var expansion in config (already supported) |
| Trajectory format | ddx-agent JSONL != ATIF v1.4; conversion needed | Adapter handles conversion in `populate_context_post_run()` |
| Binary portability | Static Go binary for `linux/amd64` | `GOOS=linux GOARCH=amd64` in build step |
| Smoke run | `harbor run --dataset terminal-bench/terminal-bench-2 --agent ddx-agent -n 1` | Defined in scripts/benchmark/ |

---

## Downstream References

Beads that depend on this audit:

- `agent-a3ce467a` — Implement the installed-agent Python adapter and smoke-run workflow
- `agent-82042311` — Specify benchmark mode and evaluation plan (consumes §1, §3, §4)
- `agent-1192db7b` — Capture baseline (consumes §5 for run methodology)
- `agent-5f35fdeb` — Benchmark-mode preset (no new CLI changes needed per §1)
