package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadTool_Execute(t *testing.T) {
	dir := t.TempDir()
	tool := &ReadTool{WorkDir: dir}

	t.Run("reads file contents", func(t *testing.T) {
		path := filepath.Join(dir, "hello.txt")
		require.NoError(t, os.WriteFile(path, []byte("hello world\n"), 0o644))

		result, err := tool.Execute(context.Background(), mustJSON(t, ReadParams{Path: "hello.txt"}))
		require.NoError(t, err)
		assert.Equal(t, "hello world\n", result)
	})

	t.Run("reads absolute path", func(t *testing.T) {
		path := filepath.Join(dir, "abs.txt")
		require.NoError(t, os.WriteFile(path, []byte("absolute"), 0o644))

		result, err := tool.Execute(context.Background(), mustJSON(t, ReadParams{Path: path}))
		require.NoError(t, err)
		assert.Equal(t, "absolute", result)
	})

	t.Run("errors on missing file", func(t *testing.T) {
		_, err := tool.Execute(context.Background(), mustJSON(t, ReadParams{Path: "nonexistent.txt"}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such file")
	})

	t.Run("errors on binary content", func(t *testing.T) {
		path := filepath.Join(dir, "binary.bin")
		require.NoError(t, os.WriteFile(path, []byte{0x00, 0xFF, 0xFE, 0x01}, 0o644))

		_, err := tool.Execute(context.Background(), mustJSON(t, ReadParams{Path: "binary.bin"}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "binary")
	})

	t.Run("supports offset and limit", func(t *testing.T) {
		path := filepath.Join(dir, "lines.txt")
		require.NoError(t, os.WriteFile(path, []byte("line0\nline1\nline2\nline3\nline4"), 0o644))

		result, err := tool.Execute(context.Background(), mustJSON(t, ReadParams{
			Path:   "lines.txt",
			Offset: 1,
			Limit:  2,
		}))
		require.NoError(t, err)
		assert.Equal(t, "line1\nline2", result)
	})

	t.Run("offset beyond file length returns empty", func(t *testing.T) {
		path := filepath.Join(dir, "short.txt")
		require.NoError(t, os.WriteFile(path, []byte("one\ntwo"), 0o644))

		result, err := tool.Execute(context.Background(), mustJSON(t, ReadParams{
			Path:   "short.txt",
			Offset: 100,
		}))
		require.NoError(t, err)
		assert.Equal(t, "", result)
	})

	t.Run("errors on invalid params", func(t *testing.T) {
		_, err := tool.Execute(context.Background(), json.RawMessage(`{invalid`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid params")
	})
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}
