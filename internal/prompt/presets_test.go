package prompt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPresetNames(t *testing.T) {
	names := PresetNames()
	require.Len(t, names, 5)
	assert.Equal(t, "default", names[0]) // default first
	assert.Contains(t, names, "smart")
	assert.Contains(t, names, "cheap")
	assert.Contains(t, names, "benchmark")
}

func TestGetPreset(t *testing.T) {
	t.Run("known preset", func(t *testing.T) {
		p := GetPreset("cheap")
		assert.Equal(t, "cheap", p.Name)
		assert.Contains(t, p.Base, "pragmatic")
		assert.NotEmpty(t, p.Guidelines)
	})

	t.Run("unknown falls back to default", func(t *testing.T) {
		p := GetPreset("nonexistent")
		assert.Equal(t, "default", p.Name)
	})
}

func TestResolvePresetName(t *testing.T) {
	t.Run("empty uses default", func(t *testing.T) {
		got, err := ResolvePresetName("")
		require.NoError(t, err)
		assert.Equal(t, "default", got)
	})

	t.Run("canonical preset", func(t *testing.T) {
		got, err := ResolvePresetName("smart")
		require.NoError(t, err)
		assert.Equal(t, "smart", got)
	})

	t.Run("removed preset errors", func(t *testing.T) {
		_, err := ResolvePresetName("codex")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `preset "codex" was removed`)
		assert.Contains(t, err.Error(), `"cheap"`)
	})

	t.Run("unknown preset errors", func(t *testing.T) {
		_, err := ResolvePresetName("nope")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown preset "nope"`)
	})
}

func TestNewFromPreset(t *testing.T) {
	for _, name := range PresetNames() {
		t.Run(name, func(t *testing.T) {
			b := NewFromPreset(name)
			result := b.WithDate("2026-04-07").Build()
			assert.NotEmpty(t, result)
			assert.Contains(t, result, "Guidelines:")
			assert.Contains(t, result, "Current date: 2026-04-07")
		})
	}
}

func TestPresets_NoDuplicateGuidelines(t *testing.T) {
	for name, preset := range Presets {
		seen := make(map[string]bool)
		for _, g := range preset.Guidelines {
			assert.False(t, seen[g], "duplicate guideline in %s: %q", name, g)
			seen[g] = true
		}
	}
}
