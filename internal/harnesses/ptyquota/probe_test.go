package ptyquota

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/pty/cassette"
	"github.com/DocumentDrivenDX/agent/internal/pty/session"
	"github.com/stretchr/testify/require"
)

func TestErrorStatus(t *testing.T) {
	require.Equal(t, StatusOK, ErrorStatus(nil))
	require.Equal(t, StatusUnavailable, ErrorStatus(&ProbeError{Status: StatusUnavailable}))
	require.Equal(t, StatusError, ErrorStatus(errors.New("plain error")))
}

func TestRunMissingBinaryIsUnavailable(t *testing.T) {
	_, err := Run(context.Background(), Config{
		HarnessName: "missing",
		Binary:      "/definitely/missing/quota-probe-binary",
		Timeout:     time.Second,
	})
	require.Error(t, err)
	require.Equal(t, StatusUnavailable, ErrorStatus(err))
}

func TestRunAuthTextIsUnauthenticated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed PTY probes require Unix PTY support")
	}
	_, err := Run(context.Background(), Config{
		HarnessName:  "fake",
		Binary:       "sh",
		Args:         []string{"-c", "printf 'Please log in to continue'; sleep 5"},
		ReadyMarkers: []string{"never-ready"},
		Timeout:      2 * time.Second,
		Size:         session.Size{Rows: 8, Cols: 80},
	})
	require.Error(t, err)
	require.Equal(t, StatusUnauthenticated, ErrorStatus(err))
}

func TestRunTimeoutDoesNotPromoteCassette(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed PTY probes require Unix PTY support")
	}
	target := filepath.Join(t.TempDir(), "timeout-cassette")
	_, err := Run(context.Background(), Config{
		HarnessName:  "fake",
		Binary:       "sh",
		Args:         []string{"-c", "sleep 5"},
		ReadyMarkers: []string{"never-ready"},
		Timeout:      100 * time.Millisecond,
		Size:         session.Size{Rows: 8, Cols: 80},
		CassetteDir:  target,
	})
	require.Error(t, err)
	require.Equal(t, StatusError, ErrorStatus(err))
	_, statErr := os.Stat(target)
	require.True(t, errors.Is(statErr, os.ErrNotExist), "timeout should not leave accepted cassette evidence")
}

func TestRunReturnsWhenProcessExitsBeforeMarkers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed PTY probes require Unix PTY support")
	}
	start := time.Now()
	_, err := Run(context.Background(), Config{
		HarnessName:  "fake",
		Binary:       "sh",
		Args:         []string{"-c", "printf 'started\\n'"},
		ReadyMarkers: []string{"never-ready"},
		Timeout:      2 * time.Second,
		Size:         session.Size{Rows: 8, Cols: 80},
	})
	require.ErrorContains(t, err, "exited before expected output")
	require.Less(t, time.Since(start), time.Second)
}

func TestRunRequiresAllDoneMarkers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed PTY probes require Unix PTY support")
	}
	target := filepath.Join(t.TempDir(), "partial-cassette")
	_, err := Run(context.Background(), Config{
		HarnessName: "fake",
		Binary:      "sh",
		Args:        []string{"-c", "printf 'first marker'; sleep 5"},
		DoneMarkers: []string{"first marker", "second marker"},
		Timeout:     100 * time.Millisecond,
		Size:        session.Size{Rows: 8, Cols: 80},
		CassetteDir: target,
		Quota: func(string) (cassette.QuotaRecord, error) {
			return cassette.QuotaRecord{Source: "pty", Status: string(StatusOK)}, nil
		},
	})
	require.Error(t, err)
	_, statErr := os.Stat(target)
	require.True(t, errors.Is(statErr, os.ErrNotExist), "partial output should not leave accepted cassette evidence")
}

func TestRunDoesNotTreatHealthyOAuthTextAsAuthFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed PTY probes require Unix PTY support")
	}
	_, err := Run(context.Background(), Config{
		HarnessName: "fake",
		Binary:      "sh",
		Args:        []string{"-c", "printf 'Authenticated with OAuth\\r\\n100%% left\\r\\n'; sleep 1"},
		DoneMarkers: []string{"% left"},
		Timeout:     2 * time.Second,
		Size:        session.Size{Rows: 8, Cols: 80},
		Quota: func(string) (cassette.QuotaRecord, error) {
			return cassette.QuotaRecord{Source: "pty", Status: string(StatusOK)}, nil
		},
	})
	require.NoError(t, err)
}

func TestRunDoesNotTreatWorkdirPathContainingSignInAsAuthFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed PTY probes require Unix PTY support")
	}
	_, err := Run(context.Background(), Config{
		HarnessName: "fake",
		Binary:      "sh",
		Args:        []string{"-c", "printf 'gpt-5.4 high · 100%% left · /tmp/sign in/project\\r\\n'; sleep 1"},
		DoneMarkers: []string{"% left"},
		Timeout:     2 * time.Second,
		Size:        session.Size{Rows: 8, Cols: 120},
		Quota: func(string) (cassette.QuotaRecord, error) {
			return cassette.QuotaRecord{Source: "pty", Status: string(StatusOK)}, nil
		},
	})
	require.NoError(t, err)
}

func TestRunRefusesToOverwriteNewerCassetteVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed PTY probes require Unix PTY support")
	}
	target := filepath.Join(t.TempDir(), "future-cassette")
	require.NoError(t, os.MkdirAll(target, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(target, cassette.ManifestFile), []byte(`{"version":2}`), 0o600))

	_, err := Run(context.Background(), Config{
		HarnessName: "fake",
		Binary:      "sh",
		Args:        []string{"-c", "printf '100%% left\\n'"},
		DoneMarkers: []string{"% left"},
		Timeout:     2 * time.Second,
		Size:        session.Size{Rows: 8, Cols: 80},
		CassetteDir: target,
		Quota: func(string) (cassette.QuotaRecord, error) {
			return cassette.QuotaRecord{Source: "pty", Status: string(StatusOK)}, nil
		},
	})
	require.ErrorContains(t, err, "refuse to overwrite newer schema")
	raw, readErr := os.ReadFile(filepath.Join(target, cassette.ManifestFile))
	require.NoError(t, readErr)
	require.JSONEq(t, `{"version":2}`, string(raw))
}

func TestRunScrubsCassetteOutputBeforePromotion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed PTY probes require Unix PTY support")
	}
	root := t.TempDir()
	workdir := filepath.Join(root, "worktree")
	require.NoError(t, os.MkdirAll(workdir, 0o750))
	target := filepath.Join(root, "quota-cassette")

	_, err := Run(context.Background(), Config{
		HarnessName: "fake",
		Binary:      "sh",
		Args:        []string{"-c", "printf 'alice@example.com %s 100%% left\\n' \"$PWD\""},
		Workdir:     workdir,
		DoneMarkers: []string{"% left"},
		Timeout:     2 * time.Second,
		Size:        session.Size{Rows: 8, Cols: 120},
		CassetteDir: target,
		Quota: func(string) (cassette.QuotaRecord, error) {
			return cassette.QuotaRecord{Source: "pty", Status: string(StatusOK)}, nil
		},
	})
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(target, cassette.OutputRawFile))
	require.NoError(t, err)
	require.NotContains(t, string(raw), workdir)
	require.Contains(t, string(raw), "$WORKTREE")

	reader, err := cassette.Open(target)
	require.NoError(t, err)
	require.Equal(t, "redacted", reader.ScrubReport().Status)
	require.NotZero(t, reader.ScrubReport().HitCounts["email"])
	require.NotContains(t, strings.Join(reader.Manifest().Command.Argv, " "), workdir)
}
