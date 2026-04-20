# DDX Agent

Embeddable Go agent runtime — local-model-first via LM Studio.

[![CI](https://github.com/DocumentDrivenDX/agent/actions/workflows/ci.yml/badge.svg)](https://github.com/DocumentDrivenDX/agent/actions/workflows/ci.yml)
[![Release](https://github.com/DocumentDrivenDX/agent/releases/latest/badge.svg)](https://github.com/DocumentDrivenDX/agent/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

DDX Agent is a Go library that implements a coding agent runtime — a tool-calling
LLM loop with file read/write, shell execution, navigation helpers, and
structured I/O. Designed to be embedded in
[DDx](https://github.com/DocumentDrivenDX/ddx) and other build orchestrators.
Prioritizes local model inference via LM Studio and Ollama, with transparent
escalation to cloud providers.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/DocumentDrivenDX/agent/master/install.sh | bash
```

Or with Go:

```bash
go install github.com/DocumentDrivenDX/agent/cmd/agent@latest
```

## Quick Start

Start [LM Studio](https://lmstudio.ai/) with a model that supports tool
calling (Qwen 3.5+, Llama 3.2+), then:

```bash
ddx-agent -p "Read main.go and tell me the package name"
```

DDX Agent connects to `localhost:1234` by default.

### With Anthropic

```bash
export AGENT_PROVIDER=anthropic
export AGENT_API_KEY=sk-ant-...
export AGENT_MODEL=claude-sonnet-4-20250514
ddx-agent -p "Read main.go and tell me the package name"
```

## Demos

### Read a file and explain it

[![asciicast](https://asciinema.org/a/placeholder.svg)](website/static/demos/file-read.cast)

```
$ ddx-agent -p 'Read main.go and explain what this program does'

This program is a simple HTTP server that listens on port 8080 and responds
with "Hello from DDX Agent!" to any request.
[success] tokens: 1861 in / 70 out
```

### Edit a config file

```
$ ddx-agent -p 'Read config.yaml, change the server port from 8080 to 9090, then verify'

Done. The server port in config.yaml has been changed from 8080 to 9090.
[success] tokens: 4082 in / 127 out
```

### Explore project structure

```
$ ddx-agent -p 'List all Go files and summarize the package structure'

Package Structure:
├── cmd/server/main.go       (package main)
├── internal/api/handler.go  (package api)
├── internal/api/middleware.go (package api)
└── internal/db/postgres.go  (package db)

A typical Go project with cmd/ for entry points, internal/ for private packages.
[success] tokens: 6388 in / 297 out
```

## As a Library

```go
import (
    "context"
    "fmt"

    "github.com/DocumentDrivenDX/agent"
    _ "github.com/DocumentDrivenDX/agent/configinit"
)

func main() {
    a, err := agent.New(agent.ServiceOptions{})
    if err != nil {
        panic(err)
    }
    events, err := a.Execute(context.Background(), agent.ServiceExecuteRequest{
        Prompt:   "Read main.go and tell me the package name",
        ModelRef: "cheap",
        WorkDir:  ".",
    })
    if err != nil {
        panic(err)
    }
    for event := range events {
        fmt.Println(event.Type)
    }
}
```

## Features

- **Embeddable library** — `agent.New(...).Execute(ctx, request)`, no subprocess overhead
- **Local-model-first** — LM Studio, Ollama via OpenAI-compatible API
- **Multi-provider** — OpenAI-compatible, Anthropic Claude, virtual (test replay)
- **9 built-in tools** — read, write, edit, bash, find, grep, ls, patch, task
- **Session logging** — JSONL with replay and cost tracking
- **System prompt composition** — pi-style builder with context file discovery
- **Standalone CLI** — `ddx-agent -p "prompt"` with config, logging, replay

## Configuration

```yaml
# .agent/config.yaml
providers:
  local:
    type: lmstudio
    base_url: http://localhost:1234/v1
    model: qwen3.5-7b
default: local
preset: default                  # system prompt style (see below)
max_iterations: 20
session_log_dir: .agent/sessions
tools:
  bash:
    output_filter:
      mode: off                  # off | rtk | auto
      rtk_binary: rtk
      max_bytes: 51200
```

Environment overrides: `AGENT_PROVIDER`, `AGENT_BASE_URL`, `AGENT_API_KEY`, `AGENT_MODEL`

`tools.bash.output_filter.mode: rtk` proxies allowlisted noisy commands such as
`git status` and `go test` through an installed `rtk` binary. If `rtk` is not
available, bash runs the original command and includes a fallback marker.
Built-in `read`, `find`, `grep`, and `ls` are not filtered.

## System Prompt Presets

DDX Agent ships built-in system prompt presets that describe prompt intent.
Select one with `--preset` or the
top-level `preset` config field:

| Preset      | Description                                           |
|-------------|-------------------------------------------------------|
| `default`   | Balanced, tool-aware prompt                          |
| `smart`     | Rich, thorough prompt for quality-sensitive runs      |
| `cheap`     | Pragmatic, direct prompt for latency/cost-sensitive runs |
| `minimal`   | Bare minimum — one sentence                           |
| `benchmark` | Non-interactive prompt optimized for evaluation       |

When no preset is specified, `default` is used.

## Session Replay

Every run is logged. Replay past sessions:

```bash
ddx-agent log                  # list sessions
ddx-agent replay <session-id>  # human-readable replay
```

## Documentation

- [Getting Started](https://documentdrivendx.github.io/agent/docs/getting-started/)
- [Microsite](https://documentdrivendx.github.io/agent/)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
