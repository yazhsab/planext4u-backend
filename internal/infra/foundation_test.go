package infra_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBEInfra001SecurityAndIsolationControls(t *testing.T) {
	t.Parallel()
	assertMarkers(t, "../../infra/bootstrap/main.tf", []string{"enable_key_rotation", "prevent_destroy", "aws_s3_bucket_versioning", "DenyInsecureTransport"})
	assertMarkers(t, "../../infra/modules/network/main.tf", []string{"aws_flow_log", "map_public_ip_on_launch = false", "private_dns_enabled", "single_nat_gateway"})
	assertMarkers(t, "../../infra/modules/data/main.tf", []string{"manage_master_user_password", "storage_encrypted", "deletion_protection", "aws_secretsmanager_secret", "value provisioned outside Terraform state"})
	assertMarkers(t, "../../infra/modules/artifacts/main.tf", []string{"image_tag_mutability = \"IMMUTABLE\"", "scan_on_push = true", "encryption_type = \"KMS\""})
	assertMarkers(t, "../../infra/modules/compute/main.tf", []string{"assign_public_ip = false", "readonlyRootFilesystem", "deployment_circuit_breaker", "aws_wafv2_web_acl", "enable_execute_command"})
}

func TestBEInfra001DeploymentEvidenceAndAutomaticRollback(t *testing.T) {
	for _, test := range []struct {
		name          string
		failFirstWait bool
		wantSuccess   bool
		wantReason    string
	}{
		{name: "stable deployment", wantSuccess: true},
		{name: "failed deployment rolls back", failFirstWait: true, wantSuccess: false, wantReason: "ecs_stabilization_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			temporary := t.TempDir()
			fakeAWS := filepath.Join(temporary, "aws")
			state := filepath.Join(temporary, "state")
			if err := os.WriteFile(fakeAWS, []byte(`#!/usr/bin/env bash
set -e
operation="$1:$2"
case "$operation" in
  ecs:describe-services) printf '%s\n' 'arn:aws:ecs:ap-south-1:111111111111:task-definition/planext4u-staging-platform:7' ;;
  ecs:describe-task-definition) printf '%s\n' '{"family":"planext4u-staging-platform","taskRoleArn":"arn:aws:iam::111111111111:role/task","executionRoleArn":"arn:aws:iam::111111111111:role/execution","networkMode":"awsvpc","containerDefinitions":[{"name":"platform","image":"old@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"requiresCompatibilities":["FARGATE"],"cpu":"512","memory":"1024","taskDefinitionArn":"old","revision":7,"status":"ACTIVE"}' ;;
  ecs:register-task-definition) printf '%s\n' 'arn:aws:ecs:ap-south-1:111111111111:task-definition/planext4u-staging-platform:8' ;;
  ecs:update-service) printf '%s\n' '{}' ;;
  ecs:wait)
    count=0
    if [[ -f "$FAKE_AWS_STATE" ]]; then count="$(cat "$FAKE_AWS_STATE")"; fi
    count=$((count + 1))
    printf '%s' "$count" > "$FAKE_AWS_STATE"
    if [[ "${FAIL_FIRST_WAIT:-false}" == true && "$count" == 1 ]]; then exit 1; fi
    ;;
  *) printf 'unexpected fake aws operation %s\n' "$operation" >&2; exit 1 ;;
esac
`), 0o755); err != nil {
				t.Fatal(err)
			}
			evidence := filepath.Join(temporary, "evidence.json")
			command := exec.Command("bash", "../../scripts/deploy-ecs.sh")
			command.Env = append(os.Environ(),
				"PATH="+temporary+":"+os.Getenv("PATH"), "AWS_REGION=ap-south-1", "ECS_CLUSTER=planext4u-staging", "ECS_SERVICE=platform", "CONTAINER_NAME=platform",
				"IMAGE_URI=111111111111.dkr.ecr.ap-south-1.amazonaws.com/planext4u/platform@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				"DEPLOYMENT_EVIDENCE="+evidence, "DEPLOYMENT_ENVIRONMENT=staging", "FAKE_AWS_STATE="+state,
				"FAIL_FIRST_WAIT="+map[bool]string{true: "true", false: "false"}[test.failFirstWait])
			err := command.Run()
			if test.wantSuccess && err != nil {
				t.Fatalf("deployment: %v", err)
			}
			if !test.wantSuccess && err == nil {
				t.Fatal("failed deployment returned success")
			}
			var record struct {
				Deployed       bool   `json:"deployed"`
				RollbackReason string `json:"rollback_reason"`
				Image          string `json:"image"`
			}
			contents, readErr := os.ReadFile(evidence)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if json.Unmarshal(contents, &record) != nil || record.Deployed != test.wantSuccess || record.RollbackReason != test.wantReason || !strings.Contains(record.Image, "@sha256:") {
				t.Fatalf("deployment evidence = %s", contents)
			}
		})
	}
}

func TestBEInfra001OIDCUsesRepositoryAndEnvironmentBoundTrust(t *testing.T) {
	t.Parallel()
	contents := readFile(t, "../../infra/modules/delivery_identity/main.tf")
	for _, marker := range []string{"sts:AssumeRoleWithWebIdentity", "token.actions.githubusercontent.com:aud", "repo:${var.github_repository}:ref:refs/heads/main", "repo:${var.github_repository}:environment:${var.environment}", "iam:PassRole"} {
		if !strings.Contains(contents, marker) {
			t.Errorf("delivery identity is missing %q", marker)
		}
	}
	for _, forbidden := range []string{"AWS_ACCESS_KEY_ID", "secret_access_key", "AdministratorAccess"} {
		if strings.Contains(contents, forbidden) {
			t.Errorf("delivery identity contains forbidden long-lived or broad credential marker %q", forbidden)
		}
	}
}

func TestBEInfra001DeploymentRequiresDigestAndRollbackEvidence(t *testing.T) {
	t.Parallel()
	contents := readFile(t, "../../scripts/deploy-ecs.sh")
	for _, marker := range []string{"@sha256:", "previous_task_definition", "services-stable", "rollback_reason", "DEPLOYMENT_EVIDENCE"} {
		if !strings.Contains(contents, marker) {
			t.Errorf("deployment script is missing %q", marker)
		}
	}
}

func assertMarkers(t *testing.T, path string, markers []string) {
	t.Helper()
	contents := readFile(t, path)
	for _, marker := range markers {
		if !strings.Contains(contents, marker) {
			t.Errorf("%s is missing %q", path, marker)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
