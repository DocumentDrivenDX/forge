package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLsTool_Execute(t *testing.T) {
	dir := t.TempDir()
	tl := &LsTool{WorkDir: dir}

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "subdir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "subdir", "nested.go"), []byte("package sub"), 0o644))

	t.Run("lists working directory by default", func(t *testing.T) {
		result, err := tl.Execute(context.Background(), mustJSON(t, LsParams{}))
		require.NoError(t, err)
		assert.Contains(t, result, "subdir/")
		assert.Contains(t, result, "main.go")
		assert.Contains(t, result, "README.md")
	})

	t.Run("directories appear before files", func(t *testing.T) {
		result, err := tl.Execute(context.Background(), mustJSON(t, LsParams{}))
		require.NoError(t, err)
		lines := strings.Split(result, "\n")
		// Find indices of dir and file entries.
		dirIdx, fileIdx := -1, -1
		for i, l := range lines {
			if strings.HasSuffix(l, "/") && !strings.HasSuffix(l, "./") {
				if dirIdx == -1 {
					dirIdx = i
				}
			} else if strings.HasSuffix(l, ".go") {
				if fileIdx == -1 {
					fileIdx = i
				}
			}
		}
		if dirIdx != -1 && fileIdx != -1 {
			assert.Less(t, dirIdx, fileIdx, "dirs should come before files")
		}
	})

	t.Run("lists a subdirectory", func(t *testing.T) {
		result, err := tl.Execute(context.Background(), mustJSON(t, LsParams{Path: "subdir"}))
		require.NoError(t, err)
		assert.Contains(t, result, "nested.go")
		assert.NotContains(t, result, "main.go")
	})

	t.Run("lists with absolute path", func(t *testing.T) {
		result, err := tl.Execute(context.Background(), mustJSON(t, LsParams{Path: filepath.Join(dir, "subdir")}))
		require.NoError(t, err)
		assert.Contains(t, result, "nested.go")
	})

	t.Run("empty directory", func(t *testing.T) {
		empty := filepath.Join(dir, "empty")
		require.NoError(t, os.MkdirAll(empty, 0o755))
		result, err := tl.Execute(context.Background(), mustJSON(t, LsParams{Path: "empty"}))
		require.NoError(t, err)
		assert.Equal(t, "(empty directory)", result)
	})

	t.Run("errors on nonexistent path", func(t *testing.T) {
		_, err := tl.Execute(context.Background(), mustJSON(t, LsParams{Path: "nonexistent"}))
		require.Error(t, err)
	})

	t.Run("errors on invalid params", func(t *testing.T) {
		_, err := tl.Execute(context.Background(), []byte(`{bad`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid params")
	})
}
