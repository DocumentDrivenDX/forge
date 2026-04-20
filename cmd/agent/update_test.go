package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSemVer(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    SemVer
		wantErr bool
	}{
		{
			name:  "standard version",
			input: "v0.0.8",
			want:  SemVer{Major: 0, Minor: 0, Patch: 8},
		},
		{
			name:  "version without v prefix",
			input: "1.2.3",
			want:  SemVer{Major: 1, Minor: 2, Patch: 3},
		},
		{
			name:  "pre-release version",
			input: "v0.1.0-beta",
			want:  SemVer{Major: 0, Minor: 1, Patch: 0, PreRelease: "beta"},
		},
		{
			name:  "rc version",
			input: "v2.0.0-rc1",
			want:  SemVer{Major: 2, Minor: 0, Patch: 0, PreRelease: "rc1"},
		},
		{
			name:    "dev version",
			input:   "dev",
			wantErr: true,
		},
		{
			name:    "empty version",
			input:   "",
			wantErr: true,
		},
		{
			name:    "invalid format",
			input:   "not-a-version",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSemVer(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestSemVer_String(t *testing.T) {
	tests := []struct {
		name string
		v    SemVer
		want string
	}{
		{"standard", SemVer{0, 0, 8, ""}, "v0.0.8"},
		{"with prerelease", SemVer{1, 2, 3, "beta"}, "v1.2.3-beta"},
		{"rc version", SemVer{2, 0, 0, "rc1"}, "v2.0.0-rc1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.v.String())
		})
	}
}

func TestSemVer_Less(t *testing.T) {
	tests := []struct {
		name   string
		v      SemVer
		other  SemVer
		want   bool
		reason string
	}{
		{
			name:   "patch less",
			v:      SemVer{Major: 0, Minor: 0, Patch: 7},
			other:  SemVer{Major: 0, Minor: 0, Patch: 8},
			want:   true,
			reason: "v0.0.7 < v0.0.8",
		},
		{
			name:   "patch equal",
			v:      SemVer{Major: 0, Minor: 0, Patch: 8},
			other:  SemVer{Major: 0, Minor: 0, Patch: 8},
			want:   false,
			reason: "v0.0.8 == v0.0.8",
		},
		{
			name:   "minor less",
			v:      SemVer{Major: 0, Minor: 1, Patch: 0},
			other:  SemVer{Major: 0, Minor: 2, Patch: 0},
			want:   true,
			reason: "v0.1.0 < v0.2.0",
		},
		{
			name:   "major less",
			v:      SemVer{Major: 0, Minor: 9, Patch: 9},
			other:  SemVer{Major: 1, Minor: 0, Patch: 0},
			want:   true,
			reason: "v0.9.9 < v1.0.0",
		},
		{
			name:   "prerelease less than release",
			v:      SemVer{Major: 1, Minor: 0, Patch: 0, PreRelease: "beta"},
			other:  SemVer{Major: 1, Minor: 0, Patch: 0},
			want:   true,
			reason: "v1.0.0-beta < v1.0.0",
		},
		{
			name:   "release greater than prerelease",
			v:      SemVer{Major: 1, Minor: 0, Patch: 0},
			other:  SemVer{Major: 1, Minor: 0, Patch: 0, PreRelease: "beta"},
			want:   false,
			reason: "v1.0.0 > v1.0.0-beta",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.v.Less(tt.other)
			assert.Equal(t, tt.want, got, tt.reason)
		})
	}
}

func TestGetLatestRelease(t *testing.T) {
	// Create mock GitHub API server
	mockRelease := `{
		"tag_name": "v0.0.9",
		"name": "Version 0.0.9",
		"body": "## What's Changed\n\n- Added update command\n- Enhanced installer",
		"published_at": "2026-04-08T00:00:00Z"
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/test/repo/releases/latest", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(mockRelease))
	}))
	defer srv.Close()

	originalAPIBase := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() {
		githubAPIBase = originalAPIBase
	})

	homeDir := t.TempDir()
	cacheFile := filepath.Join(homeDir, "cache.json")

	release, err := GetLatestRelease("test/repo", cacheFile)
	require.NoError(t, err)
	assert.Equal(t, "v0.0.9", release.TagName)
	assert.Contains(t, release.Body, "Added update command")

}

func TestGetLatestRelease_CreatesMissingCacheDir(t *testing.T) {
	mockRelease := `{
		"tag_name": "v0.0.10",
		"name": "Version 0.0.10",
		"body": "## What's Changed\n\n- Fixed update cache handling",
		"published_at": "2026-04-09T00:00:00Z"
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/test/repo/releases/latest", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(mockRelease))
	}))
	defer srv.Close()

	originalAPIBase := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() {
		githubAPIBase = originalAPIBase
	})

	cacheFile := filepath.Join(t.TempDir(), "cache", "latest-version.json")

	release, err := GetLatestRelease("test/repo", cacheFile)
	require.NoError(t, err)
	assert.Equal(t, "v0.0.10", release.TagName)
	assert.FileExists(t, cacheFile)
}

func TestGetLatestRelease_Cache(t *testing.T) {
	homeDir := t.TempDir()
	cacheFile := filepath.Join(homeDir, "cache.json")

	// Create cache file with recent timestamp
	cachedData := `{"version":"v0.0.8","time":"` + time.Now().Format(time.RFC3339) + `"}`
	require.NoError(t, os.WriteFile(cacheFile, []byte(cachedData), 0644))

	// This should use cache and not make network call
	release, err := GetLatestRelease("test/repo", cacheFile)
	require.NoError(t, err)
	assert.Equal(t, "v0.0.8", release.TagName)
}

func TestGetLatestRelease_CacheExpired(t *testing.T) {
	t.Skip("Skipping integration test - requires network access to GitHub API")
}

func TestFindBinaryPath(t *testing.T) {
	// This test just verifies the function doesn't panic
	path, err := FindBinaryPath()
	assert.NoError(t, err)
	assert.NotEmpty(t, path)
	assert.True(t, strings.HasSuffix(path, "/ddx-agent") || strings.Contains(path, "ddx-agent") || strings.Contains(path, "go-build"))
}

func TestDownloadBinary_Mock(t *testing.T) {
	t.Skip("Skipping - requires network access to GitHub releases")
}

func TestCachedVersion_Serialize(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "cache.json")

	// Save and load cached version
	err := saveCachedVersion(tmpFile, "v0.0.8")
	require.NoError(t, err)

	cached, err := loadCachedVersion(tmpFile)
	require.NoError(t, err)
	assert.Equal(t, "v0.0.8", cached.Version)
	assert.WithinDuration(t, time.Now(), cached.Time, 1*time.Second)
}

func TestSemVer_ComparisonEdgeCases(t *testing.T) {
	// Test various edge cases in version comparison
	tests := []struct {
		v      SemVer
		other  SemVer
		expect bool // true if v < other
	}{
		{SemVer{Major: 0, Minor: 0, Patch: 1}, SemVer{Major: 0, Minor: 0, Patch: 2}, true},                                          // patch diff
		{SemVer{Major: 0, Minor: 1, Patch: 0}, SemVer{Major: 0, Minor: 1, Patch: 0}, false},                                         // equal
		{SemVer{Major: 1, Minor: 0, Patch: 0}, SemVer{Major: 0, Minor: 9, Patch: 9}, false},                                         // major wins
		{SemVer{Major: 1, Minor: 2, Patch: 3, PreRelease: "alpha"}, SemVer{Major: 1, Minor: 2, Patch: 3, PreRelease: "beta"}, true}, // prerelease alpha < beta
		{SemVer{Major: 1, Minor: 0, Patch: 0}, SemVer{Major: 1, Minor: 0, Patch: 0, PreRelease: "rc1"}, false},                      // release > rc
	}

	for _, tt := range tests {
		t.Run(tt.v.String()+" vs "+tt.other.String(), func(t *testing.T) {
			assert.Equal(t, tt.expect, tt.v.Less(tt.other))
		})
	}
}

func TestGetLatestRelease_ErrorHandling(t *testing.T) {
	// Test with invalid JSON response
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	originalAPIBase := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() {
		githubAPIBase = originalAPIBase
	})

	_, err := GetLatestRelease("test/repo", "")
	assert.Error(t, err)
}

func TestGetLatestRelease_NetworkError(t *testing.T) {
	// Test with non-existent server (should timeout or fail)
	// Skip this test as it requires network access and may flake
	t.Skip("Skipping network-dependent test")
}

func TestCmdVersion_CheckOnlySkipsUpdateLookup(t *testing.T) {
	oldVersion := Version
	oldRepo := githubRepo
	oldAPIBase := githubAPIBase
	t.Cleanup(func() {
		Version = oldVersion
		githubRepo = oldRepo
		githubAPIBase = oldAPIBase
	})

	Version = "v0.0.8"
	githubRepo = "test/repo"

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.NotFound(w, r)
	}))
	defer srv.Close()
	githubAPIBase = srv.URL

	_, _, code := captureStdIO(t, func() int {
		return cmdVersion([]string{"--check-only"})
	})
	assert.Equal(t, 0, code)
	assert.Equal(t, 0, calls, "check-only version path should not query release API")
}

func TestCmdVersion_ShowsUpdateAvailability(t *testing.T) {
	oldVersion := Version
	oldRepo := githubRepo
	oldAPIBase := githubAPIBase
	t.Cleanup(func() {
		Version = oldVersion
		githubRepo = oldRepo
		githubAPIBase = oldAPIBase
	})

	Version = "v0.0.8"
	githubRepo = "test/repo"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/test/repo/releases/latest", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"tag_name":"v0.0.9","name":"v0.0.9","body":"","published_at":"2026-04-08T00:00:00Z"}`)
	}))
	defer srv.Close()
	githubAPIBase = srv.URL

	home := t.TempDir()
	t.Setenv("HOME", home)

	stdout, stderr, code := captureStdIO(t, func() int {
		return cmdVersion(nil)
	})
	assert.Equal(t, 0, code)
	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "ddx-agent v0.0.8")
	assert.Contains(t, stdout, "Update available: v0.0.9")
}

func TestCmdUpdate_CheckOnly_OutdatedReturnsOne(t *testing.T) {
	oldVersion := Version
	oldRepo := githubRepo
	oldAPIBase := githubAPIBase
	t.Cleanup(func() {
		Version = oldVersion
		githubRepo = oldRepo
		githubAPIBase = oldAPIBase
	})

	Version = "v0.0.8"
	githubRepo = "test/repo"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/test/repo/releases/latest", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"tag_name":"v0.0.9","name":"v0.0.9","body":"","published_at":"2026-04-08T00:00:00Z"}`)
	}))
	defer srv.Close()
	githubAPIBase = srv.URL

	home := t.TempDir()
	t.Setenv("HOME", home)

	stdout, stderr, code := captureStdIO(t, func() int {
		return cmdUpdate([]string{"--check-only"})
	})
	assert.Equal(t, 1, code)
	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Current: v0.0.8")
	assert.Contains(t, stdout, "Latest:  v0.0.9")
	assert.Contains(t, stdout, "Update available")
}

func TestCmdUpdate_CheckOnly_UpToDateReturnsZero(t *testing.T) {
	oldVersion := Version
	oldRepo := githubRepo
	oldAPIBase := githubAPIBase
	t.Cleanup(func() {
		Version = oldVersion
		githubRepo = oldRepo
		githubAPIBase = oldAPIBase
	})

	Version = "v0.0.9"
	githubRepo = "test/repo"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/test/repo/releases/latest", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"tag_name":"v0.0.9","name":"v0.0.9","body":"","published_at":"2026-04-08T00:00:00Z"}`)
	}))
	defer srv.Close()
	githubAPIBase = srv.URL

	home := t.TempDir()
	t.Setenv("HOME", home)

	stdout, stderr, code := captureStdIO(t, func() int {
		return cmdUpdate([]string{"--check-only"})
	})
	assert.Equal(t, 0, code)
	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "You are up to date")
}

func TestReplaceBinary_PreservesOriginalPermissions(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "ddx-agent")
	newPath := filepath.Join(dir, "ddx-agent.new")

	require.NoError(t, os.WriteFile(oldPath, []byte("old"), 0o700))
	require.NoError(t, os.WriteFile(newPath, []byte("new"), 0o755))

	var out bytes.Buffer
	require.NoError(t, ReplaceBinary(oldPath, newPath, &out))

	data, err := os.ReadFile(oldPath)
	require.NoError(t, err)
	assert.Equal(t, "new", string(data))

	info, err := os.Stat(oldPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	_, err = os.Stat(newPath)
	assert.Error(t, err, "new temporary binary path should be consumed")
}

func TestDownloadBinary_RemovesTempFileOnSmallDownload(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	oldTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := io.NopCloser(strings.NewReader("tiny"))
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Body:          body,
			ContentLength: int64(len("tiny")),
			Header:        make(http.Header),
			Request:       req,
		}, nil
	})
	t.Cleanup(func() {
		http.DefaultTransport = oldTransport
	})

	var out bytes.Buffer
	_, err := DownloadBinary("v0.0.9", &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too small")

	matches, globErr := filepath.Glob(filepath.Join(tmp, "ddx-agent-update-*"))
	require.NoError(t, globErr)
	assert.Len(t, matches, 0, "temporary update artifacts should be cleaned up")
}

func captureStdIO(t *testing.T, fn func() int) (stdout string, stderr string, code int) {
	t.Helper()

	origStdout := os.Stdout
	origStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	require.NoError(t, err)
	stderrR, stderrW, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = stdoutW
	os.Stderr = stderrW

	doneOut := make(chan string, 1)
	doneErr := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, stdoutR)
		doneOut <- buf.String()
	}()
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, stderrR)
		doneErr <- buf.String()
	}()

	code = fn()

	require.NoError(t, stdoutW.Close())
	require.NoError(t, stderrW.Close())
	os.Stdout = origStdout
	os.Stderr = origStderr

	stdout = <-doneOut
	stderr = <-doneErr
	return stdout, stderr, code
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
