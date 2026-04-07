package tool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEditTool_Execute(t *testing.T) {
	dir := t.TempDir()
	tool := &EditTool{WorkDir: dir}

	setup := func(t *testing.T, name, content string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}

	t.Run("replaces unique string", func(t *testing.T) {
		setup(t, "edit1.txt", "hello world")

		result, err := tool.Execute(context.Background(), mustJSON(t, EditParams{
			Path:      "edit1.txt",
			OldString: "world",
			NewString: "forge",
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "Replaced 1 occurrence")

		data, err := os.ReadFile(filepath.Join(dir, "edit1.txt"))
		require.NoError(t, err)
		assert.Equal(t, "hello forge", string(data))
	})

	t.Run("errors on not found", func(t *testing.T) {
		setup(t, "edit2.txt", "hello world")

		_, err := tool.Execute(context.Background(), mustJSON(t, EditParams{
			Path:      "edit2.txt",
			OldString: "nonexistent",
			NewString: "replaced",
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("errors on ambiguous match", func(t *testing.T) {
		setup(t, "edit3.txt", "foo bar foo baz")

		_, err := tool.Execute(context.Background(), mustJSON(t, EditParams{
			Path:      "edit3.txt",
			OldString: "foo",
			NewString: "qux",
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "2 times")
	})

	t.Run("errors on empty old_string", func(t *testing.T) {
		setup(t, "edit4.txt", "content")

		_, err := tool.Execute(context.Background(), mustJSON(t, EditParams{
			Path:      "edit4.txt",
			OldString: "",
			NewString: "replaced",
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be empty")
	})

	t.Run("errors on missing file", func(t *testing.T) {
		_, err := tool.Execute(context.Background(), mustJSON(t, EditParams{
			Path:      "missing.txt",
			OldString: "foo",
			NewString: "bar",
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such file")
	})
}
