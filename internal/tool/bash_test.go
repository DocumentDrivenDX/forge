package tool

import (
	"context"
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
