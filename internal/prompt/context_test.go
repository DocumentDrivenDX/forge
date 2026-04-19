package prompt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadContextFiles(t *testing.T) {
	// Create hierarchy:
	// root/
	//   AGENTS.md (global)
	//   project/
	//     CLAUDE.md (project-level)
	//     sub/
	//       AGENTS.md (sub-level)
	root := t.TempDir()
	project := filepath.Join(root, "project")
	sub := filepath.Join(project, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("global rules"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(project, "CLAUDE.md"), []byte("project rules"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("sub rules"), 0o644))

	t.Run("discovers files up the tree", func(t *testing.T) {
		files := LoadContextFiles(sub)
		require.GreaterOrEqual(t, len(files), 3)

		// Higher dirs first
		var contents []string
		for _, f := range files {
			contents = append(contents, f.Content)
		}
		assert.Contains(t, contents, "global rules")
		assert.Contains(t, contents, "project rules")
		assert.Contains(t, contents, "sub rules")

		// Global should come before sub
		globalIdx := -1
		subIdx := -1
		for i, f := range files {
			if f.Content == "global rules" {
				globalIdx = i
			}
			if f.Content == "sub rules" {
				subIdx = i
			}
		}
		if globalIdx >= 0 && subIdx >= 0 {
			assert.Less(t, globalIdx, subIdx, "global should appear before sub")
		}
	})

	t.Run("empty dir does not crash", func(t *testing.T) {
		empty := t.TempDir()
		files := LoadContextFiles(empty)
		// May find files from real parent dirs, but shouldn't crash
		_ = files
	})
}
