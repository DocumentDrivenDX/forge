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

func setupGrepFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	files := map[string]string{
		"main.go":         "package main\n\nfunc main() {\n\t// TODO: implement\n}\n",
		"util.go":         "package main\n\nfunc add(a, b int) int {\n\treturn a + b\n}\n",
		"util_test.go":    "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\t// TODO: test add\n}\n",
		"cmd/server.go":   "package cmd\n\n// ServerConfig holds configuration.\ntype ServerConfig struct{}\n",
		"data/binary.bin": string([]byte{0x00, 0xFF, 0xFE}),
	}
	for path, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return dir
}

func TestGrepTool_Execute(t *testing.T) {
	dir := setupGrepFixture(t)
	g := &GrepTool{WorkDir: dir}

	t.Run("finds pattern across files", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, GrepParams{Pattern: "TODO"}))
		require.NoError(t, err)
		assert.Contains(t, result, "main.go")
		assert.Contains(t, result, "util_test.go")
		assert.Contains(t, result, "TODO")
	})

	t.Run("includes line numbers", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, GrepParams{Pattern: "func main"}))
		require.NoError(t, err)
		// format: file:linenum:content
		assert.Contains(t, result, ":3:")
	})

	t.Run("restricts to glob filter", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, GrepParams{
			Pattern: "TODO",
			Glob:    "*_test.go",
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "util_test.go")
		assert.NotContains(t, result, "main.go")
	})

	t.Run("case insensitive search", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, GrepParams{
			Pattern:         "serverconfig",
			CaseInsensitive: true,
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "ServerConfig")
	})

	t.Run("no matches", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, GrepParams{Pattern: "xyzzy_nonexistent"}))
		require.NoError(t, err)
		assert.Equal(t, "(no matches)", result)
	})

	t.Run("skips binary files", func(t *testing.T) {
		// Binary file contains 0x00 which is not valid UTF-8 in context.
		result, err := g.Execute(context.Background(), mustJSON(t, GrepParams{Pattern: "."}))
		require.NoError(t, err)
		assert.NotContains(t, result, "binary.bin")
	})

	t.Run("errors on invalid regex", func(t *testing.T) {
		_, err := g.Execute(context.Background(), mustJSON(t, GrepParams{Pattern: "[invalid"}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid pattern")
	})

	t.Run("errors on invalid params", func(t *testing.T) {
		_, err := g.Execute(context.Background(), []byte(`{bad json`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid params")
	})

	t.Run("skips .git directory", func(t *testing.T) {
		// Create a .git directory with a file containing a matchable pattern.
		gitFile := filepath.Join(dir, ".git", "config")
		require.NoError(t, os.MkdirAll(filepath.Dir(gitFile), 0o755))
		require.NoError(t, os.WriteFile(gitFile, []byte("[core]\n\trepositoryformatversion = 0\n"), 0o644))

		result, err := g.Execute(context.Background(), mustJSON(t, GrepParams{Pattern: "repositoryformatversion"}))
		require.NoError(t, err)
		assert.Equal(t, "(no matches)", result)
	})

	t.Run("results sorted by file then line", func(t *testing.T) {
		result, err := g.Execute(context.Background(), mustJSON(t, GrepParams{Pattern: "package"}))
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Greater(t, len(lines), 1)
		// Verify file ordering.
		var files []string
		for _, l := range lines {
			parts := strings.SplitN(l, ":", 2)
			if len(parts) > 0 {
				files = append(files, parts[0])
			}
		}
		// Files should be in non-decreasing order.
		for i := 1; i < len(files); i++ {
			assert.LessOrEqual(t, files[i-1], files[i])
		}
	})

	t.Run("respects ExcludeDirs override", func(t *testing.T) {
		// Create a vendor directory with files containing the pattern
		vendorFile := filepath.Join(dir, "vendor", "example.com", "pkg", "file.go")
		require.NoError(t, os.MkdirAll(filepath.Dir(vendorFile), 0o755))
		require.NoError(t, os.WriteFile(vendorFile, []byte("// TODO: vendor code\n"), 0o644))

		// Default behavior: skip vendor/
		result, err := g.Execute(context.Background(), mustJSON(t, GrepParams{Pattern: "TODO"}))
		require.NoError(t, err)
		assert.NotContains(t, result, "vendor")

		// With ExcludeDirs set to empty array: search all directories including vendor/
		emptySlice := []string{}
		result, err = g.Execute(context.Background(), mustJSON(t, GrepParams{
			Pattern:     "TODO",
			ExcludeDirs: &emptySlice,
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "vendor")

		// With custom ExcludeDirs: only skip specified dirs
		customSlice := []string{".git"}
		result, err = g.Execute(context.Background(), mustJSON(t, GrepParams{
			Pattern:     "TODO",
			ExcludeDirs: &customSlice,
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "vendor")
	})

	t.Run("ExcludeDirs overrides all default skip dirs", func(t *testing.T) {
		// Create files in multiple skipped directories
		gitFile := filepath.Join(dir, ".git", "config")
		require.NoError(t, os.MkdirAll(filepath.Dir(gitFile), 0o755))
		require.NoError(t, os.WriteFile(gitFile, []byte("[core]\n\tTODO: git config\n"), 0o644))

		vendorFile := filepath.Join(dir, "vendor", "pkg.go")
		require.NoError(t, os.MkdirAll(filepath.Dir(vendorFile), 0o755))
		require.NoError(t, os.WriteFile(vendorFile, []byte("// vendor TODO\n"), 0o644))

		nodeModulesFile := filepath.Join(dir, "node_modules", "pkg.js")
		require.NoError(t, os.MkdirAll(filepath.Dir(nodeModulesFile), 0o755))
		require.NoError(t, os.WriteFile(nodeModulesFile, []byte("// node_modules TODO\n"), 0o644))

		// With empty ExcludeDirs, all should be found
		emptySlice := []string{}
		result, err := g.Execute(context.Background(), mustJSON(t, GrepParams{
			Pattern:     "TODO",
			ExcludeDirs: &emptySlice,
		}))
		require.NoError(t, err)
		assert.Contains(t, result, ".git")
		assert.Contains(t, result, "vendor")
		assert.Contains(t, result, "node_modules")
	})
}
