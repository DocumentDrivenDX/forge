package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DocumentDrivenDX/agent"
	agentConfig "github.com/DocumentDrivenDX/agent/config"
	"github.com/DocumentDrivenDX/agent/internal/safefs"
)

const defaultRouteHealthCooldown = 30 * time.Second

type routeHealthState struct {
	Failures map[string]time.Time `json:"failures,omitempty"`
}

type routeProvider struct {
	mu                sync.Mutex
	cfg               *agentConfig.Config
	workDir           string
	routeKey          string
	requestedModel    string
	requestedModelRef string
	candidates        []agentConfig.ModelRouteCandidateConfig
	order             []int
	allowDeprecated   bool
	selectedProvider  string
	attempted         []string
	failoverCount     int
}

func newRouteProvider(cfg *agentConfig.Config, workDir, routeKey, requestedModel, requestedModelRef string, route agentConfig.ModelRouteConfig, order []int, initialProvider string, allowDeprecated bool) *routeProvider {
	return &routeProvider{
		cfg:               cfg,
		workDir:           workDir,
		routeKey:          routeKey,
		requestedModel:    requestedModel,
		requestedModelRef: requestedModelRef,
		candidates:        append([]agentConfig.ModelRouteCandidateConfig(nil), route.Candidates...),
		order:             append([]int(nil), order...),
		allowDeprecated:   allowDeprecated,
		selectedProvider:  initialProvider,
	}
}

func (p *routeProvider) Chat(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (agent.Response, error) {
	var attempts []string
	var failures []string

	for i, candidateIndex := range p.order {
		candidate := p.candidates[candidateIndex]
		attempts = append(attempts, candidate.Provider)

		provider, providerCfg, err := p.buildCandidate(candidate)
		if err != nil {
			return agent.Response{}, err
		}
		resp, err := provider.Chat(ctx, messages, tools, withModelOverride(opts, providerCfg.Model))
		if err == nil {
			if resp.Attempt == nil {
				resp.Attempt = &agent.AttemptMetadata{}
			}
			resp.Attempt.ProviderName = candidate.Provider
			resp.Attempt.Route = p.routeKey
			if resp.Attempt.RequestedModel == "" {
				resp.Attempt.RequestedModel = p.requestedModel
			}
			p.recordAttempt(candidate.Provider, attempts, i, true)
			return resp, nil
		}

		failures = append(failures, fmt.Sprintf("%s: %v", candidate.Provider, err))
		if !shouldFailover(err) || i == len(p.order)-1 {
			p.markCandidateFailure(candidate.Provider)
			p.recordAttempt("", attempts, max(i, 0), false)
			if !shouldFailover(err) {
				return agent.Response{}, err
			}
			return agent.Response{}, &routeError{Route: p.routeKey, Attempts: failures}
		}
		p.markCandidateFailure(candidate.Provider)
	}

	return agent.Response{}, &routeError{Route: p.routeKey, Attempts: failures}
}

func (p *routeProvider) ChatStream(ctx context.Context, messages []agent.Message, tools []agent.ToolDef, opts agent.Options) (<-chan agent.StreamDelta, error) {
	var attempts []string
	var failures []string

	for i, candidateIndex := range p.order {
		candidate := p.candidates[candidateIndex]
		attempts = append(attempts, candidate.Provider)

		provider, providerCfg, err := p.buildCandidate(candidate)
		if err != nil {
			return nil, err
		}
		streamingProvider, ok := provider.(agent.StreamingProvider)
		if !ok {
			return nil, fmt.Errorf("agent: provider %q does not support streaming", candidate.Provider)
		}

		ch, err := streamingProvider.ChatStream(ctx, messages, tools, withModelOverride(opts, providerCfg.Model))
		if err == nil {
			p.recordAttempt(candidate.Provider, attempts, i, true)
			return ch, nil
		}

		failures = append(failures, fmt.Sprintf("%s: %v", candidate.Provider, err))
		if !shouldFailover(err) || i == len(p.order)-1 {
			p.markCandidateFailure(candidate.Provider)
			p.recordAttempt("", attempts, max(i, 0), false)
			if !shouldFailover(err) {
				return nil, err
			}
			return nil, &routeError{Route: p.routeKey, Attempts: failures}
		}
		p.markCandidateFailure(candidate.Provider)
	}

	return nil, &routeError{Route: p.routeKey, Attempts: failures}
}

func (p *routeProvider) SessionStartMetadata() (string, string) {
	model := ""
	if len(p.order) > 0 {
		model = p.candidates[p.order[0]].Model
	}
	return p.selectedProvider, model
}

func (p *routeProvider) ChatStartMetadata() (string, string, int) {
	return "", "", 0
}

func (p *routeProvider) RoutingReport() agent.RoutingReport {
	p.mu.Lock()
	defer p.mu.Unlock()
	return agent.RoutingReport{
		SelectedProvider:   p.selectedProvider,
		SelectedRoute:      p.routeKey,
		AttemptedProviders: append([]string(nil), p.attempted...),
		FailoverCount:      p.failoverCount,
	}
}

func (p *routeProvider) buildCandidate(candidate agentConfig.ModelRouteCandidateConfig) (agent.Provider, agentConfig.ProviderConfig, error) {
	overrides := agentConfig.ProviderOverrides{
		AllowDeprecated: p.allowDeprecated,
	}
	if candidate.Model != "" {
		overrides.Model = candidate.Model
	} else if p.requestedModelRef != "" {
		overrides.ModelRef = p.requestedModelRef
	}

	provider, pc, _, err := p.cfg.BuildProviderWithOverrides(candidate.Provider, overrides)
	if err != nil {
		return nil, agentConfig.ProviderConfig{}, err
	}
	if candidate.Model != "" {
		pc.Model = candidate.Model
		provider, err = buildProviderFromResolvedConfig(candidate.Provider, pc)
		if err != nil {
			return nil, agentConfig.ProviderConfig{}, err
		}
	}
	return provider, pc, nil
}

func (p *routeProvider) recordAttempt(provider string, attempts []string, failovers int, success bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if success {
		p.selectedProvider = provider
	}
	p.attempted = append(p.attempted, attempts...)
	p.failoverCount += failovers

	state, _ := loadRouteHealthState(p.workDir, p.routeKey)
	if state.Failures == nil {
		state.Failures = make(map[string]time.Time)
	}
	if success {
		delete(state.Failures, provider)
	}
	_ = saveRouteHealthState(p.workDir, p.routeKey, state)
}

func (p *routeProvider) markCandidateFailure(provider string) {
	state, _ := loadRouteHealthState(p.workDir, p.routeKey)
	if state.Failures == nil {
		state.Failures = make(map[string]time.Time)
	}
	state.Failures[provider] = time.Now().UTC()
	_ = saveRouteHealthState(p.workDir, p.routeKey, state)
}

type routeError struct {
	Route    string
	Attempts []string
}

func (e *routeError) Error() string {
	return fmt.Sprintf("agent: route %q failed after attempts: %s", e.Route, strings.Join(e.Attempts, " | "))
}

func withModelOverride(opts agent.Options, model string) agent.Options {
	out := opts
	out.Model = model
	return out
}

func routeAttemptOrder(route agentConfig.ModelRouteConfig, counter int, state routeHealthState, cooldown time.Duration) []int {
	switch route.Strategy {
	case "priority-round-robin":
		return priorityRoundRobinOrder(route.Candidates, counter, state, cooldown)
	default:
		return orderedFailoverOrder(route.Candidates, state, cooldown)
	}
}

func priorityRoundRobinOrder(candidates []agentConfig.ModelRouteCandidateConfig, counter int, state routeHealthState, cooldown time.Duration) []int {
	eligible := healthyCandidateIndexes(candidates, state, cooldown)
	if len(eligible) == 0 {
		eligible = make([]int, len(candidates))
		for i := range candidates {
			eligible[i] = i
		}
	}
	bestPriority := candidates[eligible[0]].Priority
	for _, idx := range eligible[1:] {
		if candidates[idx].Priority > bestPriority {
			bestPriority = candidates[idx].Priority
		}
	}
	var preferred []int
	var remainder []int
	for _, idx := range eligible {
		if candidates[idx].Priority == bestPriority {
			preferred = append(preferred, idx)
		} else {
			remainder = append(remainder, idx)
		}
	}
	if len(preferred) == 0 {
		return remainder
	}
	rotated := append([]int(nil), preferred...)
	if len(rotated) > 1 {
		offset := counter % len(rotated)
		rotated = append(rotated[offset:], rotated[:offset]...)
	}
	return append(rotated, remainder...)
}

func orderedFailoverOrder(candidates []agentConfig.ModelRouteCandidateConfig, state routeHealthState, cooldown time.Duration) []int {
	eligible := healthyCandidateIndexes(candidates, state, cooldown)
	if len(eligible) > 0 {
		return eligible
	}
	order := make([]int, len(candidates))
	for i := range candidates {
		order[i] = i
	}
	return order
}

func healthyCandidateIndexes(candidates []agentConfig.ModelRouteCandidateConfig, state routeHealthState, cooldown time.Duration) []int {
	now := time.Now().UTC()
	var eligible []int
	for i, candidate := range candidates {
		failedAt, ok := state.Failures[candidate.Provider]
		if !ok || now.Sub(failedAt) >= cooldown {
			eligible = append(eligible, i)
		}
	}
	return eligible
}

func shouldFailover(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "status code: 401"),
		strings.Contains(msg, "status code: 403"),
		strings.Contains(msg, "status code: 408"),
		strings.Contains(msg, "status code: 409"),
		strings.Contains(msg, "status code: 429"),
		strings.Contains(msg, "status code: 500"),
		strings.Contains(msg, "status code: 502"),
		strings.Contains(msg, "status code: 503"),
		strings.Contains(msg, "status code: 504"),
		strings.Contains(msg, "401 unauthorized"),
		strings.Contains(msg, "403 forbidden"),
		strings.Contains(msg, "408 request timeout"),
		strings.Contains(msg, "409 conflict"),
		strings.Contains(msg, "429 too many requests"),
		strings.Contains(msg, "500 internal server error"),
		strings.Contains(msg, "502 bad gateway"),
		strings.Contains(msg, "503 service unavailable"),
		strings.Contains(msg, "504 gateway timeout"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "dial tcp"),
		strings.Contains(msg, "no such host"),
		strings.Contains(msg, "timeout"),
		strings.Contains(msg, "temporarily unavailable"),
		strings.Contains(msg, "service unavailable"),
		strings.Contains(msg, "unreachable"),
		strings.Contains(msg, "connection reset"):
		return true
	default:
		return false
	}
}

func routeHealthStateFile(workDir, routeKey string) string {
	return filepath.Join(workDir, ".agent", "route-health-"+routeStateKey(routeKey)+".json")
}

func loadRouteHealthState(workDir, routeKey string) (routeHealthState, error) {
	path := routeHealthStateFile(workDir, routeKey)
	data, err := safefs.ReadFile(path)
	if err != nil {
		return routeHealthState{Failures: make(map[string]time.Time)}, nil
	}
	var state routeHealthState
	if err := json.Unmarshal(data, &state); err != nil {
		return routeHealthState{Failures: make(map[string]time.Time)}, nil
	}
	if state.Failures == nil {
		state.Failures = make(map[string]time.Time)
	}
	return state, nil
}

func saveRouteHealthState(workDir, routeKey string, state routeHealthState) error {
	path := routeHealthStateFile(workDir, routeKey)
	if err := safefs.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	// WriteFileAtomic prevents readers from seeing a partially-written file
	// if the process is interrupted between write and flush.
	return safefs.WriteFileAtomic(path, data, 0o600)
}

func routeHealthCooldown(cfg *agentConfig.Config) time.Duration {
	if cfg == nil || strings.TrimSpace(cfg.Routing.HealthCooldown) == "" {
		return defaultRouteHealthCooldown
	}
	d, err := time.ParseDuration(cfg.Routing.HealthCooldown)
	if err != nil || d <= 0 {
		return defaultRouteHealthCooldown
	}
	return d
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func routeStateKey(routeName string) string {
	replacer := strings.NewReplacer("/", "_", ":", "_", " ", "_")
	return replacer.Replace(routeName)
}
