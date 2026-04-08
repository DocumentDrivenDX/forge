# Project Concerns — DDX Agent

## Area Labels

| Area | Description |
|------|-------------|
| `all` | Every bead |
| `lib` | Core library packages (agent loop, tools, providers, logging) |
| `cli` | Standalone CLI binary |

## Active Concerns

- **go-std** — Go + Standard Toolchain (areas: all)
- **testing** — Multi-layer testing with property-based, fuzz, and E2E coverage (areas: all)

## Project Overrides

### go-std

- **CLI framework**: None. DDX Agent CLI is minimal enough for `flag` stdlib. Cobra
  is not needed.
- **Test framework**: Use `testing` stdlib + `testify/assert` for assertions.
  No external test runner.
- **Structured logging**: Use `log/slog` from stdlib. No third-party logger.
- **HTTP client**: Use provider SDKs (`openai-go`, `anthropic-sdk-go`) directly.
  No custom HTTP client abstraction.

### testing

- **Property-based testing**: Use `pgregory.net/rapid` for property-based tests
  in Go. Define properties for all serialization (session log events),
  tool-call round-trips, and provider message translation.
- **Fuzz testing**: Use Go's native `testing.F` fuzz support for parsers,
  config loading, and tool input handling.
- **E2E testing**: Full agent loop E2E tests run against LM Studio with a
  loaded model (build tag `e2e`). Verify a complete file-read-and-edit
  workflow end-to-end.
- **Integration tests**: Provider integration tests against real LM Studio and
  real Anthropic API using build tags (`integration`, `e2e`).
- **Test data**: Use `rapid` generators for structured test data (Messages,
  ToolCalls, TokenUsage). Factory functions with sensible defaults for complex
  types.
- **Performance ratchets**: Track agent loop overhead (<1ms per iteration
  excluding model inference) and tool execution overhead via benchmarks.
