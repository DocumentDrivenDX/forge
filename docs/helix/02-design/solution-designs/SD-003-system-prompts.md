---
ddx:
  id: SD-003
  depends_on:
    - FEAT-001
    - helix.prd
---
# Solution Design: SD-003 — System Prompt Management

**Feature**: PRD P1-1 (System Prompt Composition)

## Scope

System prompt composition for the agent library — how callers build and
customize the system message sent to the LLM. Modeled after pi's approach
(`buildSystemPrompt`) but adapted for Go library semantics.

## Requirements Mapping

| Requirement | Technical Capability | Package | Priority |
|-------------|---------------------|---------|----------|
| PRD P1-1: System prompt composition | Composable prompt builder | `agent/prompt` | P1 |
| Base + caller additions | Section-based composition | `agent/prompt` | P1 |
| Context file loading | Load AGENTS.md/CLAUDE.md from work dir | `agent/prompt` | P1 |
| Template arg substitution | `$1`, `$@`, `${@:N}` patterns | `agent/prompt` | P1 |
| Tool-aware sections | List available tools in prompt | `agent/prompt` | P1 |
| Dynamic context injection | Date, cwd, metadata | `agent/prompt` | P1 |

## Design: Pi-Style Composable Prompt Builder

### Approach

A `prompt.Builder` that constructs a system prompt from composable sections.
The builder knows about available tools, project context files, and dynamic
values. Callers configure what they need; the builder assembles the final string.

This is a **library utility** — it helps callers construct the `SystemPrompt`
field of `agent.Request`. It does not change the core types.

### Key Design Decisions

**D1: Builder pattern, not template engine.** Pi uses string concatenation
with sections. We follow the same approach — a `Builder` with methods to add
sections, not a generic template language. Simpler, more predictable, debuggable.

**D2: Context files follow Claude Code convention.** Load `AGENTS.md` and
`CLAUDE.md` from the working directory and parent directories, same as pi.
These files provide project-specific instructions.

**D3: Prompt templates are markdown files with frontmatter.** Templates live
in `.agent/prompts/` or `~/.config/agent/prompts/`. They use `$1`, `$@`,
`${@:N}` for argument substitution (bash-style, matching pi).

**D4: Library has no opinion on base prompt content.** The library provides
the composition machinery. The CLI provides a default base prompt. Embedders
(DDx) provide their own.

### Package: `agent/prompt`

```go
// Builder constructs a system prompt from composable sections.
type Builder struct {
    base          string
    toolSection   string
    guidelines    []string
    contextFiles  []ContextFile
    appendText    string
    date          string
    workDir       string
    metadata      map[string]string
}

// ContextFile is a project instruction file (AGENTS.md, CLAUDE.md, etc.).
type ContextFile struct {
    Path    string
    Content string
}

// Template is a prompt template loaded from a file.
type Template struct {
    Name        string            // template name (filename sans extension)
    Description string            // from frontmatter
    Content     string            // template body after frontmatter
    Source      string            // file path it was loaded from
}

// New creates a Builder with a base system prompt.
func New(base string) *Builder

// WithTools adds a tools section listing available tool names and descriptions.
func (b *Builder) WithTools(tools []agent.Tool) *Builder

// WithGuidelines adds behavioral guidelines.
func (b *Builder) WithGuidelines(guidelines ...string) *Builder

// WithContextFiles adds project context files (AGENTS.md, etc.).
func (b *Builder) WithContextFiles(files []ContextFile) *Builder

// WithAppend appends additional text after all sections.
func (b *Builder) WithAppend(text string) *Builder

// WithWorkDir sets the working directory shown in the prompt.
func (b *Builder) WithWorkDir(dir string) *Builder

// WithDate sets the date shown in the prompt. Defaults to today.
func (b *Builder) WithDate(date string) *Builder

// WithMetadata adds key-value pairs shown in the prompt.
func (b *Builder) WithMetadata(key, value string) *Builder

// Build assembles and returns the final system prompt string.
func (b *Builder) Build() string

// LoadContextFiles discovers and loads AGENTS.md/CLAUDE.md from
// the working directory and its parents up to the filesystem root.
func LoadContextFiles(workDir string) []ContextFile

// LoadTemplate reads a prompt template from a file, parsing frontmatter.
func LoadTemplate(path string) (*Template, error)

// LoadTemplates loads all templates from a directory.
func LoadTemplates(dir string) ([]*Template, error)

// SubstituteArgs replaces $1, $@, ${@:N}, ${@:N:L} in template content.
func SubstituteArgs(content string, args []string) string
```

### Prompt Assembly Order

Following pi's pattern, the built prompt has these sections in order:

1. **Base prompt** — the role description and core behavior
2. **Tools section** — "Available tools:" with names and descriptions
3. **Guidelines** — behavioral rules as bullet points
4. **Append text** — caller-provided additions
5. **Project context** — content from AGENTS.md/CLAUDE.md files
6. **Dynamic context** — date, working directory, metadata

### Context File Discovery

Walk from `workDir` up to filesystem root, loading the first
`AGENTS.md` or `CLAUDE.md` found at each level. This matches Claude Code's
behavior. Files higher in the tree appear first (global before project-specific).

### Template Arg Substitution

Matching pi exactly:
- `$1`, `$2`, ... — positional args
- `$@` or `$ARGUMENTS` — all args joined with spaces
- `${@:N}` — args from Nth onward (1-indexed)
- `${@:N:L}` — L args starting from Nth

## Presets

DDX Agent ships with built-in system prompt presets that track the style and conventions of well-known coding agents:

| Preset | Description |
|--------|-------------|
| `ddx-agent` | DDX Agent default — balanced, tool-aware, structured output |
| `minimal` | Bare minimum — one sentence, like pi |
| `claude` | Tracks Claude Code style — thorough, safety-conscious |
| `codex` | Tracks OpenAI Codex CLI style — pragmatic, direct |
| `cursor` | Tracks Cursor style — fast, action-oriented |

### Boundary with Model Catalog

Prompt presets are strictly about system prompt behavior. They do **not**
select providers, model aliases, tiers/profiles, or canonical model targets.
Those belong to the agent model catalog and routing/config layer described in
FEAT-004 and SD-005.

This boundary is important because agent already uses `preset` in CLI/config
surfaces. Future model-policy surfaces must use distinct terms such as
`model_ref`, `profile`, or `alias`, never `preset`.

### Using Presets

```go
// Load a preset
preset := prompt.NewFromPreset("claude")

// Apply tools and context
preset.WithTools(tools)
.WithContextFiles(prompt.LoadContextFiles(workDir))

// Build the system prompt
sysPrompt := preset.Build()

// Use in agent.Request
req := agent.Request{
    SystemPrompt: sysPrompt,
    // ...
}
```

### Preset API

```go
// PresetNames returns all available preset names in stable order.
func PresetNames() []string

// GetPreset returns a preset by name, or the agent default if not found.
func GetPreset(name string) Preset

// NewFromPreset creates a Builder initialized from a named preset.
func NewFromPreset(name string) *Builder
```

## Traceability

| Requirement | Component | Test Strategy |
|-------------|-----------|---------------|
| Composable builder | prompt.Builder | Unit: build with various section combos |
| Context file loading | LoadContextFiles | Unit: temp dir hierarchy with AGENTS.md |
| Template loading | LoadTemplate | Unit: parse frontmatter + content |
| Arg substitution | SubstituteArgs | Unit: all patterns ($1, $@, ${@:N:L}) |
| Tool section | WithTools | Unit: tools listed in output |
| Presets | prompt.NewFromPreset | Unit: build from each preset name |

## Risks

| Risk | Prob | Impact | Mitigation |
|------|------|--------|------------|
| Context files too large | L | M | Document size limits; truncation in future |
| Template syntax conflicts | L | L | Use pi's exact syntax for compatibility |
