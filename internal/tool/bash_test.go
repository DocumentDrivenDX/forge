package tool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBashTool_Execute(t *testing.T) {
	dir := t.TempDir()
	tool := &BashTool{WorkDir: dir}

	t.Run("captures stdout and exit code", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
			Command: "echo hello",
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "Exit code: 0")
		assert.Contains(t, result, "hello")
	})

	t.Run("captures stderr", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
			Command: "echo error >&2",
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "Stderr:")
		assert.Contains(t, result, "error")
	})

	t.Run("captures non-zero exit code", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
			Command: "exit 42",
		}))
		require.NoError(t, err) // non-zero exit is not a Go error
		assert.Contains(t, result, "Exit code: 42")
	})

	t.Run("runs in working directory", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
			Command: "pwd",
		}))
		require.NoError(t, err)
		assert.Contains(t, result, dir)
	})

	t.Run("kills on timeout", func(t *testing.T) {
		_, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
			Command:   "sleep 60",
			TimeoutMs: 50,
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timed out")
	})

	t.Run("kills on context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		_, err := tool.Execute(ctx, mustJSON(t, BashParams{
			Command: "sleep 10",
		}))
		// Should fail due to cancelled context
		require.Error(t, err)
	})

	t.Run("stdin behaves as immediate EOF and does not hang", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
			Command:   `if IFS= read -r line; then echo "stdin_line:$line"; else echo "stdin_eof"; fi`,
			TimeoutMs: 200,
		}))
		require.NoError(t, err)
		assert.Contains(t, result, "Exit code: 0")
		assert.Contains(t, result, "stdin_eof")
	})

	t.Run("truncates oversized output and preserves non-zero exit code", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
			Command: "yes a | head -c 1100000; exit 7",
		}))
		require.NoError(t, err) // non-zero exit should be captured, not returned as Go error
		assert.Contains(t, result, "Exit code: 7")
		assert.Contains(t, result, "lines omitted]")
	})
}

func TestBashTool_RTKOutputFilter(t *testing.T) {
	dir := t.TempDir()
	rtk := filepath.Join(dir, "rtk")
	require.NoError(t, os.WriteFile(rtk, []byte("#!/bin/sh\necho rtk:$*\n"), 0o755))

	tool := &BashTool{
		WorkDir: dir,
		OutputFilter: BashOutputFilterConfig{
			Mode:      "rtk",
			RTKBinary: rtk,
		},
	}

	result, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
		Command: "git status --short",
	}))
	require.NoError(t, err)
	assert.Contains(t, result, "[output filter: rtk git status]")
	assert.Contains(t, result, "rtk:git status --short")
}

func TestBashTool_RTKOutputFilter_GoTest(t *testing.T) {
	dir := t.TempDir()
	rtk := filepath.Join(dir, "rtk")
	require.NoError(t, os.WriteFile(rtk, []byte("#!/bin/sh\necho rtk:$*\n"), 0o755))

	tool := &BashTool{
		WorkDir: dir,
		OutputFilter: BashOutputFilterConfig{
			Mode:      "rtk",
			RTKBinary: rtk,
		},
	}

	result, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
		Command: "go test ./...",
	}))
	require.NoError(t, err)
	assert.Contains(t, result, "[output filter: rtk go test]")
	assert.Contains(t, result, "rtk:go test ./...")
}

func TestBashTool_RTKOutputFilter_MissingFallsBack(t *testing.T) {
	dir := t.TempDir()
	tool := &BashTool{
		WorkDir: dir,
		OutputFilter: BashOutputFilterConfig{
			Mode:      "rtk",
			RTKBinary: filepath.Join(dir, "missing-rtk"),
		},
	}

	result, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
		Command: "git status --short",
	}))
	require.NoError(t, err)
	assert.Contains(t, result, "output filter unavailable")
	assert.Contains(t, result, "used raw output")
}

func TestBashTool_RTKOutputFilter_NonzeroPreserved(t *testing.T) {
	dir := t.TempDir()
	rtk := filepath.Join(dir, "rtk")
	require.NoError(t, os.WriteFile(rtk, []byte("#!/bin/sh\necho rtk failed >&2\nexit 13\n"), 0o755))

	tool := &BashTool{
		WorkDir: dir,
		OutputFilter: BashOutputFilterConfig{
			Mode:      "rtk",
			RTKBinary: rtk,
		},
	}

	result, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
		Command: "git status --short",
	}))
	require.NoError(t, err)
	assert.Contains(t, result, "Exit code: 13")
	assert.Contains(t, result, "Stderr:")
	assert.Contains(t, result, "rtk failed")
}

func TestBashTool_OutputFilterMaxBytes(t *testing.T) {
	dir := t.TempDir()
	tool := &BashTool{
		WorkDir: dir,
		OutputFilter: BashOutputFilterConfig{
			MaxBytes: 5,
		},
	}

	result, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
		Command: "printf 123456789",
	}))
	require.NoError(t, err)
	assert.Contains(t, result, "12345")
	assert.Contains(t, result, "output filter truncated")
	assert.NotContains(t, result, "6789")
}

func TestBashTool_BenchmarkPolicyRejectsShellFind(t *testing.T) {
	dir := t.TempDir()
	tool := &BashTool{WorkDir: dir, Mode: "benchmark"}

	result, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
		Command: `find / -name "*.go" -type f 2>/dev/null | head -20`,
	}))
	require.NoError(t, err)
	assert.Contains(t, result, "Exit code: 2")
	assert.Contains(t, result, "policy blocked")
	assert.Contains(t, result, "use the find tool instead")
}

func TestBashTool_BenchmarkPolicyRejectsRecursiveLs(t *testing.T) {
	dir := t.TempDir()
	tool := &BashTool{WorkDir: dir, Mode: "benchmark"}

	result, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
		Command: "ls -R",
	}))
	require.NoError(t, err)
	assert.Contains(t, result, "Exit code: 2")
	assert.Contains(t, result, "use the ls or find tool instead")
}

func TestBashTool_BenchmarkPolicyAllowsRepoVerification(t *testing.T) {
	dir := t.TempDir()
	tool := &BashTool{WorkDir: dir, Mode: "benchmark"}

	result, err := tool.Execute(context.Background(), mustJSON(t, BashParams{
		Command: "printf ok",
	}))
	require.NoError(t, err)
	assert.Contains(t, result, "Exit code: 0")
	assert.Contains(t, result, "ok")
	assert.NotContains(t, result, "policy blocked")
}
