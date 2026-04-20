package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeRunSubcommand_NoArgs(t *testing.T) {
	isRun, remaining := normalizeRunSubcommand([]string{})
	assert.False(t, isRun)
	assert.Equal(t, []string{}, remaining)
}

func TestNormalizeRunSubcommand_EmptyRun(t *testing.T) {
	isRun, remaining := normalizeRunSubcommand([]string{"run"})
	assert.True(t, isRun)
	assert.Equal(t, []string{}, remaining)
}

func TestNormalizeRunSubcommand_RunWithPrompt(t *testing.T) {
	isRun, remaining := normalizeRunSubcommand([]string{"run", "Read the file"})
	assert.True(t, isRun)
	assert.Equal(t, []string{"Read the file"}, remaining)
}

func TestNormalizeRunSubcommand_RunWithFlags(t *testing.T) {
	isRun, remaining := normalizeRunSubcommand([]string{"run", "--model", "qwen3.5-27b", "Read the file"})
	assert.True(t, isRun)
	assert.Equal(t, []string{"--model", "qwen3.5-27b", "Read the file"}, remaining)
}

func TestNormalizeRunSubcommand_RunWithReasoningFlag(t *testing.T) {
	isRun, remaining := normalizeRunSubcommand([]string{"run", "--reasoning", "high", "Read the file"})
	assert.True(t, isRun)
	assert.Equal(t, []string{"--reasoning", "high", "Read the file"}, remaining)
}

func TestNormalizeRunSubcommand_RunWithBoolFlag(t *testing.T) {
	isRun, remaining := normalizeRunSubcommand([]string{"run", "--json", "Read the file"})
	assert.True(t, isRun)
	assert.Equal(t, []string{"--json", "Read the file"}, remaining)
}

func TestNormalizeRunSubcommand_RunAfterFlags(t *testing.T) {
	isRun, remaining := normalizeRunSubcommand([]string{"--work-dir", "/tmp", "run", "Read the file"})
	assert.True(t, isRun)
	assert.Equal(t, []string{"--work-dir", "/tmp", "Read the file"}, remaining)
}

func TestNormalizeRunSubcommand_RunBetweenFlags(t *testing.T) {
	isRun, remaining := normalizeRunSubcommand([]string{"--work-dir", "/tmp", "run", "--model", "qwen3.5-27b"})
	assert.True(t, isRun)
	assert.Equal(t, []string{"--work-dir", "/tmp", "--model", "qwen3.5-27b"}, remaining)
}

func TestNormalizeRunSubcommand_SubcommandNotRun(t *testing.T) {
	isRun, remaining := normalizeRunSubcommand([]string{"log"})
	assert.False(t, isRun)
	assert.Equal(t, []string{"log"}, remaining)
}

func TestNormalizeRunSubcommand_SubcommandWithArgs(t *testing.T) {
	isRun, remaining := normalizeRunSubcommand([]string{"replay", "s-12345"})
	assert.False(t, isRun)
	assert.Equal(t, []string{"replay", "s-12345"}, remaining)
}

func TestNormalizeRunSubcommand_ModelFlag(t *testing.T) {
	isRun, remaining := normalizeRunSubcommand([]string{"--model", "qwen3.5-27b", "Read the file"})
	assert.False(t, isRun)
	assert.Equal(t, []string{"--model", "qwen3.5-27b", "Read the file"}, remaining)
}

func TestNormalizeRunSubcommand_MultipleFlags(t *testing.T) {
	isRun, remaining := normalizeRunSubcommand([]string{"run", "--model", "qwen3.5-27b", "--provider", "local", "--max-iter", "10", "Some prompt"})
	assert.True(t, isRun)
	assert.Equal(t, []string{"--model", "qwen3.5-27b", "--provider", "local", "--max-iter", "10", "Some prompt"}, remaining)
}

func TestNormalizeRunSubcommand_MixedArgsAndFlags(t *testing.T) {
	isRun, remaining := normalizeRunSubcommand([]string{"-p", "Read this", "run", "--model", "test"})
	assert.True(t, isRun)
	assert.Equal(t, []string{"-p", "Read this", "--model", "test"}, remaining)
}
