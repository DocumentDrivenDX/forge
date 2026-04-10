package tool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupPatchTool(t *testing.T) (tool *PatchTool, dir string) {
	t.Helper()
	dir = t.TempDir()
	return &PatchTool{WorkDir: dir}, dir
}

func setupFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err)
	return string(data)
}

// ======================== REPLACE ========================

func TestPatchTool_Replace_SingleOccurrence(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "a.go", "func hello() {}\n")

	result, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "a.go",
		Search:    "func hello() {}",
		Content:   "func hello() { return \"hi\" }",
		Operation: "replace",
	}))
	require.NoError(t, err)
	assert.Contains(t, result, "Replaced 1 occurrence")
	assert.Equal(t, "func hello() { return \"hi\" }\n", readFile(t, dir, "a.go"))
}

func TestPatchTool_Replace_MidFile(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "b.go", "package main\n\nfunc hello() {\n\t// TODO\n}\n")

	result, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:    "b.go",
		Search:  "\t// TODO\n",
		Content: "\tif name == \"\" {\n\t\treturn \"anonymous\"\n\t}\n",
	}))
	require.NoError(t, err)
	assert.Contains(t, result, "Replaced 1 occurrence")
	assert.Contains(t, readFile(t, dir, "b.go"), `if name == ""`)
}

func TestPatchTool_Replace_NotFound(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "c.go", "hello world\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:   "c.go",
		Search: "missing_text",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	// File unchanged
	assert.Equal(t, "hello world\n", readFile(t, dir, "c.go"))
}

func TestPatchTool_Replace_MultipleOccurrences(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "d.go", "foo bar foo baz foo\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:    "d.go",
		Search:  "foo",
		Content: "qux",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "appears 3 times")
	// File unchanged
	assert.Equal(t, "foo bar foo baz foo\n", readFile(t, dir, "d.go"))
}

func TestPatchTool_Replace_NoSearchText(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "e.go", "content\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:    "e.go",
		Content: "replacement",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "search text is required")
	// File unchanged
	assert.Equal(t, "content\n", readFile(t, dir, "e.go"))
}

// ======================== REPLACE_ALL ========================

func TestPatchTool_ReplaceAll_MultipleOccurrences(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "f.go", "old old old\n")

	result, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "f.go",
		Search:    "old",
		Content:   "new",
		Operation: "replace_all",
	}))
	require.NoError(t, err)
	assert.Contains(t, result, "Replaced 3 occurrences")
	assert.Equal(t, "new new new\n", readFile(t, dir, "f.go"))
}

func TestPatchTool_ReplaceAll_NotFound(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "g.go", "content\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "g.go",
		Search:    "missing",
		Content:   "replacement",
		Operation: "replace_all",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	// File unchanged
	assert.Equal(t, "content\n", readFile(t, dir, "g.go"))
}

func TestPatchTool_ReplaceAll_NoSearchText(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "h.go", "content\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "h.go",
		Content:   "replacement",
		Operation: "replace_all",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "search text is required")
}

// ======================== PREPEND ========================

func TestPatchTool_Prepend_NoSearch(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "i.go", "existing\n")

	result, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "i.go",
		Content:   "package main\n",
		Operation: "prepend",
	}))
	require.NoError(t, err)
	assert.Contains(t, result, "Prepended")
	assert.Equal(t, "package main\n\nexisting\n", readFile(t, dir, "i.go"))
}

func TestPatchTool_Prepend_WithSearch(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "j.go", "package main\n\nfunc hello() {\n}\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "j.go",
		Search:    "func hello()",
		Content:   "// Greet prints a greeting.",
		Operation: "prepend",
	}))
	require.NoError(t, err)
	content := readFile(t, dir, "j.go")
	// Content is inserted immediately before "func hello()"
	assert.Contains(t, content, "// Greet prints a greeting.\nfunc hello()")
}

func TestPatchTool_Prepend_SearchNotFound(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "k.go", "hello world\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "k.go",
		Search:    "nonexistent",
		Content:   "prefix",
		Operation: "prepend",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	// File unchanged
	assert.Equal(t, "hello world\n", readFile(t, dir, "k.go"))
}

func TestPatchTool_Prepend_EmptyFile(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "l.go", "")

	result, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "l.go",
		Content:   "new content",
		Operation: "prepend",
	}))
	require.NoError(t, err)
	assert.Contains(t, result, "Prepended")
	// With empty file, prepend adds the content with a leading newline
	assert.Equal(t, "new content\n", readFile(t, dir, "l.go"))
}

// ======================== APPEND ========================

func TestPatchTool_Append_NoSearch(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "m.go", "existing\n")

	result, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "m.go",
		Content:   "trailing\n",
		Operation: "append",
	}))
	require.NoError(t, err)
	assert.Contains(t, result, "Appended")
	assert.Equal(t, "existing\n\ntrailing\n", readFile(t, dir, "m.go"))
}

func TestPatchTool_Append_WithSearch(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "n.go", "func hello() {\n\treturn\n}\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "n.go",
		Search:    "return",
		Content:   "// TODO: add logging",
		Operation: "append",
	}))
	require.NoError(t, err)
	content := readFile(t, dir, "n.go")
	assert.Contains(t, content, "return\n// TODO: add logging")
}

func TestPatchTool_Append_SearchNotFound(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "o.go", "hello world\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "o.go",
		Search:    "nonexistent",
		Content:   "suffix",
		Operation: "append",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	// File unchanged
	assert.Equal(t, "hello world\n", readFile(t, dir, "o.go"))
}

// ======================== LINE ENDING NORMALIZATION ========================

func TestPatchTool_CRLF_Replace(t *testing.T) {
	tool, dir := setupPatchTool(t)
	// CRLF file
	setupFile(t, dir, "p.go", "line1\r\nline2\r\nline3\r\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "p.go",
		Search:    "line2",
		Content:   "replaced",
		Operation: "replace",
	}))
	require.NoError(t, err)
	// Source uses CRLF but search is LF-normalized — should still match
	content := readFile(t, dir, "p.go")
	assert.Equal(t, "line1\r\nreplaced\r\nline3\r\n", content)
}

func TestPatchTool_CRLF_SearchWithLF(t *testing.T) {
	tool, dir := setupPatchTool(t)
	// CRLF file, search provided with LF
	setupFile(t, dir, "q.go", "hello\r\nworld\r\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "q.go",
		Search:    "hello\n", // LF in search
		Content:   "hi\n",
		Operation: "replace",
	}))
	require.NoError(t, err)
	// Should match because search is normalized to CRLF
	assert.Equal(t, "hi\r\nworld\r\n", readFile(t, dir, "q.go"))
}

func TestPatchTool_CRLF_Prepend(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "r.go", "existing\r\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "r.go",
		Content:   "// header",
		Operation: "prepend",
	}))
	require.NoError(t, err)
	// Prepend uses CRLF separator
	assert.Equal(t, "// header\r\nexisting\r\n", readFile(t, dir, "r.go"))
}

func TestPatchTool_CRLF_Append(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "s.go", "existing\r\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "s.go",
		Content:   "// footer",
		Operation: "append",
	}))
	require.NoError(t, err)
	// Content after existing CRLF-terminated file
	assert.Equal(t, "existing\r\n\r\n// footer", readFile(t, dir, "s.go"))
}

// ======================== UNICODE ========================

func TestPatchTool_Unicode_Replace(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "t.go", "// greet returns a greeting 🌍\nfunc greet() string {\n\treturn \"héllo\"\n}\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "t.go",
		Search:    "return \"héllo\"",
		Content:   "return \"wørld 🌍\"",
		Operation: "replace",
	}))
	require.NoError(t, err)
	content := readFile(t, dir, "t.go")
	assert.Contains(t, content, "wørld 🌍")
}

func TestPatchTool_Unicode_Prepend(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "u.go", "func greet() {}\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "u.go",
		Content:   "// 🚀 autogenerated\n",
		Operation: "prepend",
	}))
	require.NoError(t, err)
	content := readFile(t, dir, "u.go")
	assert.Equal(t, "// 🚀 autogenerated\n\nfunc greet() {}\n", content)
}

// ======================== SPECIAL CHARACTERS ========================

func TestPatchTool_GoSyntaxChars(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "v.go", "msg := fmt.Sprintf(\"hello %s\", name)\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "v.go",
		Search:    "fmt.Sprintf(\"hello %s\", name)",
		Content:   "fmt.Sprintf(\"hi %s (%d)\", name, len(name))",
		Operation: "replace",
	}))
	require.NoError(t, err)
	assert.Contains(t, readFile(t, dir, "v.go"), "hi %s")
}

func TestPatchTool_TabAndWhitespace(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "w.go", "\tindent1\n\t\tindent2\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "w.go",
		Search:    "\t\tindent2",
		Content:   "\t\tindent2_fixed",
		Operation: "replace",
	}))
	require.NoError(t, err)
	assert.Equal(t, "\tindent1\n\t\tindent2_fixed\n", readFile(t, dir, "w.go"))
}

func TestPatchTool_MultiLineReplacement(t *testing.T) {
	tool, dir := setupPatchTool(t)
	original := `package main

func greet() {
	fmt.Println("old")
	fmt.Println("also old")
}
`
	setupFile(t, dir, "x.go", original)

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path: "x.go",
		Search: `fmt.Println("old")
	fmt.Println("also old")`,
		Content: `fmt.Println("new")
	fmt.Println("also new")`,
		Operation: "replace",
	}))
	require.NoError(t, err)
	content := readFile(t, dir, "x.go")
	assert.Contains(t, content, `"new"`)
	assert.Contains(t, content, `"also new"`)
	assert.NotContains(t, content, `"old"`)
}

// ======================== ERROR CASES ========================

func TestPatchTool_InvalidParams(t *testing.T) {
	tool, _ := setupPatchTool(t)

	_, err := tool.Execute(context.Background(), []byte(`{bad json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid params")
}

func TestPatchTool_MissingPath(t *testing.T) {
	tool, _ := setupPatchTool(t)

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Content: "content",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path is required")
}

func TestPatchTool_FileNotFound(t *testing.T) {
	tool, _ := setupPatchTool(t)

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:    "nonexistent.txt",
		Search:  "foo",
		Content: "bar",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such file")
}

func TestPatchTool_UnknownOperation(t *testing.T) {
	tool, dir := setupPatchTool(t)
	setupFile(t, dir, "y.go", "hello\n")

	_, err := tool.Execute(context.Background(), mustJSON(t, PatchParams{
		Path:      "y.go",
		Search:    "hello",
		Content:   "world",
		Operation: "foobar",
	}))
	require.NoError(t, err) // defaults to replace
	content := readFile(t, dir, "y.go")
	assert.Equal(t, "world\n", content)
}

// ======================== DETECT / NORMALIZE HELPERS ========================

func TestDetectLineEnding(t *testing.T) {
	assert.Equal(t, "\n", detectLineEnding("a\nb\nc"))
	assert.Equal(t, "\r\n", detectLineEnding("a\r\nb\r\nc"))
	assert.Equal(t, "\r\n", detectLineEnding("a\r\nb\r\nc\n")) // more CRLF
	assert.Equal(t, "\n", detectLineEnding("a\nb\r\nc"))       // more LF
}

func TestNormalizeLineEndings(t *testing.T) {
	// LF target: all become LF
	assert.Equal(t, "a\nb\nc", normalizeLineEndings("a\r\nb\r\nc", "\n"))

	// CRLF target: all become CRLF
	assert.Equal(t, "a\r\nb\r\nc", normalizeLineEndings("a\nb\nc", "\r\n"))
	assert.Equal(t, "a\r\nb\r\nc", normalizeLineEndings("a\r\nb\r\nc", "\r\n"))
	// Already mixed → all CRLF
	assert.Equal(t, "a\r\nb\r\nc", normalizeLineEndings("a\r\nb\nc", "\r\n"))
}

func TestSearchReplace_NoMatch(t *testing.T) {
	assert.Equal(t, "hello world", searchReplace("hello world", "missing", "x"))
}

func TestSearchReplace_FirstOccurrenceOnly(t *testing.T) {
	// searchReplace only replaces the first occurrence
	assert.Equal(t, "X b a b", searchReplace("a b a b", "a", "X"))
}
