#!/usr/bin/env bash
set -euo pipefail

script_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
required=(AWS_REGION ECS_CLUSTER ECR_REGISTRY SOURCE_REVISION SERVICES_JSON DEPLOYMENT_ENVIRONMENT RELEASE_EVIDENCE)
for variable_name in "${required[@]}"; do
  if [[ -z "${!variable_name:-}" ]]; then
    printf '%s is required\n' "$variable_name" >&2
    exit 2
  fi
done

if [[ ! "$ECR_REGISTRY" =~ ^[0-9]{12}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com/[a-z0-9][a-z0-9._/-]*$ ]] ||
   [[ ! "$SOURCE_REVISION" =~ ^[a-f0-9]{40}$ ]] ||
   [[ "$DEPLOYMENT_ENVIRONMENT" != "staging" && "$DEPLOYMENT_ENVIRONMENT" != "production" ]]; then
  printf 'release registry, revision or environment is invalid\n' >&2
  exit 2
fi
if ! jq -e 'type == "array" and length > 0 and length <= 20 and all(type == "string" and test("^[A-Za-z0-9_-]{1,255}$")) and (unique | length) == length' <<<"$SERVICES_JSON" >/dev/null; then
  printf 'SERVICES_JSON must be a unique array of safe ECS service names\n' >&2
  exit 2
fi
if [[ -n "${SMOKE_URL:-}" && ! "$SMOKE_URL" =~ ^https://[A-Za-z0-9.-]+(:[0-9]+)?(/.*)?$ ]]; then
  printf 'SMOKE_URL must use HTTPS for a protected deployment\n' >&2
  exit 2
fi
if [[ "$DEPLOYMENT_ENVIRONMENT" == "staging" ]]; then
  for variable_name in VERTICAL_SLICE_SMOKE_ORIGIN STAGING_SMOKE_PROVIDER STAGING_SMOKE_PROVIDER_TOKEN; do
    if [[ -z "${!variable_name:-}" ]]; then
      printf '%s is required for a staging release\n' "$variable_name" >&2
      exit 2
    fi
  done
fi

release_tmp="$(mktemp -d)"
trap 'rm -rf "$release_tmp"' EXIT
evidence_directory="$release_tmp/services"
mkdir -p "$evidence_directory"
rollback_results="$release_tmp/rollback.json"
printf '[]\n' >"$rollback_results"
services=()
while IFS= read -r service; do
  services+=("$service")
done < <(jq -r '.[]' <<<"$SERVICES_JSON")
deployed_services=()

append_rollback_result() {
  local service="$1" status="$2"
  local candidate="$release_tmp/rollback-next.json"
  jq --arg service "$service" --arg status "$status" '. + [{service:$service,status:$status}]' "$rollback_results" >"$candidate"
  mv "$candidate" "$rollback_results"
}

rollback_release() {
  local index service evidence previous status
  for ((index=${#deployed_services[@]}-1; index>=0; index--)); do
    service="${deployed_services[$index]}"
    evidence="$evidence_directory/$service.json"
    previous="$(jq -er '.previous_task_definition' "$evidence")"
    status=failed
    if aws ecs update-service --region "$AWS_REGION" --cluster "$ECS_CLUSTER" --service "$service" --task-definition "$previous" >/dev/null &&
       aws ecs wait services-stable --region "$AWS_REGION" --cluster "$ECS_CLUSTER" --services "$service"; then
      status=passed
    fi
    append_rollback_result "$service" "$status"
  done
}

write_release_evidence() {
  local deployed="$1" reason="$2" final_smoke="$3"
  local service_records="$release_tmp/service-records.json"
  if compgen -G "$evidence_directory/*.json" >/dev/null; then
    jq -s '.' "$evidence_directory"/*.json >"$service_records"
  else
    printf '[]\n' >"$service_records"
  fi
  jq -n \
    --arg environment "$DEPLOYMENT_ENVIRONMENT" \
    --arg source_revision "$SOURCE_REVISION" \
    --arg failure_reason "$reason" \
    --arg final_smoke "$final_smoke" \
    --argjson deployed "$deployed" \
    --slurpfile services "$service_records" \
    --slurpfile rollback "$rollback_results" \
    '{schema_version:1,environment:$environment,source_revision:$source_revision,deployed:$deployed,failure_reason:$failure_reason,final_smoke:$final_smoke,services:$services[0],rollback:$rollback[0]}' \
    >"$RELEASE_EVIDENCE"
}

fail_release() {
  local reason="$1" smoke="${2:-not_run}"
  rollback_release
  write_release_evidence false "$reason" "$smoke"
  printf 'release failed and the deployed set was rolled back: %s\n' "$reason" >&2
  exit 1
}

repository_prefix="${ECR_REGISTRY#*/}"
for service in "${services[@]}"; do
  repository="$repository_prefix/$service"
  if ! digest="$(aws ecr describe-images --region "$AWS_REGION" --repository-name "$repository" --image-ids imageTag="$SOURCE_REVISION" --query 'imageDetails[0].imageDigest' --output text)" ||
     [[ ! "$digest" =~ ^sha256:[a-f0-9]{64}$ ]]; then
    fail_release "image_digest_unavailable:$service"
  fi
  evidence="$evidence_directory/$service.json"
  if ! AWS_REGION="$AWS_REGION" ECS_CLUSTER="$ECS_CLUSTER" ECS_SERVICE="$service" CONTAINER_NAME="$service" \
    IMAGE_URI="$ECR_REGISTRY/$service@$digest" DEPLOYMENT_ENVIRONMENT="$DEPLOYMENT_ENVIRONMENT" \
    SMOKE_URL= VERTICAL_SLICE_SMOKE_ORIGIN= DEPLOYMENT_EVIDENCE="$evidence" "$script_directory/deploy-ecs.sh"; then
    fail_release "service_deployment_failed:$service"
  fi
  deployed_services+=("$service")
done

if [[ -n "${SMOKE_URL:-}" ]] && ! curl --fail --silent --show-error --retry 6 --retry-all-errors --retry-delay 5 --max-time 15 "$SMOKE_URL" >/dev/null; then
  fail_release "release_readiness_failed" failed
fi
if [[ "$DEPLOYMENT_ENVIRONMENT" == "staging" ]] && ! "$script_directory/staging-vertical-slice-smoke.sh" "$VERTICAL_SLICE_SMOKE_ORIGIN"; then
  fail_release "vertical_slice_smoke_failed" failed
fi

write_release_evidence true "" passed
printf 'release deployed and final smoke passed: %s\n' "$(IFS=,; printf '%s' "${services[*]}")"
