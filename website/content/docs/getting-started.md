---
title: Getting Started
weight: 1
---

## Install

```bash
go install github.com/DocumentDrivenDX/agent/cmd/ddx-agent@latest
```

## Quick Start with LM Studio

1. Start [LM Studio](https://lmstudio.ai/) and load a model with tool-calling support (e.g., Qwen 3.5).

2. Run `ddx-agent`:

```bash
ddx-agent -p "Read main.go and tell me the package name"
```

DDX Agent connects to LM Studio at `localhost:1234` by default.

## Quick Start with Anthropic

```bash
export AGENT_PROVIDER=anthropic
export AGENT_API_KEY=sk-ant-...
export AGENT_MODEL=claude-sonnet-4-20250514

ddx-agent -p "Read main.go and tell me the package name"
```

## Configuration

Create `.agent/config.yaml` in your project:

```yaml
provider: openai-compat
base_url: http://localhost:1234/v1
model: qwen3.5-7b
max_iterations: 20
session_log_dir: .agent/sessions
```

Environment variables override the config file:
- `AGENT_PROVIDER` — `openai-compat` or `anthropic`
- `AGENT_BASE_URL` — provider base URL
- `AGENT_API_KEY` — API key
- `AGENT_MODEL` — model name

## As a Library

```go
import (
    "context"
    "github.com/DocumentDrivenDX/agent"
    "github.com/DocumentDrivenDX/agent/provider/openai"
    "github.com/DocumentDrivenDX/agent/tool"
)

func main() {
    p := openai.New(openai.Config{
        BaseURL: "http://localhost:1234/v1",
        Model:   "qwen3.5-7b",
    })

    result, err := agent.Run(context.Background(), agent.Request{
        Prompt:   "Read main.go and tell me the package name",
        Provider: p,
        Tools: []agent.Tool{
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
ddx-agent log                  # list sessions
ddx-agent replay <session-id>  # human-readable replay
```
