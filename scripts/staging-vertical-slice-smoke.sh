#!/usr/bin/env bash
set -euo pipefail

origin="${1:-${VERTICAL_SLICE_SMOKE_ORIGIN:-}}"
origin="${origin%/}"
if [[ -z "$origin" ]]; then
  printf 'a staging origin is required\n' >&2
  exit 2
fi
if [[ ! "$origin" =~ ^https://[A-Za-z0-9.-]+(:[0-9]+)?$ ]] && [[ "${ALLOW_INSECURE_LOCAL_SMOKE:-false}" != "true" || ! "$origin" =~ ^http://(127\.0\.0\.1|localhost)(:[0-9]+)?$ ]]; then
  printf 'the staging smoke origin must use HTTPS; insecure HTTP is limited to localhost tests\n' >&2
  exit 2
fi
if [[ "${STAGING_SMOKE_PROVIDER:-}" != "firebase" && "${STAGING_SMOKE_PROVIDER:-}" != "oidc" ]]; then
  printf 'STAGING_SMOKE_PROVIDER must be firebase or oidc\n' >&2
  exit 2
fi
if [[ -z "${STAGING_SMOKE_PROVIDER_TOKEN:-}" || ${#STAGING_SMOKE_PROVIDER_TOKEN} -gt 16384 ]]; then
  printf 'STAGING_SMOKE_PROVIDER_TOKEN is required and must not exceed 16 KiB\n' >&2
  exit 2
fi

smoke_tmp="$(mktemp -d)"
trap 'rm -rf "$smoke_tmp"' EXIT
umask 077
traceparent='00-11111111111111111111111111111111-2222222222222222-01'
access_token=''

request_step() {
  local step="$1" method="$2" path="$3" assertion="$4" body_file="${5:-}" token="${6:-}"
  local correlation="corr-staging-${step}-001"
  local headers="$smoke_tmp/${step}.headers" response="$smoke_tmp/${step}.json"
  local args=(--silent --show-error --retry 2 --retry-all-errors --retry-delay 1 --connect-timeout 5 --max-time 15 --dump-header "$headers" --output "$response" --write-out '%{http_code}' --request "$method" --header "Accept: application/json" --header "X-Correlation-ID: $correlation" --header "traceparent: $traceparent")
  if [[ -n "$body_file" ]]; then
    args+=(--header 'Content-Type: application/json' --data-binary "@$body_file")
  fi
  if [[ -n "$token" ]]; then
    args+=(--header "Authorization: Bearer $token")
  fi
  local status
  if ! status="$(curl "${args[@]}" "$origin$path")"; then
    printf 'staging smoke request failed at %s\n' "$step" >&2
    return 1
  fi
  if [[ "$status" != "200" && ! ( "$step" == "login" && "$status" == "201" ) ]]; then
    printf 'staging smoke returned HTTP %s at %s\n' "$status" "$step" >&2
    return 1
  fi
  if [[ "$path" == "/healthz" || "$path" == "/readyz" ]]; then
    local request_id
    request_id="$(awk 'tolower($1) == "x-request-id:" { value=$2; gsub("\\r", "", value); print value }' "$headers" | tail -n 1)"
    if [[ -z "$request_id" ]]; then
      printf 'staging smoke request ID missing at %s\n' "$step" >&2
      return 1
    fi
  else
    local returned_correlation
    returned_correlation="$(awk 'tolower($1) == "x-correlation-id:" { value=$2; gsub("\\r", "", value); print value }' "$headers" | tail -n 1)"
    if [[ "$returned_correlation" != "$correlation" ]]; then
      printf 'staging smoke correlation mismatch at %s\n' "$step" >&2
      return 1
    fi
  fi
  if ! jq -e "$assertion" "$response" >/dev/null; then
    printf 'staging smoke contract assertion failed at %s\n' "$step" >&2
    return 1
  fi
}

jq -n '{provider:env.STAGING_SMOKE_PROVIDER,provider_token:env.STAGING_SMOKE_PROVIDER_TOKEN,device_id:"device-staging-smoke-001",country:(env.STAGING_SMOKE_COUNTRY // "IN")}' >"$smoke_tmp/login-request.json"
jq -n '{latitude:((env.STAGING_SMOKE_LATITUDE // "13.08") | tonumber),longitude:((env.STAGING_SMOKE_LONGITUDE // "80.27") | tonumber),accuracy_metres:12,captured_at:(now | todate),purpose:"LOCATION_SERVICEABILITY"}' >"$smoke_tmp/location-request.json"

request_step ready GET /readyz '.status == "ready" and (.service | type == "string" and length > 0)'
request_step login POST /v1/auth/exchange '(.identity_id | type == "string" and length > 0) and (.tokens.access_token | type == "string" and length > 20)' "$smoke_tmp/login-request.json"
access_token="$(jq -er '.tokens.access_token' "$smoke_tmp/login.json")"
request_step location POST /v1/serviceability/check '(.serviceable | type == "boolean") and (.reason_code | type == "string")' "$smoke_tmp/location-request.json" "$access_token"
request_step bootstrap GET '/v1/bootstrap?platform=android&app_version=0.1.0&locale=en' '(.revision | type == "number") and (.flags | type == "object") and (.home_sections | type == "array")' '' "$access_token"
request_step home GET /v1/home '(.categories | type == "array") and (.featured_items | type == "array") and (.projection_status | type == "string")' '' "$access_token"
request_step catalog GET '/v1/catalog/items?limit=1' '(.items | type == "array") and (.has_more | type == "boolean") and (.projection_status | type == "string")' '' "$access_token"

access_token=''
printf 'staging distributed vertical slice passed: provider login -> location -> CMS bootstrap -> home -> catalog\n'
