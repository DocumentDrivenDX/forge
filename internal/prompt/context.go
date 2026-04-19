package prompt

import (
	"path/filepath"

	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

// contextFilenames are the filenames to look for as project context files.
var contextFilenames = []string{"AGENTS.md", "CLAUDE.md"}

// LoadContextFiles discovers and loads project instruction files (AGENTS.md,
// CLAUDE.md) from workDir and each parent directory up to the filesystem root.
// Files higher in the tree appear first (global before project-specific).
func LoadContextFiles(workDir string) []ContextFile {
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil
	}

	var files []ContextFile
	seen := make(map[string]bool)

	dir := absDir
	for {
		for _, name := range contextFilenames {
			path := filepath.Join(dir, name)
			if seen[path] {
				continue
			}
			data, err := safefs.ReadFile(path)
			if err != nil {
				continue
			}
			seen[path] = true
			// Prepend so higher dirs come first
			files = append([]ContextFile{{Path: path, Content: string(data)}}, files...)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached root
		}
		dir = parent
	}

	return files
}
