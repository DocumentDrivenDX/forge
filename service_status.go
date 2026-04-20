package agent

import (
	"strings"
	"time"

	"github.com/DocumentDrivenDX/agent/internal/harnesses"
)

func statusError(status, source string, capturedAt time.Time) *StatusError {
	if status == "" || status == "connected" || status == "ok" {
		return nil
	}
	return &StatusError{
		Type:      statusErrorType(status),
		Detail:    status,
		Source:    source,
		Timestamp: capturedAt,
	}
}

func statusErrorType(status string) string {
	lower := strings.ToLower(status)
	switch {
	case strings.Contains(lower, "api_key") || strings.Contains(lower, "unauth") || strings.Contains(lower, "401") || strings.Contains(lower, "403"):
		return "unauthenticated"
	case lower == "unreachable" || strings.Contains(lower, "not found") || strings.Contains(lower, "connection") || strings.Contains(lower, "timeout"):
		return "unavailable"
	default:
		return "error"
	}
}

func quotaStatus(fresh bool, windows []harnesses.QuotaWindow) string {
	if len(windows) == 0 {
		return "unknown"
	}
	if !fresh {
		return "stale"
	}
	for _, w := range windows {
		if w.State == "blocked" {
			return "blocked"
		}
	}
	return "ok"
}

func accountStatusFromInfo(info *harnesses.AccountInfo, source string, capturedAt time.Time, fresh bool) *AccountStatus {
	if info == nil {
		return nil
	}
	return &AccountStatus{
		Authenticated: true,
		Email:         info.Email,
		PlanType:      info.PlanType,
		OrgName:       info.OrgName,
		Source:        source,
		CapturedAt:    capturedAt,
		Fresh:         fresh,
	}
}

func providerAuthStatus(entry ServiceProviderEntry, status string, capturedAt time.Time) AccountStatus {
	auth := AccountStatus{
		Source:     "service provider config",
		CapturedAt: capturedAt,
		Fresh:      true,
	}
	if statusErrorType(status) == "unauthenticated" {
		auth.Unauthenticated = true
		auth.Detail = status
		return auth
	}
	if entry.APIKey != "" {
		auth.Authenticated = true
		return auth
	}
	switch normalizeServiceProviderType(entry.Type) {
	case "anthropic", "openrouter":
		auth.Unauthenticated = true
		auth.Detail = "api_key not configured"
	default:
		auth.Detail = "authentication not required or not reported"
	}
	return auth
}

func providerEndpointStatus(entry ServiceProviderEntry, status string, modelCount int, capturedAt time.Time) []EndpointStatus {
	source := strings.TrimRight(entry.BaseURL, "/") + "/models"
	if entry.BaseURL == "" {
		source = "service provider config"
	}
	base := EndpointStatus{
		Name:       "default",
		BaseURL:    entry.BaseURL,
		ProbeURL:   source,
		Status:     endpointStatus(status),
		Source:     source,
		CapturedAt: capturedAt,
		Fresh:      true,
		ModelCount: modelCount,
		LastError:  statusError(status, source, capturedAt),
	}
	if base.Status == "connected" {
		base.LastSuccessAt = capturedAt
	}
	out := []EndpointStatus{base}
	for _, endpoint := range entry.Endpoints {
		out = append(out, EndpointStatus{
			Name:       endpoint.Name,
			BaseURL:    endpoint.BaseURL,
			ProbeURL:   strings.TrimRight(endpoint.BaseURL, "/") + "/models",
			Status:     "unknown",
			Source:     "service provider config",
			CapturedAt: capturedAt,
			Fresh:      false,
		})
	}
	return out
}

func providerQuotaState(entry ServiceProviderEntry, capturedAt time.Time) *QuotaState {
	switch normalizeServiceProviderType(entry.Type) {
	case "openrouter":
		source := strings.TrimRight(entry.BaseURL, "/") + "/auth/key"
		if entry.BaseURL == "" {
			source = "openrouter /auth/key"
		}
		if entry.APIKey == "" {
			return &QuotaState{
				Source:     source,
				Status:     "unauthenticated",
				CapturedAt: capturedAt,
				LastError: &StatusError{
					Type:      "unauthenticated",
					Detail:    "api_key not configured",
					Source:    source,
					Timestamp: capturedAt,
				},
			}
		}
		return &QuotaState{
			Source:     source,
			Status:     "unavailable",
			CapturedAt: capturedAt,
			LastError: &StatusError{
				Type:      "unavailable",
				Detail:    "quota probe not yet captured",
				Source:    source,
				Timestamp: capturedAt,
			},
		}
	default:
		return nil
	}
}

func endpointStatus(status string) string {
	if status == "connected" {
		return "connected"
	}
	if statusErrorType(status) == "unauthenticated" {
		return "unauthenticated"
	}
	if status == "unreachable" || statusErrorType(status) == "unavailable" {
		return "unreachable"
	}
	return "error"
}
