package agent

// This file implements ListProviders and HealthCheck for the DdxAgent service.
// It lives in the root package to avoid import cycles; provider config data is
// injected via the ServiceConfig interface defined in service.go.
//
// Note: We cannot import agent/config or provider/openai here because both
// packages import the root agent package, creating a cycle. Provider probing
// is done inline using net/http.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
	claudeharness "github.com/DocumentDrivenDX/agent/internal/harnesses/claude"
	codexharness "github.com/DocumentDrivenDX/agent/internal/harnesses/codex"
)

const (
	defaultQuotaRefreshDebounce     = 15 * time.Minute
	defaultQuotaRefreshStartupWait  = 2 * time.Second
	defaultQuotaRefreshProbeTimeout = 30 * time.Second
)

// healthCheckClaudeQuotaRefresher is the function used to probe Claude's direct
// PTY quota. It is a package-level variable so tests can substitute a fake
// without spawning real harness sessions.
var healthCheckClaudeQuotaRefresher = func(timeout time.Duration) ([]harnesses.QuotaWindow, *harnesses.AccountInfo, error) {
	return claudeharness.ReadClaudeQuotaViaPTY(timeout)
}

var healthCheckCodexQuotaRefresher = func(timeout time.Duration) ([]harnesses.QuotaWindow, error) {
	return codexharness.ReadCodexQuotaViaPTY(timeout)
}

var healthCheckCodexSessionQuotaReader = func() (*codexharness.CodexQuotaSnapshot, bool) {
	return codexharness.ReadCodexQuotaFromSessionTokenCounts()
}

var primaryQuotaRefresh = &quotaRefreshCoordinator{
	lastAttempt: make(map[string]time.Time),
	inFlight:    make(map[string]bool),
}

type quotaRefreshCoordinator struct {
	mu          sync.Mutex
	lastAttempt map[string]time.Time
	inFlight    map[string]bool
}

type quotaRefreshMode int

const (
	quotaRefreshAsync quotaRefreshMode = iota
	quotaRefreshStartup
)

type quotaRefreshPolicy struct {
	debounce     time.Duration
	startupWait  time.Duration
	probeTimeout time.Duration
}

type quotaCacheStatus struct {
	needsRefresh bool
	usable       bool
}

func (s *service) ensurePrimaryQuotaRefresh(ctx context.Context, mode quotaRefreshMode) {
	policy := s.quotaRefreshPolicy()
	var waits []<-chan struct{}
	for _, name := range []string{"claude", "codex"} {
		status := primaryQuotaCacheStatus(name, policy.debounce)
		if !status.needsRefresh {
			continue
		}
		done := requestPrimaryQuotaRefresh(ctx, name, policy)
		if mode == quotaRefreshStartup && !status.usable && done != nil {
			waits = append(waits, done)
		}
	}
	if mode == quotaRefreshStartup && len(waits) > 0 && policy.startupWait > 0 {
		waitForPrimaryQuotaRefreshes(waits, policy.startupWait)
	}
}

func (s *service) quotaRefreshPolicy() quotaRefreshPolicy {
	policy := quotaRefreshPolicy{
		debounce:     defaultQuotaRefreshDebounce,
		startupWait:  defaultQuotaRefreshStartupWait,
		probeTimeout: defaultQuotaRefreshProbeTimeout,
	}
	if s != nil {
		if s.opts.QuotaRefreshDebounce > 0 {
			policy.debounce = s.opts.QuotaRefreshDebounce
		}
		if s.opts.QuotaRefreshStartupWait > 0 {
			policy.startupWait = s.opts.QuotaRefreshStartupWait
		}
	}
	return policy
}

func (s *service) startPrimaryQuotaRefreshWorker() {
	if s == nil || s.opts.QuotaRefreshInterval <= 0 {
		return
	}
	ctx := s.opts.QuotaRefreshContext
	if ctx == nil {
		ctx = context.Background()
	}
	interval := s.opts.QuotaRefreshInterval
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.ensurePrimaryQuotaRefresh(ctx, quotaRefreshAsync)
			}
		}
	}()
}

func requestPrimaryQuotaRefresh(ctx context.Context, harnessName string, policy quotaRefreshPolicy) <-chan struct{} {
	done := make(chan struct{})
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		close(done)
		return nil
	}

	now := time.Now()
	primaryQuotaRefresh.mu.Lock()
	if primaryQuotaRefresh.inFlight[harnessName] {
		primaryQuotaRefresh.mu.Unlock()
		close(done)
		return nil
	}
	if last := primaryQuotaRefresh.lastAttempt[harnessName]; !last.IsZero() && now.Sub(last) < policy.debounce {
		primaryQuotaRefresh.mu.Unlock()
		close(done)
		return nil
	}
	primaryQuotaRefresh.lastAttempt[harnessName] = now
	primaryQuotaRefresh.inFlight[harnessName] = true
	primaryQuotaRefresh.mu.Unlock()

	go func() {
		defer close(done)
		defer func() {
			primaryQuotaRefresh.mu.Lock()
			primaryQuotaRefresh.inFlight[harnessName] = false
			primaryQuotaRefresh.mu.Unlock()
		}()

		switch harnessName {
		case "claude":
			refreshClaudeQuotaCache(ctx, policy.debounce, policy.probeTimeout)
		case "codex":
			refreshCodexQuotaCache(ctx, policy.debounce, policy.probeTimeout)
		}
	}()
	return done
}

func waitForPrimaryQuotaRefreshes(waits []<-chan struct{}, timeout time.Duration) {
	deadline := time.After(timeout)
	for _, done := range waits {
		select {
		case <-done:
		case <-deadline:
			return
		}
	}
}

func primaryQuotaCacheStatus(harnessName string, debounce time.Duration) quotaCacheStatus {
	now := time.Now()
	switch harnessName {
	case "claude":
		cachePath, err := claudeharness.ClaudeQuotaCachePath()
		if err != nil {
			return quotaCacheStatus{}
		}
		snap, _ := claudeharness.ReadClaudeQuotaFrom(cachePath)
		if snap == nil {
			return quotaCacheStatus{needsRefresh: true}
		}
		decision := claudeharness.DecideClaudeQuotaRouting(snap, now, debounce)
		return quotaCacheStatus{
			needsRefresh: !decision.Fresh,
			usable:       decision.Fresh,
		}
	case "codex":
		cachePath, err := codexharness.CodexQuotaCachePath()
		if err != nil {
			return quotaCacheStatus{}
		}
		snap, _ := codexharness.ReadCodexQuotaFrom(cachePath)
		if snap == nil {
			return quotaCacheStatus{needsRefresh: true}
		}
		decision := codexharness.DecideCodexQuotaRouting(snap, now, debounce)
		return quotaCacheStatus{
			needsRefresh: !decision.Fresh || !codexQuotaCacheComplete(snap),
			usable:       decision.Fresh && decision.PreferCodex,
		}
	default:
		return quotaCacheStatus{}
	}
}

func codexQuotaCacheComplete(snap *codexharness.CodexQuotaSnapshot) bool {
	return snap != nil &&
		!snap.CapturedAt.IsZero() &&
		strings.TrimSpace(snap.Source) != "" &&
		len(snap.Windows) > 0 &&
		snap.Account != nil &&
		strings.TrimSpace(snap.Account.PlanType) != ""
}

// ListProviders returns providers known to the native-agent harness with live
// status, configured-default markers, and cooldown state.
func (s *service) ListProviders(ctx context.Context) ([]ProviderInfo, error) {
	sc := s.opts.ServiceConfig
	if sc == nil {
		return nil, fmt.Errorf("service: no ServiceConfig provided; pass ServiceOptions.ServiceConfig")
	}

	names := sc.ProviderNames()
	defaultName := sc.DefaultProviderName()
	cooldown := sc.HealthCooldown()
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}

	type indexedInfo struct {
		idx  int
		info ProviderInfo
	}
	results := make([]indexedInfo, len(names))
	var wg sync.WaitGroup

	for i, name := range names {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()

			entry, ok := sc.Provider(name)
			if !ok {
				results[idx] = indexedInfo{idx: idx, info: ProviderInfo{
					Name:   name,
					Status: "error: provider not found in config",
				}}
				return
			}

			info := ProviderInfo{
				Name:         name,
				Type:         normalizeServiceProviderType(entry.Type),
				BaseURL:      entry.BaseURL,
				Endpoints:    append([]ServiceProviderEndpoint(nil), entry.Endpoints...),
				IsDefault:    name == defaultName,
				DefaultModel: entry.Model,
			}

			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			capturedAt := time.Now().UTC()
			info.Status, info.ModelCount, info.Capabilities = probeServiceProvider(probeCtx, entry)
			info.CooldownState = serviceProviderCooldown(sc, name, cooldown)
			info.Auth = providerAuthStatus(entry, info.Status, capturedAt)
			info.EndpointStatus = providerEndpointStatus(entry, info.Status, info.ModelCount, capturedAt)
			info.Quota = providerQuotaState(entry, capturedAt)
			info.LastError = statusError(info.Status, info.EndpointStatus[0].Source, capturedAt)

			results[idx] = indexedInfo{idx: idx, info: info}
		}(i, name)
	}
	wg.Wait()

	out := make([]ProviderInfo, len(names))
	for _, r := range results {
		out[r.idx] = r.info
	}
	return out, nil
}

// HealthCheck triggers a fresh probe for the named target and updates internal state.
// target.Type is "harness" or "provider".
func (s *service) HealthCheck(ctx context.Context, target HealthTarget) error {
	switch target.Type {
	case "provider":
		sc := s.opts.ServiceConfig
		if sc == nil {
			return fmt.Errorf("service: no ServiceConfig provided; pass ServiceOptions.ServiceConfig")
		}
		entry, ok := sc.Provider(target.Name)
		if !ok {
			return fmt.Errorf("service: provider %q not found", target.Name)
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		status, _, _ := probeServiceProvider(probeCtx, entry)
		if status == "connected" {
			return nil
		}
		return fmt.Errorf("service: provider %q: %s", target.Name, status)

	case "harness":
		statuses := s.registry.Discover()
		for _, st := range statuses {
			if st.Name != target.Name {
				continue
			}
			if !st.Available {
				return fmt.Errorf("service: harness %q unavailable: %s", target.Name, st.Error)
			}
			// For subscription harnesses, refresh the quota cache when stale.
			if target.Name == "claude" {
				healthCheckRefreshClaudeQuota(ctx)
			}
			if target.Name == "codex" {
				healthCheckRefreshCodexQuota(ctx)
			}
			return nil
		}
		return fmt.Errorf("service: harness %q not registered", target.Name)

	default:
		return fmt.Errorf("service: unknown HealthTarget.Type %q (want \"harness\" or \"provider\")", target.Type)
	}
}

// probeServiceProvider pings a provider and returns (status, modelCount, capabilities).
func probeServiceProvider(ctx context.Context, entry ServiceProviderEntry) (status string, modelCount int, caps []string) {
	switch entry.Type {
	case "anthropic":
		if entry.APIKey == "" {
			return "error: api_key not configured", 0, nil
		}
		// Anthropic does not expose an unauthenticated /v1/models list endpoint.
		// Treat key presence as the connectivity signal.
		return "connected", 0, []string{"tool_use", "vision", "streaming"}

	case "openai", "openrouter", "lmstudio", "omlx", "ollama", "minimax", "qwen", "zai", "":
		if entry.BaseURL == "" {
			return "error: base_url not configured", 0, nil
		}
		n, err := discoverOpenAIModels(ctx, entry.BaseURL, entry.APIKey)
		if err != nil {
			msg := err.Error()
			if serviceIsUnreachable(msg) {
				return "unreachable", 0, nil
			}
			return "error: " + serviceTrimError(msg), 0, nil
		}
		return "connected", n, []string{"tool_use", "streaming", "json_mode"}

	default:
		return "error: unknown provider type " + entry.Type, 0, nil
	}
}

// discoverOpenAIModels queries the /v1/models endpoint and returns the model count.
// This is a minimal inline version of provider/openai.DiscoverModels that avoids
// the import cycle (provider/openai imports the root agent package).
func discoverOpenAIModels(ctx context.Context, baseURL, apiKey string) (int, error) {
	base := strings.TrimRight(baseURL, "/")
	endpoint := base + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("discovery: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("discovery: %s returned HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var mr struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return 0, fmt.Errorf("discovery: decode response from %s: %w", endpoint, err)
	}
	return len(mr.Data), nil
}

// serviceProviderCooldown scans all model routes for any candidate matching
// providerName and returns the first active CooldownState, or nil.
func serviceProviderCooldown(sc ServiceConfig, providerName string, cooldown time.Duration) *CooldownState {
	workDir := sc.WorkDir()
	if workDir == "" {
		return nil
	}
	now := time.Now().UTC()
	for _, routeName := range sc.ModelRouteNames() {
		for _, candidate := range sc.ModelRouteCandidates(routeName) {
			if candidate != providerName {
				continue
			}
			failures := serviceLoadRouteFailures(workDir, routeName)
			failedAt, hasFail := failures[providerName]
			if !hasFail {
				continue
			}
			until := failedAt.Add(cooldown)
			if until.Before(now) {
				continue
			}
			return &CooldownState{
				Reason:    "consecutive_failures",
				Until:     until,
				FailCount: 1,
			}
		}
	}
	return nil
}

// serviceLoadRouteFailures reads the file-backed route health state and returns
// the Failures map (provider name → failure timestamp).
func serviceLoadRouteFailures(workDir, routeName string) map[string]time.Time {
	type routeHealthState struct {
		Failures map[string]time.Time `json:"failures,omitempty"`
	}
	key := serviceRouteStateKey(routeName)
	path := filepath.Join(workDir, ".agent", "route-health-"+key+".json")
	// #nosec G304 -- operator-managed path under workDir
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rs routeHealthState
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil
	}
	return rs.Failures
}

func normalizeServiceProviderType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if t == "" {
		return "openai"
	}
	return t
}

func serviceIsUnreachable(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "dial tcp") ||
		strings.Contains(lower, "unreachable") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "i/o timeout")
}

func serviceTrimError(msg string) string {
	const maxLen = 120
	if len(msg) > maxLen {
		return msg[:maxLen] + "..."
	}
	return msg
}

func serviceRouteStateKey(routeName string) string {
	replacer := strings.NewReplacer("/", "_", ":", "_", " ", "_")
	return replacer.Replace(routeName)
}

// healthCheckRefreshClaudeQuota refreshes the Claude direct PTY quota cache when
// the cached snapshot is older than the default refresh debounce. It is a
// best-effort operation: errors are silently discarded so that a claude absence
// does not fail HealthCheck.
func healthCheckRefreshClaudeQuota(ctx context.Context) {
	refreshClaudeQuotaCache(ctx, defaultQuotaRefreshDebounce, defaultQuotaRefreshProbeTimeout)
}

func refreshClaudeQuotaCache(_ context.Context, debounce, timeout time.Duration) {
	cachePath, err := claudeharness.ClaudeQuotaCachePath()
	if err != nil {
		return
	}

	snap, _ := claudeharness.ReadClaudeQuotaFrom(cachePath)
	if snap != nil && claudeharness.DecideClaudeQuotaRouting(snap, time.Now(), debounce).Fresh {
		// Cache is fresh enough; skip the expensive PTY probe.
		return
	}

	// Cache is absent or stale - run a direct PTY probe with a bounded timeout.
	windows, acct, probeErr := healthCheckClaudeQuotaRefresher(timeout)
	if probeErr != nil || len(windows) == 0 {
		return
	}

	// Convert QuotaWindow slice to ClaudeQuotaSnapshot. The PTY path returns
	// percent-based windows; synthesise remaining counts from UsedPercent.
	newSnap := claudeharness.ClaudeQuotaSnapshot{
		CapturedAt: time.Now().UTC(),
		Source:     "pty",
		Account:    acct,
	}
	for _, w := range windows {
		switch w.LimitID {
		case "session":
			// 5-hour window; use a nominal limit of 100 so Remaining = 100-Used.
			used := int(w.UsedPercent)
			newSnap.FiveHourLimit = 100
			newSnap.FiveHourRemaining = 100 - used
		case "weekly-all", "weekly-sonnet":
			used := int(w.UsedPercent)
			newSnap.WeeklyLimit = 100
			newSnap.WeeklyRemaining = 100 - used
		}
	}

	_ = claudeharness.WriteClaudeQuota(cachePath, newSnap)
}

func healthCheckRefreshCodexQuota(ctx context.Context) {
	refreshCodexQuotaCache(ctx, defaultQuotaRefreshDebounce, defaultQuotaRefreshProbeTimeout)
}

func refreshCodexQuotaCache(_ context.Context, debounce, timeout time.Duration) {
	cachePath, err := codexharness.CodexQuotaCachePath()
	if err != nil {
		return
	}

	snap, _ := codexharness.ReadCodexQuotaFrom(cachePath)
	if snap != nil && codexharness.IsCodexQuotaFresh(snap, time.Now(), debounce) && codexQuotaCacheComplete(snap) {
		return
	}

	if sessionSnap, ok := healthCheckCodexSessionQuotaReader(); ok && codexSessionQuotaUsable(sessionSnap, debounce) {
		_ = codexharness.WriteCodexQuota(cachePath, *sessionSnap)
		return
	}

	windows, probeErr := healthCheckCodexQuotaRefresher(timeout)
	if probeErr != nil || len(windows) == 0 {
		return
	}

	_ = codexharness.WriteCodexQuota(cachePath, codexharness.CodexQuotaSnapshot{
		CapturedAt: time.Now().UTC(),
		Windows:    windows,
		Source:     "pty",
	})
}

func codexSessionQuotaUsable(snap *codexharness.CodexQuotaSnapshot, debounce time.Duration) bool {
	if !codexQuotaCacheComplete(snap) {
		return false
	}
	decision := codexharness.DecideCodexQuotaRouting(snap, time.Now(), debounce)
	return decision.Fresh && decision.PreferCodex
}
