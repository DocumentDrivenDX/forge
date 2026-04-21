package codex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureCodexStatusOutput is a representative sanitized /status screen matching
// the real codex TUI format.
const fixtureCodexStatusOutput = `
› /status
  gpt-5.4 high · 100% left · $WORKTREE
Heads up, you have less than 5% of your weekly limit left.
`

func TestParseCodexStatusOutput_PrimaryWindow(t *testing.T) {
	windows := parseCodexStatusOutput(fixtureCodexStatusOutput)
	require.NotEmpty(t, windows)

	primary := windows[0]
	assert.Equal(t, "5h", primary.Name)
	assert.Equal(t, "codex", primary.LimitID)
	assert.Equal(t, 300, primary.WindowMinutes)
	assert.Equal(t, 0.0, primary.UsedPercent)
	assert.Equal(t, "ok", primary.State)
}

func TestParseCodexStatusOutput_WeeklyWarning(t *testing.T) {
	windows := parseCodexStatusOutput(fixtureCodexStatusOutput)
	require.Len(t, windows, 2)

	weekly := windows[1]
	assert.Equal(t, "7d", weekly.Name)
	assert.Equal(t, "codex", weekly.LimitID)
	assert.Equal(t, 10080, weekly.WindowMinutes)
	// "less than 5%" left → usedFloor = 95, state checked at 96
	assert.Equal(t, 95.0, weekly.UsedPercent)
	assert.Equal(t, "blocked", weekly.State)
}

func TestParseCodexStatusOutput_NoOutput(t *testing.T) {
	windows := parseCodexStatusOutput("Welcome to codex")
	assert.Empty(t, windows)
}

func TestParseCodexStatusOutput_MalformedPercent(t *testing.T) {
	windows := parseCodexStatusOutput("gpt-5.4 high · many left · /tmp/work")
	assert.Empty(t, windows)
}

func TestStripANSI_Codex(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"\x1b[1;32mGreen\x1b[0m", "Green"},
		{"\x1b[H\x1b[2JHello", "Hello"},
		{"No escapes", "No escapes"},
		{"\x1b[?2004htext\x1b[?2004l", "text"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, stripANSI(tc.input), "input: %q", tc.input)
	}
}
