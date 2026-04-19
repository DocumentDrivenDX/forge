# ddx-agent-bench

This directory contains the benchmark corpus and (gitignored) results for
`ddx-agent-bench`.

## Layout

```
bench/
  corpus/          # versioned task definitions (YAML/JSON)
  results/         # gitignored; populated by `ddx-agent-bench run`
```

## Corpus format

Each file in `bench/corpus/` is a YAML (or JSON) task:

```yaml
id: unique-task-id
description: "Human-readable description"
prompt: |
  The prompt sent to the agent.
expected_tools:        # optional; informational only
  - read
  - find
permissions: safe      # safe | supervised | unrestricted
reasoning: low         # off | low | medium | high | xhigh | max | numeric tokens
tags:
  - tool-use
```

## Usage

```sh
# List discovered candidates
ddx-agent-bench discover

# Run corpus against all candidates
ddx-agent-bench run

# Run against a specific harness only
ddx-agent-bench run --harness=claude

# Render the most recent result
ddx-agent-bench report
ddx-agent-bench report --format=markdown
```

Results are written to `bench/results/` and are **not tracked by git**.
