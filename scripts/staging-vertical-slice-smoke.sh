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

smoke_tmp="$(mktemp -d)"
trap 'rm -rf "$smoke_tmp"' EXIT
traceparent='00-11111111111111111111111111111111-2222222222222222-01'
access_token=''

request_step() {
  local step="$1" method="$2" path="$3" assertion="$4" body="${5:-}" token="${6:-}"
  local correlation="corr-staging-${step}-001"
  local headers="$smoke_tmp/${step}.headers" response="$smoke_tmp/${step}.json"
  local args=(--silent --show-error --retry 2 --retry-all-errors --retry-delay 1 --connect-timeout 5 --max-time 15 --dump-header "$headers" --output "$response" --write-out '%{http_code}' --request "$method" --header "Accept: application/json" --header "X-Correlation-ID: $correlation" --header "traceparent: $traceparent")
  if [[ -n "$body" ]]; then
    args+=(--header 'Content-Type: application/json' --data "$body")
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
  local returned_correlation
  returned_correlation="$(awk 'tolower($1) == "x-correlation-id:" { value=$2; gsub("\\r", "", value); print value }' "$headers" | tail -n 1)"
  if [[ "$returned_correlation" != "$correlation" ]]; then
    printf 'staging smoke correlation mismatch at %s\n' "$step" >&2
    return 1
  fi
  if ! jq -e "$assertion" "$response" >/dev/null; then
    printf 'staging smoke contract assertion failed at %s\n' "$step" >&2
    return 1
  fi
}

request_step ready GET /health/ready '.status == "ok"'
request_step login POST /v1/auth/exchange '.identity_id == "customer-synthetic-001" and (.tokens.access_token | startswith("p4us_v1."))' \
  '{"provider":"local","provider_token":"synthetic-customer","device_id":"device-staging-smoke-001","country":"IN"}'
access_token="$(jq -er '.tokens.access_token' "$smoke_tmp/login.json")"
request_step location POST /v1/serviceability/check '.serviceable == true and .locality == "Chennai"' \
  "{\"latitude\":13.08,\"longitude\":80.27,\"accuracy_metres\":12,\"captured_at\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"purpose\":\"LOCATION_SERVICEABILITY\"}" "$access_token"
request_step bootstrap GET '/v1/bootstrap?platform=android&app_version=0.1.0&locale=en' '.flags.customer_home == true and .flags.catalog_read == true' '' "$access_token"
request_step home GET /v1/home '(.categories | any(.name == "Daily needs")) and (.featured_items | any(.name == "Fresh milk"))' '' "$access_token"
request_step catalog GET '/v1/catalog/items?category_id=daily-needs' '(.items | length) >= 2 and (.items | all(.price.currency == "INR"))' '' "$access_token"

access_token=''
printf 'staging vertical slice passed: login -> location -> home -> catalog\n'
