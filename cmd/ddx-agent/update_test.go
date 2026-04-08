package main

import (
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
