package tool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteTool_Execute(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteTool{WorkDir: dir}

	t.Run("writes file contents", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), mustJSON(t, WriteParams{
			Path:    "output.txt",
			Content: "hello world",
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "11 bytes")

		data, err := os.ReadFile(filepath.Join(dir, "output.txt"))
		require.NoError(t, err)
		assert.Equal(t, "hello world", string(data))
	})

	t.Run("creates parent directories", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), mustJSON(t, WriteParams{
			Path:    "deep/nested/dir/file.txt",
			Content: "nested",
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "6 bytes")

		data, err := os.ReadFile(filepath.Join(dir, "deep/nested/dir/file.txt"))
		require.NoError(t, err)
		assert.Equal(t, "nested", string(data))
	})

	t.Run("overwrites existing file", func(t *testing.T) {
		path := filepath.Join(dir, "overwrite.txt")
		require.NoError(t, os.WriteFile(path, []byte("old content"), 0o644))

		_, err := tool.Execute(context.Background(), mustJSON(t, WriteParams{
			Path:    "overwrite.txt",
			Content: "new content",
		}))
		require.NoError(t, err)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "new content", string(data))
	})

	t.Run("writes empty file", func(t *testing.T) {
		_, err := tool.Execute(context.Background(), mustJSON(t, WriteParams{
			Path:    "empty.txt",
			Content: "",
		}))
		require.NoError(t, err)

		data, err := os.ReadFile(filepath.Join(dir, "empty.txt"))
		require.NoError(t, err)
		assert.Empty(t, data)
	})
}
