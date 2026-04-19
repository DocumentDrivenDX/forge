package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/DocumentDrivenDX/agent"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

const (
	maxGrepResults  = 200
	maxGrepFileSize = 1 << 20 // 1 MB; skip larger files
)

// GrepParams are the parameters for the grep tool.
type GrepParams struct {
	Pattern         string    `json:"pattern"`
	Dir             string    `json:"dir,omitempty"`
	Glob            string    `json:"glob,omitempty"`
	CaseInsensitive bool      `json:"case_insensitive,omitempty"`
	ExcludeDirs     *[]string `json:"exclude_dirs,omitempty"` // override default skip dirs; nil uses defaults, empty slice means no skips
}

// GrepTool searches file contents for a regex pattern.
type GrepTool struct {
	WorkDir     string
	ExcludeDirs []string // optional override for skipDirs; if empty, uses default skipDirs
}

func (t *GrepTool) Name() string { return "grep" }
func (t *GrepTool) Description() string {
	return "Search file contents for a regex pattern. Use instead of grep/rg shell commands."
}
func (t *GrepTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern":          {"type": "string", "description": "Regular expression to search for"},
			"dir":              {"type": "string", "description": "Directory to search in (relative or absolute; defaults to working directory)"},
			"glob":             {"type": "string", "description": "Restrict to files whose base names match this glob (e.g. '*.go', '*.ts')"},
			"case_insensitive": {"type": "boolean", "description": "Case-insensitive matching (default false)"},
			"exclude_dirs":     {"type": "array", "items": {"type": "string"}, "description": "Override default excluded directories. By default, skips .git, .hg, .svn, node_modules, and vendor/. Set to empty array [] to search all directories."}
		},
		"required": ["pattern"]
	}`)
}

func (t *GrepTool) Execute(_ context.Context, params json.RawMessage) (string, error) {
	var p GrepParams
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("grep: invalid params: %w", err)
	}
	if p.Pattern == "" {
		return "", fmt.Errorf("grep: pattern is required")
	}

	reStr := p.Pattern
	if p.CaseInsensitive {
		reStr = "(?i)" + reStr
	}
	re, err := regexp.Compile(reStr)
	if err != nil {
		return "", fmt.Errorf("grep: invalid pattern: %w", err)
	}

	baseDir := t.WorkDir
	if p.Dir != "" {
		baseDir = resolvePath(t.WorkDir, p.Dir)
	}

	type grepMatch struct {
		file    string
		lineNum int
		line    string
	}
	var matches []grepMatch
	truncated := false

	err = filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			// Determine which dirs to skip: use ExcludeDirs if set, otherwise default skipDirs
			skipMap := skipDirs
			if p.ExcludeDirs != nil {
				skipMap = make(map[string]bool, len(*p.ExcludeDirs))
				for _, d := range *p.ExcludeDirs {
					skipMap[d] = true
				}
			}
			if skipMap[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		// Apply optional filename glob filter.
		if p.Glob != "" {
			ok, globErr := filepath.Match(p.Glob, filepath.Base(path))
			if globErr != nil {
				return fmt.Errorf("grep: invalid glob: %w", globErr)
			}
			if !ok {
				return nil
			}
		}

		// Skip large files.
		info, infoErr := d.Info()
		if infoErr != nil || info.Size() > maxGrepFileSize {
			return nil
		}

		rel, _ := filepath.Rel(baseDir, path)

		data, readErr := safefs.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if !utf8.Valid(data) {
			return nil // skip binary files
		}

		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				if len(matches) >= maxGrepResults {
					truncated = true
					return nil
				}
				matches = append(matches, grepMatch{file: rel, lineNum: lineNum, line: line})
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}

	if len(matches) == 0 {
		return "(no matches)", nil
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].file != matches[j].file {
			return matches[i].file < matches[j].file
		}
		return matches[i].lineNum < matches[j].lineNum
	})

	var sb strings.Builder
	for _, m := range matches {
		fmt.Fprintf(&sb, "%s:%d:%s\n", m.file, m.lineNum, m.line)
	}
	if truncated {
		fmt.Fprintf(&sb, "(results truncated at %d matches)\n", maxGrepResults)
	}
	return TruncateHead(strings.TrimRight(sb.String(), "\n"), truncMaxLines, truncMaxBytes), nil
}

func (t *GrepTool) Parallel() bool { return true }

var _ agent.Tool = (*GrepTool)(nil)
