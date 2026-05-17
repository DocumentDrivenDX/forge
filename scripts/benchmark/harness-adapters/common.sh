#!/usr/bin/env bash

# benchmark_adapter_read_profile reads profile JSON from stdin, validates the
# documented schema, and prints a compact JSON copy on stdout.
benchmark_adapter_read_profile() {
  local adapter="$1"
  local profile_json validated err_file

  profile_json="$(cat)"
  if [[ -z "${profile_json}" ]]; then
    printf '%s: empty profile JSON on stdin\n' "${adapter}" >&2
    return 2
  fi

  err_file="$(mktemp)"
  if ! validated="$(jq -c -e '
    def req(cond; msg): if cond then . else error(msg) end;
    req(type == "object"; "profile JSON must be a JSON object")
    | req(has("id") and (.id | type == "string" and length > 0); "missing required field .id")
    | req(has("provider") and (.provider | type == "object"); "missing required field .provider")
    | req(.provider | has("type") and (.type | type == "string" and length > 0); "missing required field .provider.type")
    | req(.provider | has("model") and (.model | type == "string" and length > 0); "missing required field .provider.model")
    | req(.provider | has("base_url") and (.base_url | type == "string" and length > 0); "missing required field .provider.base_url")
    | req(.provider | has("api_key_env") and (.api_key_env | type == "string" and length > 0); "missing required field .provider.api_key_env")
    | req(has("sampling") and (.sampling | type == "object"); "missing required field .sampling")
    | req(has("limits") and (.limits | type == "object"); "missing required field .limits")
  ' <<<"${profile_json}" 2>"${err_file}")"; then
    printf '%s: %s\n' "${adapter}" "$(tr -d '\n' <"${err_file}")" >&2
    rm -f "${err_file}"
    return 2
  fi

  rm -f "${err_file}"
  printf '%s\n' "${validated}"
}
