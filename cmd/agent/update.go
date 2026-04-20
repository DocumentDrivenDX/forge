// Package main provides the ddx-agent CLI.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

const (
	defaultGitHubRepo = "DocumentDrivenDX/agent"
	defaultGitHubAPI  = "https://api.github.com"
	version           = "v0.0.8"  // Updated by release script
	updateCheckTTL    = time.Hour // Cache version check for 1 hour
)

var (
	githubRepo    = defaultGitHubRepo // Made var for testing.
	githubAPIBase = defaultGitHubAPI  // Injectable in tests to avoid live network calls.
)

// SemVer represents a semantic version.
type SemVer struct {
	Major      int
	Minor      int
	Patch      int
	PreRelease string // e.g., "rc1", "beta2"
}

// ParseSemVer parses a version string like "v0.0.8" or "1.2.3-beta".
// Returns error for non-semantic versions like "dev".
func ParseSemVer(v string) (SemVer, error) {
	// Handle special cases
	if v == "dev" || v == "" {
		return SemVer{}, fmt.Errorf("non-semantic version: %s", v)
	}

	v = strings.TrimPrefix(v, "v")

	// Split by '-' for pre-release
	parts := strings.SplitN(v, "-", 2)
	versionStr := parts[0]
	prerelease := ""
	if len(parts) > 1 {
		prerelease = parts[1]
	}

	// Parse major.minor.patch
	nums := strings.Split(versionStr, ".")
	if len(nums) != 3 {
		return SemVer{}, fmt.Errorf("invalid version format: %s", v)
	}

	var major, minor, patch int
	if _, err := fmt.Sscanf(nums[0], "%d", &major); err != nil {
		return SemVer{}, fmt.Errorf("invalid version number: %s", v)
	}
	if _, err := fmt.Sscanf(nums[1], "%d", &minor); err != nil {
		return SemVer{}, fmt.Errorf("invalid version number: %s", v)
	}
	if _, err := fmt.Sscanf(nums[2], "%d", &patch); err != nil {
		return SemVer{}, fmt.Errorf("invalid version number: %s", v)
	}

	return SemVer{Major: major, Minor: minor, Patch: patch, PreRelease: prerelease}, nil
}

// String returns the version string.
func (v SemVer) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.PreRelease != "" {
		s += "-" + v.PreRelease
	}
	return s
}

// Less returns true if v < other.
func (v SemVer) Less(other SemVer) bool {
	if v.Major != other.Major {
		return v.Major < other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor < other.Minor
	}
	// Pre-release versions are less than release versions
	if v.PreRelease != "" && other.PreRelease == "" {
		return true
	}
	if v.PreRelease == "" && other.PreRelease != "" {
		return false
	}
	return v.Patch < other.Patch || (v.Patch == other.Patch && v.PreRelease < other.PreRelease)
}

// GitHubRelease represents a GitHub release.
type GitHubRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"` // Release notes (markdown)
	PublishedAt time.Time `json:"published_at"`
}

// GetLatestRelease fetches the latest release from GitHub API.
func GetLatestRelease(repo string, cacheFile string) (*GitHubRelease, error) {
	// Check cache first
	if cached, err := loadCachedVersion(cacheFile); err == nil && time.Since(cached.Time) < updateCheckTTL {
		return &GitHubRelease{TagName: cached.Version}, nil
	}

	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(githubAPIBase, "/"), repo)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API returned %s: %s", resp.Status, string(body))
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("parsing release data: %w", err)
	}

	// Cache the result
	if err := saveCachedVersion(cacheFile, release.TagName); err != nil {
		return nil, err
	}

	return &release, nil
}

type cachedVersion struct {
	Version string    `json:"version"`
	Time    time.Time `json:"time"`
}

func loadCachedVersion(path string) (*cachedVersion, error) {
	data, err := safefs.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cv cachedVersion
	if err := json.Unmarshal(data, &cv); err != nil {
		return nil, err
	}
	return &cv, nil
}

func saveCachedVersion(path, version string) error {
	cv := cachedVersion{Version: version, Time: time.Now().UTC()}
	data, err := json.MarshalIndent(cv, "", "  ")
	if err != nil {
		return err
	}
	if err := safefs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return safefs.WriteFile(path, data, 0o600)
}

// FindBinaryPath finds the path to the ddx-agent binary.
func FindBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("getting executable path: %w", err)
	}

	dir := filepath.Dir(exe)
	// Check if in common locations
	if strings.Contains(dir, ".local/bin") ||
		strings.Contains(dir, "go/bin") ||
		strings.Contains(dir, "/bin/") {
		return exe, nil
	}

	// Fall back to which command
	cmd := exec.Command("which", "ddx-agent")
	output, err := cmd.Output()
	if err != nil {
		return exe, nil
	}
	path := strings.TrimSpace(string(output))
	if path == "" {
		return exe, nil // Return current executable as fallback
	}
	return path, nil
}

// DownloadBinary downloads the latest binary for the current platform.
func DownloadBinary(tag string, w io.Writer) (string, error) {
	osName := runtime.GOOS
	arch := runtime.GOARCH

	switch arch {
	case "amd64", "x86_64":
		arch = "amd64"
	case "arm64", "aarch64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("unsupported architecture: %s", arch)
	}

	binaryName := fmt.Sprintf("ddx-agent-%s-%s", osName, arch)
	// Use default GitHub URL for downloads (not the test override)
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", defaultGitHubRepo, tag, binaryName)

	fmt.Fprintf(w, "Downloading %s from %s...\n", tag, url)

	// Create temp file
	tmpFile, err := os.CreateTemp("", "ddx-agent-update-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Download to temp file
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		_ = safefs.Remove(tmpPath)
		return "", fmt.Errorf("downloading binary: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = safefs.Remove(tmpPath)
		return "", fmt.Errorf("download failed (%s): %s", resp.Status, string(body))
	}

	// Copy with progress
	buf := make([]byte, 32*1024)
	written := int64(0)
	total := resp.ContentLength

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			written += int64(n)
			if total > 0 {
				progress := float64(written) / float64(total) * 100
				fmt.Fprintf(w, "\r  Downloading: %.1f%%", progress)
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			_ = safefs.Remove(tmpPath)
			return "", fmt.Errorf("download interrupted: %w", err)
		}
		if _, writeErr := tmpFile.Write(buf[:n]); writeErr != nil {
			_ = tmpFile.Close()
			_ = safefs.Remove(tmpPath)
			return "", fmt.Errorf("writing temp file: %w", writeErr)
		}
	}

	if err := tmpFile.Close(); err != nil {
		_ = safefs.Remove(tmpPath)
		return "", fmt.Errorf("closing temp file: %w", err)
	}

	fmt.Fprintln(w) // Newline after progress

	// Verify download
	info, err := os.Stat(tmpPath)
	if err != nil {
		_ = safefs.Remove(tmpPath)
		return "", fmt.Errorf("checking downloaded file: %w", err)
	}
	if info.Size() < 10*1024 { // At least 10KB for a binary
		_ = safefs.Remove(tmpPath)
		return "", fmt.Errorf("downloaded file too small (%d bytes)", info.Size())
	}

	// Make executable
	if err := safefs.Chmod(tmpPath, 0o755); err != nil {
		_ = safefs.Remove(tmpPath)
		return "", fmt.Errorf("setting permissions: %w", err)
	}

	return tmpPath, nil
}

// ReplaceBinary atomically replaces the old binary with the new one.
func ReplaceBinary(oldPath, newPath string, w io.Writer) error {
	fmt.Fprintf(w, "Replacing binary at %s...\n", oldPath)

	// Get current permissions and owner
	info, err := os.Stat(oldPath)
	if err != nil {
		return fmt.Errorf("checking original binary: %w", err)
	}

	// Atomic rename (works on same filesystem)
	if err := os.Rename(newPath, oldPath); err != nil {
		// If rename fails (different filesystem), try copy+remove
		fmt.Fprintf(w, "  Note: Using fallback copy method...\n")

		// Read new binary
		data, readErr := safefs.ReadFile(newPath)
		if readErr != nil {
			_ = safefs.Remove(newPath)
			return fmt.Errorf("reading new binary: %w", readErr)
		}

		// Write to old path (atomic on most systems for small files)
		writeErr := safefs.WriteFile(oldPath, data, info.Mode())
		if writeErr != nil {
			_ = safefs.Remove(newPath)
			return fmt.Errorf("writing new binary: %w", writeErr)
		}

		// Clean up temp file
		_ = safefs.Remove(newPath)
	}

	// Preserve prior permission bits.
	if err := safefs.Chmod(oldPath, info.Mode()); err != nil {
		return fmt.Errorf("restoring original permissions: %w", err)
	}

	fmt.Fprintf(w, "Successfully updated ddx-agent\n")
	return nil
}
