package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolvePromptPlainString(t *testing.T) {
	promptText, metadata, err := resolvePrompt("Read main.go and tell me the package name")
	require.NoError(t, err)
	assert.Equal(t, "Read main.go and tell me the package name", promptText)
	assert.Nil(t, metadata)
}

func TestResolvePromptPlainJSONIsNotEnvelope(t *testing.T) {
	raw := `{"prompt":"not an envelope"}`

	promptText, metadata, err := resolvePrompt(raw)
	require.NoError(t, err)
	assert.Equal(t, raw, promptText)
	assert.Nil(t, metadata)
}

func TestResolvePromptPlainJSONWithKindIsNotEnvelope(t *testing.T) {
	raw := `{"kind":"note","text":"hello"}`

	promptText, metadata, err := resolvePrompt(raw)
	require.NoError(t, err)
	assert.Equal(t, raw, promptText)
	assert.Nil(t, metadata)
}

func TestResolvePromptPlainJSONWithKindAndTitleIsNotEnvelope(t *testing.T) {
	raw := `{"kind":"note","title":"hello"}`

	promptText, metadata, err := resolvePrompt(raw)
	require.NoError(t, err)
	assert.Equal(t, raw, promptText)
	assert.Nil(t, metadata)
}

func TestResolvePromptMalformedEnvelopeWithNonStringKindInline(t *testing.T) {
	raw := `{"kind":1,"title":"Inspect main"}`

	promptText, metadata, err := resolvePrompt(raw)
	require.Error(t, err)
	assert.Empty(t, promptText)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "invalid prompt envelope")
}

func TestResolvePromptPlainJSONWithKindAndTitleIsInvalidEnvelope(t *testing.T) {
	raw := `{"kind":"prompt","title":"Inspect main"}`

	promptText, metadata, err := resolvePrompt(raw)
	require.Error(t, err)
	assert.Empty(t, promptText)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "invalid prompt envelope")
}

func TestResolvePromptEnvelopeInline(t *testing.T) {
	raw := `{
		"kind": "prompt",
		"id": "task-42",
		"title": "Inspect main",
		"prompt": "Read main.go and tell me the package name",
		"inputs": {"paths": ["main.go"]},
		"response_schema": {"type": "object"},
		"callback": {"url": "https://example.com/callback"}
	}`

	promptText, metadata, err := resolvePrompt(raw)
	require.NoError(t, err)
	assert.Equal(t, "Read main.go and tell me the package name", promptText)
	require.NotNil(t, metadata)
	assert.Equal(t, "prompt", metadata["prompt.kind"])
	assert.Equal(t, "task-42", metadata["prompt.id"])
	assert.Equal(t, "Inspect main", metadata["prompt.title"])
	assert.Equal(t, `{"paths":["main.go"]}`, metadata["prompt.inputs"])
	assert.Equal(t, `{"type":"object"}`, metadata["prompt.response_schema"])
	assert.Equal(t, `{"url":"https://example.com/callback"}`, metadata["prompt.callback"])
}

func TestResolvePromptEnvelopeFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.json")
	raw := `{"kind":"prompt","id":"file-1","prompt":"Read main.go"}`
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))

	promptText, metadata, err := resolvePrompt("@" + path)
	require.NoError(t, err)
	assert.Equal(t, "Read main.go", promptText)
	require.NotNil(t, metadata)
	assert.Equal(t, "prompt", metadata["prompt.kind"])
	assert.Equal(t, "file-1", metadata["prompt.id"])
}

func TestResolvePromptEnvelopeInvalid(t *testing.T) {
	raw := `{"kind":"prompt","id":"task-42","inputs":{"paths":["main.go"]}}`

	promptText, metadata, err := resolvePrompt(raw)
	require.Error(t, err)
	assert.Empty(t, promptText)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "prompt envelope")
}

func TestResolvePromptEnvelopeMissingIDInline(t *testing.T) {
	raw := `{"kind":"prompt","prompt":"Read main.go"}`

	promptText, metadata, err := resolvePrompt(raw)
	require.Error(t, err)
	assert.Empty(t, promptText)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "invalid prompt envelope")
}

func TestResolvePromptEnvelopeMissingIDWithTitleInline(t *testing.T) {
	raw := `{"kind":"prompt","title":"Inspect main"}`

	promptText, metadata, err := resolvePrompt(raw)
	require.Error(t, err)
	assert.Empty(t, promptText)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "invalid prompt envelope")
}

func TestResolvePromptEnvelopeInvalidFromStdin(t *testing.T) {
	raw := `{"kind":"prompt","id":"task-42","inputs":{"paths":["main.go"]}}`
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	_, err = w.WriteString(raw)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	os.Stdin = r

	promptText, metadata, err := resolvePrompt("")
	require.Error(t, err)
	assert.Empty(t, promptText)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "prompt envelope")
}

func TestResolvePromptMalformedEnvelopeWithNullKindFromStdin(t *testing.T) {
	raw := `{"kind":null,"id":"task-42"}`
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	_, err = w.WriteString(raw)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	os.Stdin = r

	promptText, metadata, err := resolvePrompt("")
	require.Error(t, err)
	assert.Empty(t, promptText)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "invalid prompt envelope")
}

func TestResolvePromptEnvelopeMissingIDFromStdin(t *testing.T) {
	raw := `{"kind":"prompt","prompt":"Read main.go"}`
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	_, err = w.WriteString(raw)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	os.Stdin = r

	promptText, metadata, err := resolvePrompt("")
	require.Error(t, err)
	assert.Empty(t, promptText)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "invalid prompt envelope")
}

func TestResolvePromptEnvelopeMissingIDWithTitleFromStdin(t *testing.T) {
	raw := `{"kind":"prompt","title":"Inspect main"}`
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	_, err = w.WriteString(raw)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	os.Stdin = r

	promptText, metadata, err := resolvePrompt("")
	require.Error(t, err)
	assert.Empty(t, promptText)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "invalid prompt envelope")
}

func TestResolvePromptEnvelopeInvalidFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.json")
	raw := `{"kind":"prompt","id":"task-42","inputs":{"paths":["main.go"]}}`
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))

	promptText, metadata, err := resolvePrompt("@" + path)
	require.Error(t, err)
	assert.Empty(t, promptText)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "prompt envelope")
}

func TestResolvePromptMalformedEnvelopeWithNonStringKindFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.json")
	raw := `{"kind":1,"inputs":{"paths":["main.go"]}}`
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))

	promptText, metadata, err := resolvePrompt("@" + path)
	require.Error(t, err)
	assert.Empty(t, promptText)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "invalid prompt envelope")
}

func TestResolvePromptEnvelopeMissingIDFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.json")
	raw := `{"kind":"prompt","prompt":"Read main.go"}`
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))

	promptText, metadata, err := resolvePrompt("@" + path)
	require.Error(t, err)
	assert.Empty(t, promptText)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "invalid prompt envelope")
}

func TestResolvePromptEnvelopeMissingIDWithTitleFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.json")
	raw := `{"kind":"prompt","title":"Inspect main"}`
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))

	promptText, metadata, err := resolvePrompt("@" + path)
	require.Error(t, err)
	assert.Empty(t, promptText)
	assert.Nil(t, metadata)
	assert.Contains(t, err.Error(), "invalid prompt envelope")
}

func TestResolvePromptPlainJSONWithKindAndTitleFromStdinIsNotEnvelope(t *testing.T) {
	raw := `{"kind":"note","title":"hello"}`
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})
	_, err = w.WriteString(raw)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	os.Stdin = r

	promptText, metadata, err := resolvePrompt("")
	require.NoError(t, err)
	assert.Equal(t, raw, promptText)
	assert.Nil(t, metadata)
}

func TestResolvePromptPlainJSONWithKindAndTitleFromFileIsNotEnvelope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.json")
	raw := `{"kind":"note","title":"hello"}`
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))

	promptText, metadata, err := resolvePrompt("@" + path)
	require.NoError(t, err)
	assert.Equal(t, raw, promptText)
	assert.Nil(t, metadata)
}
