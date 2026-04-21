package claude

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/DocumentDrivenDX/agent/internal/pty/cassette"
	"github.com/DocumentDrivenDX/agent/internal/pty/session"
	"github.com/DocumentDrivenDX/agent/internal/pty/terminal"
	"github.com/stretchr/testify/require"
)

func Test_quotaCassetteReplayClaude(t *testing.T) {
	dir := writeClaudeQuotaCassette(t, fixtureClaudeUsageOutput)

	windows, account, err := ReadClaudeQuotaFromCassette(dir)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, "Claude Max", account.PlanType)
	require.Len(t, windows, 4)

	reader, err := cassette.Open(dir)
	require.NoError(t, err)
	require.NotNil(t, reader.Quota())
	require.Equal(t, "pty", reader.Quota().Source)
}

func writeClaudeQuotaCassette(t *testing.T, text string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "claude-quota")
	size := session.Size{Rows: 50, Cols: 220}
	rec, err := cassette.Create(dir, cassette.Manifest{
		ID:      "claude-quota-replay",
		Harness: cassette.Harness{Name: "claude"},
		Command: cassette.Command{
			Argv:          []string{"claude"},
			WorkdirPolicy: "test",
			EnvAllowlist:  []string{"TERM", "LANG", "LC_ALL"},
			TimeoutMS:     1000,
		},
		Terminal: cassette.Terminal{
			InitialRows: int(size.Rows),
			InitialCols: int(size.Cols),
			Locale:      "C.UTF-8",
			Term:        "xterm-256color",
			PTYMode:     map[string]any{"outer": "test"},
			Emulator:    cassette.Emulator{Name: "vt10x", Version: "v0.0.0-20220301184237-5011da428d02"},
		},
		Timing: cassette.Timing{ResolutionMS: 50, ClockPolicy: "test", ReplayDefault: cassette.ReplayCollapsed},
		Provenance: cassette.Provenance{
			OS:              runtime.GOOS,
			Arch:            runtime.GOARCH,
			RecorderVersion: "quota-replay-test",
		},
	})
	require.NoError(t, err)

	emu := terminal.New(terminal.Size{Rows: int(size.Rows), Cols: int(size.Cols)})
	chunk := session.OutputChunk{Bytes: []byte(text)}
	_, err = rec.RecordOutput(chunk)
	require.NoError(t, err)
	frame, err := emu.Feed(chunk.Bytes)
	require.NoError(t, err)
	_, err = rec.RecordFrame(frame)
	require.NoError(t, err)
	windows, account := parseClaudeUsageOutput(text)
	require.NoError(t, rec.WriteQuota(quotaRecord(windows, map[string]any{"plan_type": accountPlan(account)})))
	require.NoError(t, rec.RecordFinal(cassette.FinalRecord{FinalText: text, Metadata: map[string]any{"harness": "claude"}}))
	require.NoError(t, rec.WriteScrubReport(cassette.ScrubReport{
		Status:                 "clean",
		Rules:                  []string{"test-fixture"},
		HitCounts:              map[string]int{},
		IntentionallyPreserved: []string{"sanitized-quota-fixture"},
	}))
	require.NoError(t, rec.Close())
	return dir
}
