package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

// Template is a prompt template loaded from a markdown file.
type Template struct {
	Name        string // filename sans extension
	Description string // from frontmatter
	Content     string // body after frontmatter
	Source      string // file path
}

// LoadTemplate reads a prompt template from a file, parsing YAML frontmatter.
func LoadTemplate(path string) (*Template, error) {
	data, err := safefs.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("prompt: reading template: %w", err)
	}

	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	content := string(data)
	description := ""

	// Parse frontmatter (---\n...\n---\n)
	if strings.HasPrefix(content, "---\n") {
		end := strings.Index(content[4:], "\n---\n")
		if end >= 0 {
			frontmatter := content[4 : 4+end]
			content = strings.TrimSpace(content[4+end+5:])

			// Extract description from frontmatter (simple key: value parsing)
			for _, line := range strings.Split(frontmatter, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "description:") {
					description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
					description = strings.Trim(description, "\"'")
				}
				if strings.HasPrefix(line, "name:") {
					name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
					name = strings.Trim(name, "\"'")
				}
			}
		}
	}

	return &Template{
		Name:        name,
		Description: description,
		Content:     content,
		Source:      path,
	}, nil
}

// LoadTemplates loads all .md templates from a directory.
func LoadTemplates(dir string) ([]*Template, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("prompt: reading templates dir: %w", err)
	}

	var templates []*Template
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		t, err := LoadTemplate(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // skip broken templates
		}
		templates = append(templates, t)
	}
	return templates, nil
}

// slicePattern matches ${@:N} and ${@:N:L}
var slicePattern = regexp.MustCompile(`\$\{@:(\d+)(?::(\d+))?\}`)

// positionalPattern matches $1, $2, etc.
var positionalPattern = regexp.MustCompile(`\$(\d+)`)

// SubstituteArgs replaces argument placeholders in template content.
// Supports $1, $2 (positional), $@ and $ARGUMENTS (all args),
// ${@:N} (args from Nth onward), ${@:N:L} (L args from Nth).
// All indices are 1-based (bash convention).
func SubstituteArgs(content string, args []string) string {
	result := content

	// 1. Replace positional $1, $2, etc. FIRST (before wildcards)
	result = positionalPattern.ReplaceAllStringFunc(result, func(match string) string {
		num, _ := strconv.Atoi(match[1:])
		idx := num - 1
		if idx >= 0 && idx < len(args) {
			return args[idx]
		}
		return ""
	})

	// 2. Replace ${@:start} and ${@:start:length} (before simple $@)
	result = slicePattern.ReplaceAllStringFunc(result, func(match string) string {
		sub := slicePattern.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		start, _ := strconv.Atoi(sub[1])
		start-- // convert to 0-indexed
		if start < 0 {
			start = 0
		}
		if len(sub) >= 3 && sub[2] != "" {
			length, _ := strconv.Atoi(sub[2])
			end := start + length
			if end > len(args) {
				end = len(args)
			}
			if start >= len(args) {
				return ""
			}
			return strings.Join(args[start:end], " ")
		}
		if start >= len(args) {
			return ""
		}
		return strings.Join(args[start:], " ")
	})

	// 3. Replace $ARGUMENTS
	allArgs := strings.Join(args, " ")
	result = strings.ReplaceAll(result, "$ARGUMENTS", allArgs)

	// 4. Replace $@
	result = strings.ReplaceAll(result, "$@", allArgs)

	return result
}
