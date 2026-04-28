// Package termbench is the TerminalBench external-benchmark adapter.
//
// Responsibilities:
//
//  1. Parse a TerminalBench task directory (task.yaml + instruction +
//     tests/) into a typed Task.
//  2. Translate Task → an agent ServiceExecuteRequest payload (prompt,
//     work-dir, timeout). The adapter does NOT execute the task itself —
//     it returns a Plan that the caller (cmd/bench) feeds into the agent
//     service.Execute pipeline.
//  3. Capture the agent's stream of harnesses.Event into the trajectory
//     output format that TerminalBench's Harbor grader expects, namely
//     ATIF v1.4 written to <out>/logs/agent/trajectory.json. The grader
//     then runs the task's own pytest test_outputs.py against the modified
//     workspace; we do not reimplement that grading.
//
// What this package deliberately does NOT do:
//
//   - It does NOT spin up Docker containers, install Harbor, or run
//     pytest. Doing so requires the full Harbor stack (`harbor run …`)
//     and an x86_64 Docker daemon. Instead, the adapter emits the
//     artifacts Harbor expects so the upstream grader can score the run
//     unmodified. See docs/helix/02-design/external-benchmarks.md for
//     the end-to-end pipeline (agent → harness output → harbor grader).
//
//   - It does NOT reimplement TerminalBench's grading rubric. Reward
//     comes from /logs/verifier/reward.txt produced by Harbor's verifier.
//
// See SD-008 (docs/helix/02-design/solution-designs/SD-008-terminal-bench-integration.md)
// for the integration audit; this package is the Go-side companion to the
// Python adapter at scripts/benchmark/harbor_agent.py.
package termbench
