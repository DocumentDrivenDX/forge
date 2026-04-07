# Forge

Embeddable Go agent runtime — local-model-first via LM Studio.

[![CI](https://github.com/DocumentDrivenDX/forge/actions/workflows/ci.yml/badge.svg)](https://github.com/DocumentDrivenDX/forge/actions/workflows/ci.yml)
[![Release](https://github.com/DocumentDrivenDX/forge/releases/latest/badge.svg)](https://github.com/DocumentDrivenDX/forge/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Forge is a Go library that implements a coding agent runtime — a tool-calling
LLM loop with file read/write, shell execution, and structured I/O. Designed
to be embedded in [DDx](https://github.com/DocumentDrivenDX/ddx) and other
build orchestrators. Prioritizes local model inference via LM Studio and Ollama,
with transparent escalation to cloud providers.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/DocumentDrivenDX/forge/master/install.sh | bash
```

Or with Go:

```bash
go install github.com/DocumentDrivenDX/forge/cmd/forge@latest
```

## Quick Start

Start [LM Studio](https://lmstudio.ai/) with a model that supports tool
calling (Qwen 3.5+, Llama 3.2+), then:

```bash
forge -p "Read main.go and tell me the package name"
```

Forge connects to `localhost:1234` by default.

### With Anthropic

```bash
export FORGE_PROVIDER=anthropic
export FORGE_API_KEY=sk-ant-...
export FORGE_MODEL=claude-sonnet-4-20250514
forge -p "Read main.go and tell me the package name"
```

## Demos

### Read a file and explain it

[![asciicast](https://asciinema.org/a/placeholder.svg)](website/static/demos/file-read.cast)

```
$ forge -p 'Read main.go and explain what this program does'

This program is a simple HTTP server that listens on port 8080 and responds
with "Hello from Forge!" to any request.
[success] tokens: 1861 in / 70 out
```

### Edit a config file

```
$ forge -p 'Read config.yaml, change the server port from 8080 to 9090, then verify'

Done. The server port in config.yaml has been changed from 8080 to 9090.
[success] tokens: 4082 in / 127 out
```

### Explore project structure

```
$ forge -p 'List all Go files and summarize the package structure'

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

    "github.com/DocumentDrivenDX/forge"
    "github.com/DocumentDrivenDX/forge/provider/openai"
    "github.com/DocumentDrivenDX/forge/tool"
)

func main() {
    p := openai.New(openai.Config{
        BaseURL: "http://localhost:1234/v1",
        Model:   "qwen3.5-7b",
    })

    result, err := forge.Run(context.Background(), forge.Request{
        Prompt:   "Read main.go and tell me the package name",
        Provider: p,
        Tools: []forge.Tool{
            &tool.ReadTool{WorkDir: "."},
            &tool.BashTool{WorkDir: "."},
        },
        MaxIterations: 10,
    })
    if err != nil {
        panic(err)
    }
    fmt.Println(result.Output)
    fmt.Printf("Tokens: %d in / %d out, Cost: $%.4f\n",
        result.Tokens.Input, result.Tokens.Output, result.CostUSD)
}
```

## Features

- **Embeddable library** — `forge.Run(ctx, request)`, no subprocess overhead
- **Local-model-first** — LM Studio, Ollama via OpenAI-compatible API
- **Multi-provider** — OpenAI-compatible, Anthropic Claude, virtual (test replay)
- **4 built-in tools** — read, write, edit, bash
- **Session logging** — JSONL with replay and cost tracking
- **System prompt composition** — pi-style builder with context file discovery
- **Standalone CLI** — `forge -p "prompt"` with config, logging, replay

## Configuration

```yaml
# .forge/config.yaml
provider: openai-compat
base_url: http://localhost:1234/v1
model: qwen3.5-7b
max_iterations: 20
session_log_dir: .forge/sessions
```

Environment overrides: `FORGE_PROVIDER`, `FORGE_BASE_URL`, `FORGE_API_KEY`, `FORGE_MODEL`

## Session Replay

Every run is logged. Replay past sessions:

```bash
forge log                    # list sessions
forge replay <session-id>   # human-readable replay
```

## Documentation

- [Getting Started](https://documentdrivendx.github.io/forge/docs/getting-started/)
- [Microsite](https://documentdrivendx.github.io/forge/)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
