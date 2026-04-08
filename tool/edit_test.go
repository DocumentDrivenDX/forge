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

	readFile := func(t *testing.T, name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		return string(data)
	}

	// --- Single-edit (legacy) backward compat ---

	t.Run("single edit replaces unique string", func(t *testing.T) {
		setup(t, "edit1.txt", "hello world")

		result, err := tool.Execute(context.Background(), mustJSON(t, EditParams{
			Path:      "edit1.txt",
			OldString: "world",
			NewString: "agent",
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "Replaced 1 occurrence")
		assert.Equal(t, "hello agent", readFile(t, "edit1.txt"))
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
		// Empty old_string with no edits[] → "required" error
		assert.Contains(t, err.Error(), "required")
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

	// --- Multi-edit (pi-style edits[] array) ---

	t.Run("multi-edit applies disjoint edits", func(t *testing.T) {
		setup(t, "multi1.go", "package main\n\nfunc hello() {}\n\nfunc goodbye() {}\n")

		result, err := tool.Execute(context.Background(), mustJSON(t, EditParams{
			Path: "multi1.go",
			Edits: []EditEntry{
				{OldText: "func hello() {}", NewText: "func hello() string { return \"hi\" }"},
				{OldText: "func goodbye() {}", NewText: "func goodbye() string { return \"bye\" }"},
			},
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "Applied 2 edits")

		content := readFile(t, "multi1.go")
		assert.Contains(t, content, `return "hi"`)
		assert.Contains(t, content, `return "bye"`)
		assert.Contains(t, content, "package main")
	})

	t.Run("multi-edit preserves content between edits", func(t *testing.T) {
		setup(t, "multi2.txt", "AAA middle BBB")

		_, err := tool.Execute(context.Background(), mustJSON(t, EditParams{
			Path: "multi2.txt",
			Edits: []EditEntry{
				{OldText: "AAA", NewText: "XXX"},
				{OldText: "BBB", NewText: "YYY"},
			},
		}))
		require.NoError(t, err)
		assert.Equal(t, "XXX middle YYY", readFile(t, "multi2.txt"))
	})

	t.Run("multi-edit rejects overlapping edits", func(t *testing.T) {
		setup(t, "overlap.txt", "abcdef")

		_, err := tool.Execute(context.Background(), mustJSON(t, EditParams{
			Path: "overlap.txt",
			Edits: []EditEntry{
				{OldText: "abcd", NewText: "XXXX"},
				{OldText: "cdef", NewText: "YYYY"},
			},
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "overlap")
	})

	t.Run("multi-edit rejects if any oldText not found", func(t *testing.T) {
		setup(t, "notfound.txt", "hello world")

		_, err := tool.Execute(context.Background(), mustJSON(t, EditParams{
			Path: "notfound.txt",
			Edits: []EditEntry{
				{OldText: "hello", NewText: "hi"},
				{OldText: "missing", NewText: "gone"},
			},
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
		// File should be unchanged (validation before apply)
		assert.Equal(t, "hello world", readFile(t, "notfound.txt"))
	})

	t.Run("multi-edit rejects empty oldText", func(t *testing.T) {
		setup(t, "empty.txt", "content")

		_, err := tool.Execute(context.Background(), mustJSON(t, EditParams{
			Path: "empty.txt",
			Edits: []EditEntry{
				{OldText: "", NewText: "replaced"},
			},
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be empty")
	})

	t.Run("single edits[] entry works", func(t *testing.T) {
		setup(t, "single_array.txt", "old text here")

		result, err := tool.Execute(context.Background(), mustJSON(t, EditParams{
			Path: "single_array.txt",
			Edits: []EditEntry{
				{OldText: "old text", NewText: "new text"},
			},
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "Replaced 1 occurrence")
		assert.Equal(t, "new text here", readFile(t, "single_array.txt"))
	})

	t.Run("errors when no edits provided", func(t *testing.T) {
		setup(t, "noedits.txt", "content")

		_, err := tool.Execute(context.Background(), mustJSON(t, EditParams{
			Path: "noedits.txt",
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "required")
	})

	t.Run("three disjoint edits", func(t *testing.T) {
		setup(t, "three.go", "func A() {}\nfunc B() {}\nfunc C() {}\n")

		result, err := tool.Execute(context.Background(), mustJSON(t, EditParams{
			Path: "three.go",
			Edits: []EditEntry{
				{OldText: "func A() {}", NewText: "func A() int { return 1 }"},
				{OldText: "func B() {}", NewText: "func B() int { return 2 }"},
				{OldText: "func C() {}", NewText: "func C() int { return 3 }"},
			},
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "Applied 3 edits")

		content := readFile(t, "three.go")
		assert.Contains(t, content, "return 1")
		assert.Contains(t, content, "return 2")
		assert.Contains(t, content, "return 3")
	})
}
