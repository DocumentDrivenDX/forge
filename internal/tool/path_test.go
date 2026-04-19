package tool

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolvePath(t *testing.T) {
	t.Run("relative path stays under workdir", func(t *testing.T) {
		dir := t.TempDir()
		tool := &WriteTool{WorkDir: dir}

		result, err := tool.Execute(context.Background(), mustJSON(t, WriteParams{
			Path:    "relative.txt",
			Content: "relative",
		}))
		require.NoError(t, err)

		expected := filepath.Join(dir, "relative.txt")
		assert.Contains(t, result, expected)

		data, err := os.ReadFile(expected)
		require.NoError(t, err)
		assert.Equal(t, "relative", string(data))
	})

	t.Run("absolute path is preserved", func(t *testing.T) {
		dir := t.TempDir()
		tool := &WriteTool{WorkDir: dir}

		absPath := filepath.Join(dir, "absolute.txt")
		result, err := tool.Execute(context.Background(), mustJSON(t, WriteParams{
			Path:    absPath,
			Content: "absolute",
		}))
		require.NoError(t, err)

		assert.Contains(t, result, absPath)
		data, err := os.ReadFile(absPath)
		require.NoError(t, err)
		assert.Equal(t, "absolute", string(data))
	})

	t.Run("relative traversal outside workdir is preserved and reported", func(t *testing.T) {
		dir := t.TempDir()
		outsideDir := t.TempDir()
		outsidePath := filepath.Join(outsideDir, "outside.txt")

		rel, err := filepath.Rel(dir, outsidePath)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(rel, ".."))

		tool := &WriteTool{WorkDir: dir}
		result, err := tool.Execute(context.Background(), mustJSON(t, WriteParams{
			Path:    rel,
			Content: "outside",
		}))
		require.NoError(t, err)
		assert.Contains(t, result, outsidePath)

		data, err := os.ReadFile(outsidePath)
		require.NoError(t, err)
		assert.Equal(t, "outside", string(data))
	})

	t.Run("chained symlinks resolve to the final target", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks are not reliably available in this environment")
		}

		dir := t.TempDir()
		realDir := filepath.Join(dir, "real")
		require.NoError(t, os.MkdirAll(realDir, 0o755))

		link2 := filepath.Join(dir, "link2")
		if err := os.Symlink(realDir, link2); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
		link1 := filepath.Join(dir, "link1")
		if err := os.Symlink(link2, link1); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}

		tool := &WriteTool{WorkDir: dir}
		result, err := tool.Execute(context.Background(), mustJSON(t, WriteParams{
			Path:    filepath.Join("link1", "nested", "target.txt"),
			Content: "chained",
		}))
		require.NoError(t, err)

		expected := filepath.Join(realDir, "nested", "target.txt")
		aliasPath := filepath.Join(dir, "link1", "nested", "target.txt")
		assert.Contains(t, result, expected)
		assert.NotContains(t, result, aliasPath)

		data, err := os.ReadFile(expected)
		require.NoError(t, err)
		assert.Equal(t, "chained", string(data))
	})
}
