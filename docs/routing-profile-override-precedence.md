# Routing Profile Override Precedence

Route resolution treats profiles as constraints plus preferences. Explicit pins
win only inside the active profile constraint.

Rule: explicit `--harness`, `--provider`, or `--model` pins override profile
tier selection when they remain compatible with the profile. A pin that violates
the profile's placement constraint returns a typed error instead of silently
substituting a different route.

Worked examples:

- `--profile local --harness claude` returns `ErrProfilePinConflict` when
  `claude` is a non-local subscription harness. The error names `Profile=local`,
  `ConflictingPin=Harness=claude`, and `ProfileConstraint=local-only`.
- `--profile default --model gpt-5.4` keeps the `default` profile's
  `local-first` preference and uses the explicit model to bypass tier model
  selection. Routing still applies candidate eligibility, discovery, cost, and
  tie-break rules.
- `--profile local --model opus-4.7` returns `ErrProfilePinConflict` if that
  model is only servable by non-local harnesses. The local profile never
  upgrades to a subscription route.

When a hard profile has no compatible candidates, route resolution returns
`ErrNoProfileCandidate`. For example, `--profile local` with no eligible local
endpoint returns an error naming `Profile=local` and missing capability
`local endpoint`.

Unknown profile names are typed errors. A request such as
`--profile does-not-exist` returns `ErrUnknownProfile` rather than falling back
to `default` or `local-first`.
