# Evidence-Grade Terminal-Bench Comparison

Date: 2026-04-10

## Inputs

- `before_sha`: `9c8b5b09fb4058545201b8d72a21b0861e4a4cdb`
- `after_sha`: `dcc2f4512207e8b80d9f70bb2aeb7cb6c3913077`
- Shared config: [scripts/benchmark/evidence-grade-comparison.env](/Users/erik/Projects/agent/scripts/benchmark/evidence-grade-comparison.env)
- Subset: [scripts/benchmark/task-subset-v2.yaml](/Users/erik/Projects/agent/scripts/benchmark/task-subset-v2.yaml)
- Dataset: `terminal-bench@2.0`
- Provider route: `openrouter`
- Model: `anthropic/claude-opus-4.6-fast`
- Preset: `benchmark`
- Runtime: `docker`

## Artifacts

- Before report: [report-20260411T010657Z.json](/Users/erik/Projects/agent/benchmark-results/evidence-grade/report-20260411T010657Z.json)
- After report: [report-20260411T013232Z.json](/Users/erik/Projects/agent/benchmark-results/evidence-grade/report-20260411T013232Z.json)

## Summary

The initial full 15-task before/after comparison is not valid as a final
performance claim.

| Metric | Before | After | Delta |
| --- | ---: | ---: | ---: |
| Resolved-task rate | 53.33% (8/15) | 13.33% (2/15) | -40.00 pts |
| Clarification-question rate | 0.0000 | 0.0000 | 0.0000 |
| Shell anti-pattern rate | 0.3366 | 0.0851 | -0.2515 |
| Structured-edit success rate | 1.0000 | 1.0000 | 0.0000 |
| Average task duration | 101.9s | 74.9s | -26.9s |

Those raw numbers are preserved for auditability, but they should not be used as
the final conclusion because the run pair violated two important controls after
inspection:

1. The `after` run exhausted OpenRouter credits at `headless-terminal`, so 11 of
   the 13 `after` failures are infrastructure/provider failures (`402 Payment Required`)
   rather than valid task outcomes.
2. The `before_sha` predates the `benchmark` preset. Running `--preset benchmark`
   on that binary fell back to the default `agent` preset, so the run pair did
   not actually hold prompt behavior constant.

The result is still useful diagnostically, but not as evidence that the full
ForgeCode-inspired bundle regressed the 15-task subset by 40 points.

## Task-Level Changes

### Raw artifact deltas

Improved:

- `cancel-async-tasks`: fail -> pass

Regressed:

- `build-pmars`: pass -> fail
- `code-from-image`: pass -> fail
- `extract-elf`: pass -> fail
- `fix-code-vulnerability`: pass -> fail
- `git-multibranch`: pass -> fail
- `log-summary-date-ranges`: pass -> fail
- `multi-source-data-merger`: pass -> fail

Unchanged pass:

- `git-leak-recovery`

Unchanged fail:

- `break-filter-js-from-html`
- `build-cython-ext`
- `configure-git-webserver`
- `db-wal-recovery`
- `headless-terminal`
- `password-recovery`

### Valid task outcomes before provider exhaustion

The `after` run remained valid through the first four tasks only:

- `cancel-async-tasks`: fail -> pass
- `build-pmars`: pass -> fail
- `code-from-image`: pass -> fail
- `git-leak-recovery`: pass -> pass

From task 5 onward (`headless-terminal` and later), the `after` artifacts show
provider-side `402 Payment Required` failures and are not valid benchmark data.

## Interpretation

The valid conclusion from this run pair is narrower than the original headline:

- The full 15-task bundle comparison is invalid and must be rerun with sufficient credits.
- Within the valid early prefix, there is evidence of a real behavioral regression on
  at least `build-pmars`, and possibly `code-from-image`.
- The strongest concrete regression signature is `build-pmars`: the newer binary used
  the newly added `task` tool for bookkeeping, then hit iteration limit after the
  build succeeded but before installing `/usr/local/bin/pmars`.

That finding led to an immediate follow-up change on current `head`: benchmark mode
no longer exposes the `task` tool, reducing tool-surface distraction for
Terminal-Bench runs.

## Threats To Validity

- These are single runs per SHA. Run-to-run model variance may move individual tasks.
- The `after` run became invalid at task 5 due to provider credit exhaustion.
- The `before_sha` did not implement the `benchmark` preset, so `--preset benchmark`
  on that binary used the default preset instead.
- The report JSONs record `agent_git_sha` and `subset_version` at the top level rather than inside the nested `config` object. That provenance is present in the artifact, but consumers need to read the right fields.
- The comparison uses one fixed provider/model route. Results should not be generalized across providers or model versions without rerunning.
- `shell_anti_pattern_rate` and `structured_edit_success_rate` are secondary metrics. In this experiment they did not predict end-task success.

## Conclusion

This artifact should be treated as a failed first attempt at an evidence-grade
comparison, not as the final verdict on the ForgeCode-inspired bundle. The next
useful steps are:

1. Rerun with enough provider credits to keep the full subset valid.
2. Compare only binaries that both support the same benchmark preset semantics.
3. Continue with ablations rather than a bundled before/after claim.

The first ablation already landed: benchmark mode now excludes the `task` tool,
because the valid `build-pmars` trace showed it consuming turns without helping
task completion.
