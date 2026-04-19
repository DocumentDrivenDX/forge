// Package prompt provides composable system prompt construction for agent.
// Modeled after pi's buildSystemPrompt — section-based composition with
// tool awareness, project context files, and dynamic context injection.
package prompt

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent"
)

// ContextFile is a project instruction file (AGENTS.md, CLAUDE.md, etc.).
type ContextFile struct {
	Path    string
	Content string
}

// Builder constructs a system prompt from composable sections.
type Builder struct {
	base         string
	tools        []agent.Tool
	guidelines   []string
	sections     []namedSection
	contextFiles []ContextFile
	appendText   string
	date         string
	workDir      string
	metadata     map[string]string
}

// namedSection is a caller-injected section with a heading and body.
type namedSection struct {
	name string
	body string
}

// New creates a Builder with a base system prompt.
func New(base string) *Builder {
	return &Builder{
		base:     base,
		metadata: make(map[string]string),
	}
}

// WithTools adds a tools section listing available tool names and descriptions.
func (b *Builder) WithTools(tools []agent.Tool) *Builder {
	b.tools = tools
	return b
}

// WithGuidelines adds behavioral guidelines.
func (b *Builder) WithGuidelines(guidelines ...string) *Builder {
	b.guidelines = append(b.guidelines, guidelines...)
	return b
}

// WithContextFiles adds project context files (AGENTS.md, etc.).
func (b *Builder) WithContextFiles(files []ContextFile) *Builder {
	b.contextFiles = files
	return b
}

// WithSection adds a named section to the system prompt. Callers (like DDx)
// use this to inject workflow-specific guidance (bead contracts, commit rules,
// evidence requirements) into the system prompt rather than only in the user
// message. Sections appear after guidelines and before project context.
func (b *Builder) WithSection(name, body string) *Builder {
	b.sections = append(b.sections, namedSection{name: name, body: body})
	return b
}

// WithAppend appends additional text after all sections.
func (b *Builder) WithAppend(text string) *Builder {
	b.appendText = text
	return b
}

// WithWorkDir sets the working directory shown in the prompt.
func (b *Builder) WithWorkDir(dir string) *Builder {
	b.workDir = dir
	return b
}

// WithDate sets the date shown in the prompt. Defaults to today.
func (b *Builder) WithDate(date string) *Builder {
	b.date = date
	return b
}

// WithMetadata adds a key-value pair shown in the prompt.
func (b *Builder) WithMetadata(key, value string) *Builder {
	b.metadata[key] = value
	return b
}

// Build assembles and returns the final system prompt string.
func (b *Builder) Build() string {
	var sections []string

	// 1. Base prompt
	if b.base != "" {
		sections = append(sections, b.base)
	}

	// 2. Tools section
	if len(b.tools) > 0 {
		var toolLines []string
		for _, t := range b.tools {
			toolLines = append(toolLines, fmt.Sprintf("- %s: %s", t.Name(), t.Description()))
		}
		sections = append(sections, "# Tools\n"+strings.Join(toolLines, "\n"))
	}

	// 3. Guidelines
	if len(b.guidelines) > 0 {
		var guideLines []string
		for _, g := range b.guidelines {
			guideLines = append(guideLines, "- "+g)
		}
		sections = append(sections, "Guidelines:\n"+strings.Join(guideLines, "\n"))
	}

	// 4. Named sections (caller-injected, e.g. DDx workflow rules)
	for _, s := range b.sections {
		sections = append(sections, fmt.Sprintf("# %s\n\n%s", s.name, s.body))
	}

	// 5. Append text
	if b.appendText != "" {
		sections = append(sections, b.appendText)
	}

	// 7. Project context files
	if len(b.contextFiles) > 0 {
		var contextParts []string
		contextParts = append(contextParts, "# Project Context\n\nProject-specific instructions and guidelines:")
		// Sort context files by path for stable output
		sortedFiles := make([]ContextFile, len(b.contextFiles))
		copy(sortedFiles, b.contextFiles)
		sort.Slice(sortedFiles, func(i, j int) bool {
			return sortedFiles[i].Path < sortedFiles[j].Path
		})
		for _, cf := range sortedFiles {
			contextParts = append(contextParts, fmt.Sprintf("## %s\n\n%s", cf.Path, cf.Content))
		}
		sections = append(sections, strings.Join(contextParts, "\n\n"))
	}

	// 8. Dynamic context
	date := b.date
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	dynamic := fmt.Sprintf("Current date: %s", date)
	if b.workDir != "" {
		dynamic += fmt.Sprintf("\nCurrent working directory: %s", b.workDir)
	}
	// Sort metadata keys for stable output
	metaKeys := make([]string, 0, len(b.metadata))
	for k := range b.metadata {
		metaKeys = append(metaKeys, k)
	}
	sort.Strings(metaKeys)
	for _, k := range metaKeys {
		dynamic += fmt.Sprintf("\n%s: %s", k, b.metadata[k])
	}
	sections = append(sections, dynamic)

	return strings.Join(sections, "\n\n")
}
