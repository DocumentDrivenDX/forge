---
title: Getting Started
weight: 1
---

## Install

```bash
go install github.com/anthropics/forge/cmd/forge@latest
```

## Quick Start with LM Studio

1. Start [LM Studio](https://lmstudio.ai/) and load a model with tool-calling support (e.g., Qwen 3.5).

2. Run forge:

```bash
forge -p "Read main.go and tell me the package name"
```

Forge connects to LM Studio at `localhost:1234` by default.

## Quick Start with Anthropic

```bash
export FORGE_PROVIDER=anthropic
export FORGE_API_KEY=sk-ant-...
export FORGE_MODEL=claude-sonnet-4-20250514

forge -p "Read main.go and tell me the package name"
```

## Configuration

Create `.forge/config.yaml` in your project:

```yaml
provider: openai-compat
base_url: http://localhost:1234/v1
model: qwen3.5-7b
max_iterations: 20
session_log_dir: .forge/sessions
```

Environment variables override the config file:
- `FORGE_PROVIDER` — `openai-compat` or `anthropic`
- `FORGE_BASE_URL` — provider base URL
- `FORGE_API_KEY` — API key
- `FORGE_MODEL` — model name

## As a Library

```go
import (
    "context"
    "github.com/anthropics/forge"
    "github.com/anthropics/forge/provider/openai"
    "github.com/anthropics/forge/tool"
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
    // result.Output contains the agent's response
}
```

## Session Replay

Every run is logged. Replay past sessions:

```bash
forge log                    # list sessions
forge replay <session-id>   # human-readable replay
```
