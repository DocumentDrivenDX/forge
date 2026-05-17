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
require_tool diff
require_tool find
require_tool sort
require_tool cut
require_tool mktemp
require_tool paste

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

baseline_map="${tmpdir}/baseline.map"
candidate_map="${tmpdir}/candidate.map"
baseline_paths="${tmpdir}/baseline.paths"
candidate_paths="${tmpdir}/candidate.paths"

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

canonicalize_report() {
  local report_path="$1"
  jq -S '
    def dataset_id:
      (.dataset | tostring
       | sub("^.*?/"; "")
       | sub("^.*@"; ""));

    def rep_num:
      if (.rep | type) == "string" then
        ((.rep | capture("^(?<n>[0-9]+)") | .n) | tonumber)
      else
        .rep
      end;

    def reward_val:
      if has("reward") then
        .reward
      elif (.result? != null and .result.reward? != null) then
        .result.reward
      else
        null
      end;

    def cost_val:
      if has("cost_usd") then
        .cost_usd
      elif has("cost_usd_at_run_time") then
        .cost_usd_at_run_time
      else
        null
      end;

    {
      task_id,
      framework,
      dataset: dataset_id,
      rep: rep_num,
      final_status,
      invalid_class,
      reward: reward_val,
      cost: cost_val,
      profile,
    }
  ' "${report_path}"
}

collect_reports "${baseline_root}" "${baseline_map}"
collect_reports "${candidate_root}" "${candidate_map}"
cut -f1 "${baseline_map}" >"${baseline_paths}"
cut -f1 "${candidate_map}" >"${candidate_paths}"

if [[ ! -s "${baseline_paths}" ]]; then
  die "no report.json files found under ${baseline_root}"
fi
if [[ ! -s "${candidate_paths}" ]]; then
  die "no report.json files found under ${candidate_root}"
fi

if ! diff -u "${baseline_paths}" "${candidate_paths}" >&2; then
  die "report sets differ between ${baseline_root} and ${candidate_root}"
fi

index=0
while IFS=$'\t' read -r left_rel left_path right_rel right_path; do
  [[ "${left_rel}" == "${right_rel}" ]] || die "internal path mismatch: ${left_rel} != ${right_rel}"
  index=$((index + 1))

  left_norm="${tmpdir}/left-${index}.json"
  right_norm="${tmpdir}/right-${index}.json"
  canonicalize_report "${left_path}" >"${left_norm}"
  canonicalize_report "${right_path}" >"${right_norm}"

  if ! diff -u "${left_norm}" "${right_norm}" >&2; then
    die "canonical report mismatch at ${left_rel}"
  fi
done < <(paste "${baseline_map}" "${candidate_map}")
