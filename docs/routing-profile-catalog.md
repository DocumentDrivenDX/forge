# Routing Profile Catalog

The model catalog is the source of truth for routing profile names and provider
preference policy. Each profile in `internal/modelcatalog/catalog/models.yaml`
declares a target tier and a `provider_preference` value consumed by route
resolution.

Provider preferences:

- `local-first`: prefer local endpoints when eligible, then fall back to
  subscription harnesses.
- `subscription-first`: prefer subscription harnesses with healthy quota, then
  fall back to local endpoints when policy allows.
- `local-only`: require local endpoints. If no eligible local endpoint exists,
  routing returns `ErrNoProfileCandidate`.
- `subscription-only`: require subscription harnesses.

| Profile | Intent | Provider preference | Expected cost class | Example use case |
|---|---|---:|---|---|
| `default` | Balanced default routing with local endpoints preferred and subscription fallback. Avoid pay-per-token endpoints unless explicitly configured. | `local-first` | local or quota-backed subscription | Normal unattended DDx work where the caller wants predictable cost control. |
| `local` | Hard local-only routing. Never upgrades to subscription harnesses. | `local-only` | local | Air-gapped development, private-code runs, or tests that must stay on a configured endpoint. |
| `offline` | Compatibility alias for hard local-only routing. | `local-only` | local | Offline automation using only configured local endpoints. |
| `air-gapped` | Compatibility alias for hard local-only routing. | `local-only` | local | Environments where network-backed harnesses are forbidden. |
| `standard` | Cost-balanced standard-capability tier. Prefer standard models and lower known cost when scores tie. | `local-first` | local, cheap, or medium | Day-to-day coding where fast enough and cost-aware is preferred over maximum capability. |
| `cheap` | Lowest reasonable cost tier. | `local-first` | local or cheap | Bulk edits, simple fixes, and queue work where cost matters most. |
| `smart` | High-capability tier for harder reasoning and code tasks. | `subscription-first` | medium or expensive subscription, with local fallback only when eligible | Complex design or implementation work that benefits from frontier models. |
| `fast` | Medium tier optimized for latency. | `local-first` | local or medium | Interactive work where response time matters. |
| `code-high` | Canonical high-capability coding tier. | `subscription-first` | medium or expensive subscription | Directly requesting the high coding tier without the `smart` alias. |
| `code-smart` | Compatibility alias for `code-high`. | `subscription-first` | medium or expensive subscription | Existing consumers that use the older smart coding profile name. |
| `code-medium` | Canonical medium coding tier. | `local-first` | local or medium | Directly requesting the balanced coding tier. |
| `code-fast` | Compatibility alias for `code-medium`. | `local-first` | local or medium | Existing consumers that use the older fast coding profile name. |
| `code-economy` | Canonical economy coding tier. | `local-first` | local or cheap | Directly requesting economy models for low-cost work. |

Tie-break order for eligible candidates is deterministic:

1. Higher score wins.
2. Lower known cost wins. Unknown costs use the neutral average of known costs.
3. Local cost class wins when score and cost tie.
4. Harness name sorts alphabetically.
5. Provider name sorts alphabetically.
