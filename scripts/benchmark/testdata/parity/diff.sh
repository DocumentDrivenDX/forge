#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
baseline_root="${1:-${script_dir}/go-runner}"
candidate_root="${2:-${script_dir}/bash-runner}"

die() {
  printf 'diff.sh: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat >&2 <<'EOF'
Usage:
  diff.sh [baseline_root [candidate_root]]

Defaults:
  baseline_root   scripts/benchmark/testdata/parity/go-runner
  candidate_root  scripts/benchmark/testdata/parity/bash-runner

Compares cell reports between baseline and candidate, filtering out allowlisted paths.
Exits 0 if no non-allowlisted divergence found, non-zero otherwise.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi
if [[ $# -gt 2 ]]; then
  usage
  exit 2
fi

for root in "${baseline_root}" "${candidate_root}"; do
  [[ -d "${root}" ]] || die "missing directory: ${root}"
done

require_tool() {
  command -v "$1" >/dev/null 2>&1 || die "required tool not found: $1"
}

require_tool jq
require_tool find
require_tool sort

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

baseline_map="${tmpdir}/baseline.map"
candidate_map="${tmpdir}/candidate.map"

# Collect reports: relative_path -> full_path
collect_reports() {
  local root="$1"
  local out="$2"
  : >"${out}"

  while IFS= read -r report_path; do
    [[ -n "${report_path}" ]] || continue
    local rel_path="${report_path#"${root}"/}"
    printf '%s\t%s\n' "${rel_path}" "${report_path}" >>"${out}"
  done < <(find "${root}" -type f -name report.json | sort)
}

# Check if a path is allowlisted
is_allowlisted() {
  local path="$1"

  # Exact matches - allowlisted paths
  case "${path}" in
    # Timing and identity (environment-dependent)
    "cell_id"|"started_at"|"finished_at"|"wall_seconds") return 0 ;;

    # Go-runner-specific fields
    "adapter_module"|"harbor_agent"|"profile_id"|"profile_path"|"profile_snapshot") return 0 ;;
    "fiz_tools_version"|"grading_outcome"|"process_outcome"|"exit_code"|"pricing_source") return 0 ;;
    "input_tokens"|"output_tokens"|"cached_input_tokens"|"retried_input_tokens") return 0 ;;
    "tool_calls"|"tool_call_errors"|"turns"|"category"|"difficulty") return 0 ;;
    "runtime_props"|"sampling_used"|"output_dir") return 0 ;;
    "cost_usd"|"dataset_version"|"harness"|"reward") return 0 ;;

    # Bash-runner-specific or new fields
    "artifacts"|"harbor_runner_image_digest"|"task_executor_version"|"env_redacted") return 0 ;;
    "model_server.commit"|"attempt_of"|"superseded_by"|"result"|"cost_usd_at_run_time") return 0 ;;

    # Fields that differ structurally between runners
    "command"|"dataset"|"profile"|"rep") return 0 ;;
  esac

  # Prefix matches
  case "${path}" in
    "artifacts."*|"env_redacted."*|"session."*|"result."*) return 0 ;;
  esac

  return 1
}

# Compare two JSON reports, reporting non-allowlisted divergences
compare_cell() {
  local baseline_path="$1"
  local candidate_path="$2"
  local cell_name="$3"

  local divergence_count=0
  local baseline_keys candidate_keys all_keys

  baseline_keys=$(jq 'keys' "${baseline_path}" | jq -r '.[]' | sort)
  candidate_keys=$(jq 'keys' "${candidate_path}" | jq -r '.[]' | sort)
  all_keys=$(printf '%s\n' "${baseline_keys}" "${candidate_keys}" | sort -u)

  while IFS= read -r key; do
    [[ -z "${key}" ]] && continue

    local baseline_val candidate_val
    baseline_val=$(jq ".${key} // \"MISSING\" | @json" "${baseline_path}")
    candidate_val=$(jq ".${key} // \"MISSING\" | @json" "${candidate_path}")

    if [[ "${baseline_val}" != "${candidate_val}" ]]; then
      if ! is_allowlisted "${key}"; then
        printf 'DIVERGENCE at %s: path=%s\n' "${cell_name}" "${key}" >&2
        printf '  baseline:  %s\n' "${baseline_val}" >&2
        printf '  candidate: %s\n' "${candidate_val}" >&2
        divergence_count=$((divergence_count + 1))
      fi
    fi
  done < <(echo "${all_keys}")

  return $((divergence_count == 0 ? 0 : 1))
}

collect_reports "${baseline_root}" "${baseline_map}"
collect_reports "${candidate_root}" "${candidate_map}"

if [[ ! -s "${baseline_map}" ]]; then
  die "no report.json files found under ${baseline_root}"
fi
if [[ ! -s "${candidate_map}" ]]; then
  die "no report.json files found under ${candidate_root}"
fi

# Check that cell sets match
baseline_cells=$(cut -f1 "${baseline_map}" | cut -d/ -f1 | sort -u)
candidate_cells=$(cut -f1 "${candidate_map}" | cut -d/ -f1 | sort -u)

if [[ "${baseline_cells}" != "${candidate_cells}" ]]; then
  printf 'diff.sh: cell sets differ\n' >&2
  printf 'baseline: %s\n' "${baseline_cells}" >&2
  printf 'candidate: %s\n' "${candidate_cells}" >&2
  exit 1
fi

overall_status=0
while IFS=$'\t' read -r rel_path baseline_path; do
  [[ -z "${rel_path}" ]] && continue
  cell_name=$(echo "${rel_path}" | cut -d/ -f1)

  # Find matching candidate
  candidate_path=$(grep "^${rel_path}" "${candidate_map}" | cut -f2)
  [[ -z "${candidate_path}" ]] && die "no matching candidate cell for ${rel_path}"

  if ! compare_cell "${baseline_path}" "${candidate_path}" "${cell_name}"; then
    overall_status=1
  fi
done < "${baseline_map}"

exit ${overall_status}
