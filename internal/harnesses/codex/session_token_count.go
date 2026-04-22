package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
)

const codexSessionsRootEnv = "DDX_AGENT_CODEX_SESSIONS_DIR"

const (
	defaultCodexSessionMaxFiles        = 128
	defaultCodexSessionMaxBytesPerFile = 8 << 20
	defaultCodexSessionMaxLineBytes    = 1 << 20
)

type codexSessionTokenCountConfig struct {
	root            string
	now             time.Time
	staleAfter      time.Duration
	maxFiles        int
	maxBytesPerFile int64
	maxLineBytes    int
}

// CodexSessionTokenCountOption adjusts bounded session token_count scanning.
type CodexSessionTokenCountOption func(*codexSessionTokenCountConfig)

func WithCodexSessionTokenCountRoot(root string) CodexSessionTokenCountOption {
	return func(cfg *codexSessionTokenCountConfig) { cfg.root = root }
}

func WithCodexSessionTokenCountNow(now time.Time) CodexSessionTokenCountOption {
	return func(cfg *codexSessionTokenCountConfig) { cfg.now = now }
}

func WithCodexSessionTokenCountLimits(maxFiles int, maxBytesPerFile int64, maxLineBytes int) CodexSessionTokenCountOption {
	return func(cfg *codexSessionTokenCountConfig) {
		cfg.maxFiles = maxFiles
		cfg.maxBytesPerFile = maxBytesPerFile
		cfg.maxLineBytes = maxLineBytes
	}
}

// CodexSessionsRoot returns the Codex session JSONL root. Tests may override
// this with DDX_AGENT_CODEX_SESSIONS_DIR; normal operation follows CODEX_HOME.
func CodexSessionsRoot() (string, error) {
	if root := strings.TrimSpace(os.Getenv(codexSessionsRootEnv)); root != "" {
		return root, nil
	}
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return filepath.Join(home, "sessions"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "sessions"), nil
}

// ReadCodexQuotaFromSessionTokenCounts returns the newest fresh quota snapshot
// found in Codex token_count events. It never reads session content for
// historical analytics and returns false for missing, stale, or malformed
// evidence so callers can fall back to the live PTY probe.
func ReadCodexQuotaFromSessionTokenCounts(opts ...CodexSessionTokenCountOption) (*CodexQuotaSnapshot, bool) {
	cfg := defaultCodexSessionTokenCountConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.root == "" {
		root, err := CodexSessionsRoot()
		if err != nil {
			return nil, false
		}
		cfg.root = root
	}
	candidates := newestCodexSessionFiles(cfg.root, cfg.maxFiles)
	for _, candidate := range candidates {
		snapshot, ok := readCodexTokenCountQuotaFromFile(candidate.path, candidate.modTime, cfg)
		if !ok {
			continue
		}
		if IsCodexQuotaFresh(snapshot, cfg.now, cfg.staleAfter) {
			return snapshot, true
		}
	}
	return nil, false
}

func defaultCodexSessionTokenCountConfig() codexSessionTokenCountConfig {
	return codexSessionTokenCountConfig{
		now:             time.Now().UTC(),
		staleAfter:      DefaultCodexQuotaStaleAfter,
		maxFiles:        defaultCodexSessionMaxFiles,
		maxBytesPerFile: defaultCodexSessionMaxBytesPerFile,
		maxLineBytes:    defaultCodexSessionMaxLineBytes,
	}
}

type codexSessionFile struct {
	path    string
	modTime time.Time
}

func newestCodexSessionFiles(root string, maxFiles int) []codexSessionFile {
	if maxFiles <= 0 {
		maxFiles = defaultCodexSessionMaxFiles
	}
	var files []codexSessionFile
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".jsonl" {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		files = append(files, codexSessionFile{path: path, modTime: info.ModTime().UTC()})
		sortCodexSessionFiles(files)
		if len(files) > maxFiles {
			files = files[:maxFiles]
		}
		return nil
	})
	sortCodexSessionFiles(files)
	return files
}

func sortCodexSessionFiles(files []codexSessionFile) {
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})
}

func readCodexTokenCountQuotaFromFile(path string, modTime time.Time, cfg codexSessionTokenCountConfig) (*CodexQuotaSnapshot, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false
	}
	if cfg.maxBytesPerFile <= 0 {
		cfg.maxBytesPerFile = defaultCodexSessionMaxBytesPerFile
	}
	if info.Size() > cfg.maxBytesPerFile {
		return nil, false
	}
	if cfg.maxLineBytes <= 0 {
		cfg.maxLineBytes = defaultCodexSessionMaxLineBytes
	}

	// #nosec G304 -- Codex session paths are intentionally discovered under
	// CODEX_HOME; scanning is size-bounded and content is not logged.
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(io.LimitReader(f, cfg.maxBytesPerFile))
	scanner.Buffer(make([]byte, 0, min(cfg.maxLineBytes, 64*1024)), cfg.maxLineBytes)
	var newest *CodexQuotaSnapshot
	for scanner.Scan() {
		snapshot, ok := parseCodexTokenCountQuotaLine(scanner.Bytes(), modTime)
		if !ok {
			continue
		}
		if newest == nil || snapshot.CapturedAt.After(newest.CapturedAt) {
			newest = snapshot
		}
	}
	if newest == nil {
		return nil, false
	}
	return newest, true
}

type codexTokenCountLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   struct {
		Type string `json:"type"`
		Info struct {
			RateLimits json.RawMessage `json:"rate_limits"`
		} `json:"info"`
		RateLimits json.RawMessage `json:"rate_limits"`
	} `json:"payload"`
}

func parseCodexTokenCountQuotaLine(raw []byte, fileModTime time.Time) (*CodexQuotaSnapshot, bool) {
	var ev codexTokenCountLine
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, false
	}
	if ev.Type != "event_msg" || ev.Payload.Type != "token_count" {
		return nil, false
	}
	rateLimits := codexTokenCountRateLimitsRaw(ev.Payload.RateLimits, ev.Payload.Info.RateLimits)
	if len(rateLimits) == 0 {
		return nil, false
	}
	snapshot, ok := CodexQuotaSnapshotFromTokenCountRateLimits(ev.Timestamp, fileModTime, rateLimits)
	if ok {
		snapshot.Source = "codex_session_token_count"
	}
	return snapshot, ok
}

// CodexQuotaSnapshotFromTokenCountRateLimits builds a quota snapshot from a
// token_count.rate_limits payload. fallbackCapturedAt must be evidence time:
// session readers pass file mtime, while live DDx-owned streams may pass now.
func CodexQuotaSnapshotFromTokenCountRateLimits(timestamp string, fallbackCapturedAt time.Time, rateLimits json.RawMessage) (*CodexQuotaSnapshot, bool) {
	capturedAt := codexTokenCountCapturedAt(timestamp, fallbackCapturedAt)
	windows, account, ok := codexQuotaSnapshotFromTokenCountRateLimits(rateLimits)
	if !ok {
		return nil, false
	}
	return &CodexQuotaSnapshot{
		CapturedAt: capturedAt,
		Windows:    windows,
		Source:     "codex_token_count",
		Account:    account,
	}, true
}

func codexTokenCountCapturedAt(timestamp string, fileModTime time.Time) time.Time {
	timestamp = strings.TrimSpace(timestamp)
	if timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
			return parsed.UTC()
		}
	}
	return fileModTime.UTC()
}

func codexQuotaSnapshotFromTokenCountRateLimits(raw json.RawMessage) ([]harnesses.QuotaWindow, *harnesses.AccountInfo, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, nil, false
	}
	var windows []harnesses.QuotaWindow
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		window, ok := codexQuotaWindowFromRaw(obj[key])
		if ok {
			windows = append(windows, window)
		}
	}
	if len(windows) == 0 {
		return nil, nil, false
	}
	var account *harnesses.AccountInfo
	if plan := stringFromRaw(obj["plan_type"]); plan != "" {
		account = &harnesses.AccountInfo{PlanType: normalizeCodexPlanType(plan)}
	}
	return windows, account, true
}

func codexQuotaWindowFromRaw(raw json.RawMessage) (harnesses.QuotaWindow, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return harnesses.QuotaWindow{}, false
	}
	windowMinutes, ok := intFromRaw(obj["window_minutes"])
	if !ok || windowMinutes <= 0 {
		return harnesses.QuotaWindow{}, false
	}
	usedPercent, ok := floatFromRaw(obj["used_percent"])
	if !ok {
		return harnesses.QuotaWindow{}, false
	}
	window := harnesses.QuotaWindow{
		Name:          codexQuotaWindowName(windowMinutes),
		LimitID:       stringFromRaw(obj["limit_id"]),
		LimitName:     stringFromRaw(obj["limit_name"]),
		WindowMinutes: windowMinutes,
		UsedPercent:   usedPercent,
		State:         harnesses.QuotaStateFromUsedPercent(int(usedPercent)),
	}
	if resetsAt, ok := int64FromRaw(obj["resets_at"]); ok && resetsAt > 0 {
		window.ResetsAtUnix = resetsAt
		window.ResetsAt = time.Unix(resetsAt, 0).UTC().Format(time.RFC3339)
	}
	return window, true
}

func codexQuotaWindowName(minutes int) string {
	if minutes%(24*60) == 0 {
		return fmt.Sprintf("%dd", minutes/(24*60))
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dm", minutes)
}

func stringFromRaw(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

func intFromRaw(raw json.RawMessage) (int, bool) {
	n, ok := int64FromRaw(raw)
	return int(n), ok
}

func int64FromRaw(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int64(f), true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		parsed, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return parsed, err == nil
	}
	return 0, false
}

func floatFromRaw(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return parsed, err == nil
	}
	return 0, false
}
