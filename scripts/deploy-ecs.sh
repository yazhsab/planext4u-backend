#!/usr/bin/env bash
set -euo pipefail
script_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

required=(AWS_REGION ECS_CLUSTER ECS_SERVICE CONTAINER_NAME IMAGE_URI DEPLOYMENT_EVIDENCE)
for variable_name in "${required[@]}"; do
  if [[ -z "${!variable_name:-}" ]]; then
    printf '%s is required\n' "$variable_name" >&2
    exit 2
  fi
done

if [[ ! "$ECS_CLUSTER" =~ ^[A-Za-z0-9_-]{1,255}$ ]] || [[ ! "$ECS_SERVICE" =~ ^[A-Za-z0-9_-]{1,255}$ ]] || [[ ! "$CONTAINER_NAME" =~ ^[A-Za-z0-9_-]{1,255}$ ]]; then
  printf 'cluster, service and container identifiers must be explicit safe names\n' >&2
  exit 2
fi
if [[ ! "$IMAGE_URI" =~ @sha256:[a-f0-9]{64}$ ]]; then
  printf 'IMAGE_URI must use an immutable sha256 digest\n' >&2
  exit 2
fi
if [[ "${DEPLOYMENT_ENVIRONMENT:-unknown}" == "staging" && -z "${VERTICAL_SLICE_SMOKE_ORIGIN:-}" ]]; then
  printf 'VERTICAL_SLICE_SMOKE_ORIGIN is required for staging deployments\n' >&2
  exit 2
fi

deployment_tmp="$(mktemp -d)"
trap 'rm -rf "$deployment_tmp"' EXIT

previous_task_definition="$(aws ecs describe-services --region "$AWS_REGION" --cluster "$ECS_CLUSTER" --services "$ECS_SERVICE" --query 'services[0].taskDefinition' --output text)"
if [[ -z "$previous_task_definition" || "$previous_task_definition" == "None" ]]; then
  printf 'unable to resolve the current task definition\n' >&2
  exit 1
fi

aws ecs describe-task-definition --region "$AWS_REGION" --task-definition "$previous_task_definition" --query taskDefinition --output json > "$deployment_tmp/current.json"
jq --arg container "$CONTAINER_NAME" --arg image "$IMAGE_URI" '
  .containerDefinitions |= map(if .name == $container then .image = $image else . end)
  | del(.taskDefinitionArn, .revision, .status, .requiresAttributes, .compatibilities, .registeredAt, .registeredBy, .deregisteredAt)
' "$deployment_tmp/current.json" > "$deployment_tmp/candidate.json"

if ! jq -e --arg container "$CONTAINER_NAME" '[.containerDefinitions[] | select(.name == $container)] | length == 1' "$deployment_tmp/candidate.json" >/dev/null; then
  printf 'task definition does not contain exactly one requested container\n' >&2
  exit 1
fi

new_task_definition="$(aws ecs register-task-definition --region "$AWS_REGION" --cli-input-json "file://$deployment_tmp/candidate.json" --query 'taskDefinition.taskDefinitionArn' --output text)"
deployed=false
rollback_reason=""
vertical_slice_smoke="not_requested"

if aws ecs update-service --region "$AWS_REGION" --cluster "$ECS_CLUSTER" --service "$ECS_SERVICE" --task-definition "$new_task_definition" >/dev/null && \
   aws ecs wait services-stable --region "$AWS_REGION" --cluster "$ECS_CLUSTER" --services "$ECS_SERVICE"; then
  if [[ -n "${SMOKE_URL:-}" ]]; then
    if curl --fail --silent --show-error --retry 6 --retry-all-errors --retry-delay 5 --max-time 15 "$SMOKE_URL" >/dev/null; then
      deployed=true
    else
      rollback_reason="smoke_check_failed"
    fi
  else
    deployed=true
  fi
  if [[ "$deployed" == true && -n "${VERTICAL_SLICE_SMOKE_ORIGIN:-}" ]]; then
    if "$script_directory/staging-vertical-slice-smoke.sh" "$VERTICAL_SLICE_SMOKE_ORIGIN"; then
      vertical_slice_smoke="passed"
    else
      deployed=false
      rollback_reason="vertical_slice_smoke_failed"
      vertical_slice_smoke="failed"
    fi
  fi
else
  rollback_reason="ecs_stabilization_failed"
fi

if [[ "$deployed" != true ]]; then
  aws ecs update-service --region "$AWS_REGION" --cluster "$ECS_CLUSTER" --service "$ECS_SERVICE" --task-definition "$previous_task_definition" >/dev/null
  aws ecs wait services-stable --region "$AWS_REGION" --cluster "$ECS_CLUSTER" --services "$ECS_SERVICE"
fi

jq -n \
  --arg environment "${DEPLOYMENT_ENVIRONMENT:-unknown}" \
  --arg cluster "$ECS_CLUSTER" \
  --arg service "$ECS_SERVICE" \
  --arg image "$IMAGE_URI" \
  --arg previous "$previous_task_definition" \
  --arg candidate "$new_task_definition" \
  --arg commit "${GITHUB_SHA:-unknown}" \
  --arg run_id "${GITHUB_RUN_ID:-local}" \
  --arg rollback_reason "$rollback_reason" \
  --arg vertical_slice_smoke "$vertical_slice_smoke" \
  --argjson deployed "$deployed" \
  '{schema_version:1, environment:$environment, cluster:$cluster, service:$service, image:$image, previous_task_definition:$previous, candidate_task_definition:$candidate, source_commit:$commit, workflow_run_id:$run_id, deployed:$deployed, rollback_reason:$rollback_reason, vertical_slice_smoke:$vertical_slice_smoke}' \
  > "$DEPLOYMENT_EVIDENCE"

if [[ "$deployed" != true ]]; then
  printf 'deployment failed and was rolled back: %s\n' "$rollback_reason" >&2
  exit 1
fi
