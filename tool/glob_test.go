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

func setupGlobFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	files := []string{
		"main.go",
		"main_test.go",
		"README.md",
		"cmd/server/main.go",
		"cmd/server/handler.go",
		"cmd/cli/main.go",
		"internal/util.go",
		"internal/util_test.go",
	}
	for _, f := range files {
		full := filepath.Join(dir, filepath.FromSlash(f))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte("// "+f), 0o644))
	}
	return dir
}

func TestGlobTool_Execute(t *testing.T) {
	dir := setupGlobFixture(t)
	g := &GlobTool{WorkDir: dir}

	t.Run("matches all go files recursively", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, GlobParams{Pattern: "**/*.go"}))
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.GreaterOrEqual(t, len(lines), 6)
		for _, l := range lines {
			assert.True(t, strings.HasSuffix(l, ".go"), "expected .go suffix: %s", l)
		}
	})

	t.Run("matches only test files", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, GlobParams{Pattern: "**/*_test.go"}))
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		for _, l := range lines {
			assert.True(t, strings.HasSuffix(l, "_test.go"), "expected _test.go: %s", l)
		}
		assert.Len(t, lines, 2)
	})

	t.Run("matches top-level files only", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, GlobParams{Pattern: "*.go"}))
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Len(t, lines, 2) // main.go and main_test.go
		for _, l := range lines {
			assert.False(t, strings.Contains(l, "/"), "expected top-level only: %s", l)
		}
	})

	t.Run("matches markdown files", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, GlobParams{Pattern: "**/*.md"}))
		require.NoError(t, err)
		assert.Contains(t, result, "README.md")
	})

	t.Run("returns no matches for nonexistent pattern", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, GlobParams{Pattern: "**/*.xyz"}))
		require.NoError(t, err)
		assert.Equal(t, "(no matches)", result)
	})

	t.Run("searches within subdirectory", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, GlobParams{
			Pattern: "*.go",
			Dir:     "cmd/server",
		}))
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Len(t, lines, 2)
	})

	t.Run("errors on invalid params", func(t *testing.T) {
		_, err := g.Execute(context.Background(), []byte(`{invalid`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid params")
	})

	t.Run("errors on empty pattern", func(t *testing.T) {
		_, err := g.Execute(context.Background(), mustJSON(t, GlobParams{Pattern: ""}))
		require.Error(t, err)
	})

	t.Run("output is sorted alphabetically", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, GlobParams{Pattern: "**/*.go"}))
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		sorted := make([]string, len(lines))
		copy(sorted, lines)
		assert.Equal(t, sorted, lines, "output should already be sorted")
	})
}

func TestMatchParts(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"**/*.go", "main.go", true},
		{"**/*.go", "cmd/server/main.go", true},
		{"**/*.go", "cmd/server/main.ts", false},
		{"*.go", "main.go", true},
		{"*.go", "cmd/main.go", false},
		{"cmd/**/*.go", "cmd/server/main.go", true},
		{"cmd/**/*.go", "cmd/server/sub/deep.go", true},
		{"cmd/**/*.go", "internal/util.go", false},
		{"**", "anything/at/all", true},
		{"a/b/c", "a/b/c", true},
		{"a/b/c", "a/b/d", false},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+"|"+tc.name, func(t *testing.T) {
			patParts := strings.Split(tc.pattern, "/")
			nameParts := strings.Split(tc.name, "/")
			got, err := matchParts(patParts, nameParts)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
