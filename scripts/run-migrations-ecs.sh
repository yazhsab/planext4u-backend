#!/usr/bin/env bash
set -euo pipefail

required=(AWS_REGION ECS_CLUSTER MIGRATION_TASK_DEFINITION MIGRATION_CONTAINER_NAME IMAGE_URI ECS_SUBNETS ECS_SECURITY_GROUPS MIGRATION_EVIDENCE)
for name in "${required[@]}"; do
  if [[ -z "${!name:-}" ]]; then
    echo "required migration deployment setting is missing: ${name}" >&2
    exit 2
  fi
done

for command_name in aws jq; do
  command -v "$command_name" >/dev/null || { echo "required command is unavailable: ${command_name}" >&2; exit 2; }
done

if [[ "$IMAGE_URI" != *@sha256:* ]]; then
  echo "migration image must be pinned by digest" >&2
  exit 2
fi

definition="$(aws ecs describe-task-definition --region "$AWS_REGION" --task-definition "$MIGRATION_TASK_DEFINITION" --query taskDefinition --output json)"
registration="$(jq --arg image "$IMAGE_URI" '
  .containerDefinitions |= map(if .name == "migration" then .image = $image else . end) |
  del(.taskDefinitionArn,.revision,.status,.requiresAttributes,.compatibilities,.registeredAt,.registeredBy)
' <<<"$definition")"
if [[ "$(jq '[.containerDefinitions[] | select(.name == "migration" and .image == $image)] | length' --arg image "$IMAGE_URI" <<<"$registration")" != "1" ]]; then
  echo "migration container was not found in the task definition" >&2
  exit 2
fi

registered_arn="$(aws ecs register-task-definition --region "$AWS_REGION" --cli-input-json "$registration" --query taskDefinition.taskDefinitionArn --output text)"
network="awsvpcConfiguration={subnets=[${ECS_SUBNETS}],securityGroups=[${ECS_SECURITY_GROUPS}],assignPublicIp=DISABLED}"
task_arn="$(aws ecs run-task --region "$AWS_REGION" --cluster "$ECS_CLUSTER" --task-definition "$registered_arn" --launch-type FARGATE --network-configuration "$network" --count 1 --query 'tasks[0].taskArn' --output text)"
if [[ -z "$task_arn" || "$task_arn" == "None" ]]; then
  echo "migration task did not start" >&2
  exit 1
fi

stop_on_exit() {
  if [[ "${migration_complete:-false}" != "true" ]]; then
    aws ecs stop-task --region "$AWS_REGION" --cluster "$ECS_CLUSTER" --task "$task_arn" --reason "delivery job interrupted" >/dev/null 2>&1 || true
  fi
}
trap stop_on_exit EXIT INT TERM

aws ecs wait tasks-stopped --region "$AWS_REGION" --cluster "$ECS_CLUSTER" --tasks "$task_arn"
result="$(aws ecs describe-tasks --region "$AWS_REGION" --cluster "$ECS_CLUSTER" --tasks "$task_arn" --query 'tasks[0]' --output json)"
exit_code="$(jq -r --arg name "$MIGRATION_CONTAINER_NAME" '.containers[] | select(.name == $name) | .exitCode // -1' <<<"$result")"
stopped_reason="$(jq -r '.stoppedReason // "unavailable"' <<<"$result")"
jq -n \
  --arg task_arn "$task_arn" \
  --arg task_definition "$registered_arn" \
  --arg image "$IMAGE_URI" \
  --arg stopped_reason "$stopped_reason" \
  --argjson exit_code "$exit_code" \
  --arg completed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{task_arn:$task_arn,task_definition:$task_definition,image:$image,exit_code:$exit_code,stopped_reason:$stopped_reason,completed_at:$completed_at}' >"$MIGRATION_EVIDENCE"

migration_complete=true
if [[ "$exit_code" != "0" ]]; then
  echo "migration task failed: exit_code=${exit_code} reason=${stopped_reason}" >&2
  exit 1
fi
echo "database migrations completed with task ${task_arn}"
