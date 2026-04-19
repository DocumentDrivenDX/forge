package prompt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPresetNames(t *testing.T) {
	names := PresetNames()
	require.Len(t, names, 7)
	assert.Equal(t, "agent", names[0]) // default first
	assert.Contains(t, names, "worker")
	assert.Contains(t, names, "benchmark")
}

func TestGetPreset(t *testing.T) {
	t.Run("known preset", func(t *testing.T) {
		p := GetPreset("codex")
		assert.Equal(t, "codex", p.Name)
		assert.Contains(t, p.Base, "pragmatic")
		assert.NotEmpty(t, p.Guidelines)
	})

	t.Run("unknown falls back to agent", func(t *testing.T) {
		p := GetPreset("nonexistent")
		assert.Equal(t, "agent", p.Name)
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
