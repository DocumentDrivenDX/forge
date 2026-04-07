# Contributing to Forge

## Development Setup

```bash
git clone https://github.com/DocumentDrivenDX/forge.git
cd forge
make build    # build the binary
make test     # run unit tests
make check    # fmt + vet + lint + test
```

### Requirements

- Go 1.23+
- [golangci-lint](https://golangci-lint.run/) for linting
- [LM Studio](https://lmstudio.ai/) for integration tests (optional)

## Running Tests

```bash
make test                    # unit tests
make lint                    # golangci-lint
go test -tags=integration .  # integration tests (requires LM Studio)
```

Integration tests auto-discover LM Studio on `localhost:1234`, `vidar:1234`,
or `bragi:1234`. Override with `LMSTUDIO_URL` and `LMSTUDIO_MODEL`.

## Code Style

- `gofmt` is non-negotiable — CI enforces zero diff
- `go vet` must pass
- Errors must be wrapped with context: `fmt.Errorf("context: %w", err)`
- Pass `context.Context` as the first parameter for I/O functions
- Interfaces defined in the consuming package

## Making Changes

1. Create a branch or worktree (`wt switch -c my-feature`)
2. Write tests first (TDD)
3. Implement the minimum to make tests pass
4. Run `make check`
5. Commit with a descriptive message
6. Push and open a PR

## Project Structure

```
forge.go                  # Public API: Run(), Request, Result, interfaces
loop.go                   # Agent loop implementation
prompt/                   # System prompt composition
provider/openai/          # OpenAI-compatible provider (LM Studio, Ollama, etc.)
provider/anthropic/       # Anthropic Claude provider
provider/virtual/         # Dictionary replay for testing
tool/                     # Built-in tools: read, write, edit, bash
session/                  # JSONL logging, replay, cost tracking
cmd/forge/                # Standalone CLI
website/                  # Hugo/Hextra microsite
demos/                    # Demo scripts and session fixtures
```

## License

By contributing, you agree that your contributions will be licensed under the
MIT License.
