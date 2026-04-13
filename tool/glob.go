package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DocumentDrivenDX/agent"
)

const maxGlobResults = 500

// skipDirs are directories that WalkDir skips unconditionally.
var skipDirs = map[string]bool{
	".git":         true,
	".hg":          true,
	".svn":         true,
	"node_modules": true,
	"vendor":       true,
}

// GlobParams are the parameters for the glob tool.
type GlobParams struct {
	Pattern     string    `json:"pattern"`
	Dir         string    `json:"dir,omitempty"`          // base dir; defaults to WorkDir
	ExcludeDirs *[]string `json:"exclude_dirs,omitempty"` // override default skip dirs; nil uses defaults, empty slice means no skips
}

// GlobTool finds files matching a glob pattern.
type GlobTool struct {
	WorkDir     string
	ExcludeDirs []string // optional override for skipDirs; if empty, uses default skipDirs
}

func (t *GlobTool) Name() string { return "glob" }
func (t *GlobTool) Description() string {
	return "Find files matching a glob pattern. Use instead of find/ls to locate files by name."
}
func (t *GlobTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern":      {"type": "string", "description": "Glob pattern to match (e.g. '**/*.go', 'cmd/**/main.go'). Use ** to match across directories."},
			"dir":          {"type": "string", "description": "Directory to search in (relative to working directory or absolute; defaults to working directory)"},
			"exclude_dirs": {"type": "array", "items": {"type": "string"}, "description": "Override default excluded directories. By default, skips .git, .hg, .svn, node_modules, and vendor/. Set to empty array [] to search all directories."}
		},
		"required": ["pattern"]
	}`)
}

func (t *GlobTool) Execute(_ context.Context, params json.RawMessage) (string, error) {
	var p GlobParams
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("glob: invalid params: %w", err)
	}
	if p.Pattern == "" {
		return "", fmt.Errorf("glob: pattern is required")
	}

	baseDir := t.WorkDir
	if p.Dir != "" {
		baseDir = resolvePath(t.WorkDir, p.Dir)
	}

	patParts := strings.Split(filepath.ToSlash(p.Pattern), "/")

	var matches []string
	truncated := false
	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries
		}
		rel, err := filepath.Rel(baseDir, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		// Determine which dirs to skip: use ExcludeDirs if set, otherwise default skipDirs
		skipMap := skipDirs
		if p.ExcludeDirs != nil {
			skipMap = make(map[string]bool, len(*p.ExcludeDirs))
			for _, d := range *p.ExcludeDirs {
				skipMap[d] = true
			}
		}
		if d.IsDir() && skipMap[d.Name()] {
			return filepath.SkipDir
		}
		nameParts := strings.Split(filepath.ToSlash(rel), "/")
		ok, matchErr := matchParts(patParts, nameParts)
		if matchErr != nil {
			return matchErr
		}
		if ok {
			if len(matches) >= maxGlobResults {
				truncated = true
				return filepath.SkipAll
			}
			matches = append(matches, rel)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("glob: %w", err)
	}

	sort.Strings(matches)
	if len(matches) == 0 {
		return "(no matches)", nil
	}
	result := strings.Join(matches, "\n")
	if truncated {
		result += fmt.Sprintf("\n(results truncated at %d matches)", maxGlobResults)
	}
	return result, nil
}

// matchParts matches path segments nameParts against pattern segments patParts.
// "**" in patParts matches zero or more path segments.
func matchParts(pat, name []string) (bool, error) {
	for {
		if len(pat) == 0 {
			return len(name) == 0, nil
		}
		if pat[0] == "**" {
			// Consume consecutive ** tokens.
			for len(pat) > 0 && pat[0] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 0 {
				return true, nil // trailing ** matches anything
			}
			// Try matching the rest of pat against every suffix of name.
			for i := 0; i <= len(name); i++ {
				ok, err := matchParts(pat, name[i:])
				if err != nil {
					return false, err
				}
				if ok {
					return true, nil
				}
			}
			return false, nil
		}
		if len(name) == 0 {
			return false, nil
		}
		ok, err := filepath.Match(pat[0], name[0])
		if err != nil {
			return false, fmt.Errorf("invalid pattern segment %q: %w", pat[0], err)
		}
		if !ok {
			return false, nil
		}
		pat = pat[1:]
		name = name[1:]
	}
}

func (t *GlobTool) Parallel() bool { return true }

var _ agent.Tool = (*GlobTool)(nil)
