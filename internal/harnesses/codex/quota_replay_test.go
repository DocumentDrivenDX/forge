package codex

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/DocumentDrivenDX/agent/internal/pty/cassette"
	"github.com/DocumentDrivenDX/agent/internal/pty/session"
	"github.com/DocumentDrivenDX/agent/internal/pty/terminal"
	"github.com/stretchr/testify/require"
)

func Test_quotaCassetteReplayCodex(t *testing.T) {
	dir := writeCodexQuotaCassette(t, fixtureCodexStatusOutput)

	windows, err := ReadCodexQuotaFromCassette(dir)
	require.NoError(t, err)
	require.Len(t, windows, 2)

	reader, err := cassette.Open(dir)
	require.NoError(t, err)
	require.NotNil(t, reader.Quota())
	require.Equal(t, "pty", reader.Quota().Source)
}

func writeCodexQuotaCassette(t *testing.T, text string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "codex-quota")
	size := session.Size{Rows: 50, Cols: 220}
	rec, err := cassette.Create(dir, cassette.Manifest{
		ID:      "codex-quota-replay",
		Harness: cassette.Harness{Name: "codex"},
		Command: cassette.Command{
			Argv:          []string{"codex", "--no-alt-screen"},
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
	windows := parseCodexStatusOutput(text)
	require.NoError(t, rec.WriteQuota(quotaRecord(windows)))
	require.NoError(t, rec.RecordFinal(cassette.FinalRecord{FinalText: text, Metadata: map[string]any{"harness": "codex"}}))
	require.NoError(t, rec.WriteScrubReport(cassette.ScrubReport{
		Status:                 "clean",
		Rules:                  []string{"test-fixture"},
		HitCounts:              map[string]int{},
		IntentionallyPreserved: []string{"sanitized-quota-fixture"},
	}))
	require.NoError(t, rec.Close())
	return dir
}
