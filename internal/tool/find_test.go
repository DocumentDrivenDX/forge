package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupFindFixture(t *testing.T) string {
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

func TestFindTool_Execute(t *testing.T) {
	dir := setupFindFixture(t)
	g := &FindTool{WorkDir: dir}

	t.Run("matches all go files recursively", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, FindParams{Pattern: "**/*.go"}))
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.GreaterOrEqual(t, len(lines), 6)
		for _, l := range lines {
			assert.True(t, strings.HasSuffix(l, ".go"), "expected .go suffix: %s", l)
		}
	})

	t.Run("matches only test files", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, FindParams{Pattern: "**/*_test.go"}))
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		for _, l := range lines {
			assert.True(t, strings.HasSuffix(l, "_test.go"), "expected _test.go: %s", l)
		}
		assert.Len(t, lines, 2)
	})

	t.Run("matches top-level files only", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, FindParams{Pattern: "*.go"}))
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Len(t, lines, 2) // main.go and main_test.go
		for _, l := range lines {
			assert.False(t, strings.Contains(l, "/"), "expected top-level only: %s", l)
		}
	})

	t.Run("matches markdown files", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, FindParams{Pattern: "**/*.md"}))
		require.NoError(t, err)
		assert.Contains(t, result, "README.md")
	})

	t.Run("returns no matches for nonexistent pattern", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, FindParams{Pattern: "**/*.xyz"}))
		require.NoError(t, err)
		assert.Equal(t, "(no matches)", result)
	})

	t.Run("searches within subdirectory", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, FindParams{
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
		_, err := g.Execute(context.Background(), mustJSON(t, FindParams{Pattern: ""}))
		require.Error(t, err)
	})

	t.Run("skips .git directory", func(t *testing.T) {
		// Create a .git directory with a file inside.
		gitFile := filepath.Join(dir, ".git", "HEAD")
		require.NoError(t, os.MkdirAll(filepath.Dir(gitFile), 0o755))
		require.NoError(t, os.WriteFile(gitFile, []byte("ref: refs/heads/main\n"), 0o644))

		result, err := g.Execute(context.Background(), mustJSON(t, FindParams{Pattern: "**/*"}))
		require.NoError(t, err)
		assert.NotContains(t, result, ".git")
	})

	t.Run("output is sorted alphabetically", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, FindParams{Pattern: "**/*.go"}))
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		sorted := make([]string, len(lines))
		copy(sorted, lines)
		assert.Equal(t, sorted, lines, "output should already be sorted")
	})

	t.Run("respects ExcludeDirs override", func(t *testing.T) {
		// Create a vendor directory with Go files (simulating go mod vendor)
		vendorFile := filepath.Join(dir, "vendor", "example.com", "pkg", "file.go")
		require.NoError(t, os.MkdirAll(filepath.Dir(vendorFile), 0o755))
		require.NoError(t, os.WriteFile(vendorFile, []byte("// vendor file"), 0o644))

		// Default behavior: skip vendor/
		result, err := g.Execute(context.Background(), mustJSON(t, FindParams{Pattern: "**/*.go"}))
		require.NoError(t, err)
		assert.NotContains(t, result, "vendor")

		// With ExcludeDirs set to empty array: search all directories including vendor/
		emptySlice := []string{}
		result, err = g.Execute(context.Background(), mustJSON(t, FindParams{
			Pattern:     "**/*.go",
			ExcludeDirs: &emptySlice,
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "vendor")

		// With custom ExcludeDirs: only skip specified dirs
		customSlice := []string{".git"}
		result, err = g.Execute(context.Background(), mustJSON(t, FindParams{
			Pattern:     "**/*.go",
			ExcludeDirs: &customSlice,
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "vendor")
	})

	t.Run("ExcludeDirs overrides all default skip dirs", func(t *testing.T) {
		// Create files in multiple skipped directories
		gitFile := filepath.Join(dir, ".git", "HEAD")
		require.NoError(t, os.MkdirAll(filepath.Dir(gitFile), 0o755))
		require.NoError(t, os.WriteFile(gitFile, []byte("ref: refs/heads/main\n"), 0o644))

		vendorFile := filepath.Join(dir, "vendor", "pkg.go")
		require.NoError(t, os.MkdirAll(filepath.Dir(vendorFile), 0o755))
		require.NoError(t, os.WriteFile(vendorFile, []byte("// vendor"), 0o644))

		nodeModulesFile := filepath.Join(dir, "node_modules", "pkg.js")
		require.NoError(t, os.MkdirAll(filepath.Dir(nodeModulesFile), 0o755))
		require.NoError(t, os.WriteFile(nodeModulesFile, []byte("// node_modules"), 0o644))

		// With empty ExcludeDirs, all should be found
		emptySlice := []string{}
		result, err := g.Execute(context.Background(), mustJSON(t, FindParams{
			Pattern:     "**/*",
			ExcludeDirs: &emptySlice,
		}))
		require.NoError(t, err)
		assert.Contains(t, result, ".git")
		assert.Contains(t, result, "vendor")
		assert.Contains(t, result, "node_modules")
	})
}

func TestFindTool_Truncation(t *testing.T) {
	dir := t.TempDir()
	// Create more files than maxFindResults.
	for i := 0; i < maxFindResults+10; i++ {
		name := filepath.Join(dir, fmt.Sprintf("file%04d.txt", i))
		require.NoError(t, os.WriteFile(name, []byte("x"), 0o644))
	}
	g := &FindTool{WorkDir: dir}
	result, err := g.Execute(context.Background(), mustJSON(t, FindParams{Pattern: "*.txt"}))
	require.NoError(t, err)
	assert.Contains(t, result, fmt.Sprintf("(results truncated at %d matches)", maxFindResults))
	lines := strings.Split(strings.TrimSpace(result), "\n")
	// maxFindResults file lines + 1 truncation line
	assert.Equal(t, maxFindResults+1, len(lines))
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
