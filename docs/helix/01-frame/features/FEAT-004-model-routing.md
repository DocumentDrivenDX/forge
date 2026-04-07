---
ddx:
  id: FEAT-004
  depends_on:
    - helix.prd
    - FEAT-003
---
# Feature Specification: FEAT-004 — Provider Configuration

**Feature ID**: FEAT-004
**Status**: Draft
**Priority**: P0 (phase 1), P2 (phase 2)
**Owner**: Forge Team

## Overview

Provider configuration in Forge is deliberately simple. Phase 1: configure one
provider (base URL + model), it works or it doesn't. Phase 2: configure
multiple providers with round-robin distribution. No smart routing, no task
classification, no complexity heuristics.

## Problem Statement

- **Current situation**: DDx picks one harness per invocation. Switching
  providers requires changing the harness config.
- **Pain points**: No way to spread load or fail over between providers
  without manual intervention.
- **Desired outcome**: Phase 1 — dead-simple single-provider config. Phase 2 —
  multiple providers with round-robin for load distribution and basic failover.

## Requirements

### Functional Requirements

#### Phase 1 (P0): Single Provider

1. `Config` specifies one provider: type (openai-compat or anthropic), base
   URL, API key, model name
2. All requests go to that provider. If it fails, the request fails.
3. No fallback, no retry across providers (retries within a single provider
   are handled by FEAT-003)
4. The provider used is recorded in the Result

#### Phase 2 (P2): Multi-Provider Round Robin

5. `Config` specifies an ordered list of providers
6. Requests are distributed round-robin across available providers
7. If a provider fails, skip to the next one for that request
8. Provider health is tracked — temporarily skip providers that have failed
   recently (simple backoff, not circuit breaker)

### Non-Functional Requirements

- **Simplicity**: Phase 1 config is one struct with 4 fields. No YAML needed
  for library users.

## Edge Cases and Error Handling

- **Provider not reachable**: Return error immediately (phase 1). Skip to next
  provider (phase 2).
- **All providers fail (phase 2)**: Return error with details from each attempt

## Success Metrics

- Phase 1: Single-provider config works with LM Studio, Ollama, and Anthropic
- Phase 2: Round-robin distributes requests across providers

## Dependencies

- **Other features**: FEAT-003 (providers)
- **PRD requirements**: P0-3, P2-4

## Out of Scope

- Smart routing (task classification, context-length-based selection)
- Cost-based routing (pick cheapest model)
- A/B testing between models
- Concurrent multi-model execution (that's DDx quorum)
