package session

import "github.com/DocumentDrivenDX/agent"

// ModelPricing holds per-million-token costs for a model.
// Alias of agent.ModelPricing — kept here for backward compatibility.
type ModelPricing = agent.ModelPricing

// PricingTable maps model IDs to their pricing.
// Alias of agent.PricingTable — kept here for backward compatibility.
type PricingTable = agent.PricingTable

// DefaultPricing contains built-in pricing for common models.
// Delegates to agent.DefaultPricing so both packages share one source of truth.
var DefaultPricing = agent.DefaultPricing
