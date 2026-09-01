package infra_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

func TestBEInfra001RejectsUnrestrictedSecurityGroupEgress(t *testing.T) {
	t.Parallel()
	unrestrictedEgress := regexp.MustCompile(`(?s)egress\s*\{[^}]*cidr_blocks\s*=\s*\["0\.0\.0\.0/0"\]`)
	for _, path := range []string{"../../infra/modules/compute/main.tf", "../../infra/modules/data/main.tf", "../../infra/modules/network/main.tf"} {
		contents := readFile(t, path)
		if unrestrictedEgress.MatchString(contents) {
			t.Errorf("%s contains unrestricted security-group egress", path)
		}
	}
	compute := readFile(t, "../../infra/modules/compute/main.tf")
	for _, marker := range []string{"Only private application target ports", "public providers require a reviewed egress proxy", "#trivy:ignore:AWS-0053", "ingress is 443-only and the WAF is attached"} {
		if !strings.Contains(compute, marker) {
			t.Errorf("compute boundary is missing %q", marker)
		}
	}
}

func TestBEInfra001DeploymentEvidenceAndAutomaticRollback(t *testing.T) {
	for _, test := range []struct {
		name          string
		failFirstWait bool
		failSmoke     bool
		wantSuccess   bool
		wantReason    string
	}{
		{name: "stable deployment", wantSuccess: true},
		{name: "failed deployment rolls back", failFirstWait: true, wantSuccess: false, wantReason: "ecs_stabilization_failed"},
		{name: "failed vertical slice rolls back", failSmoke: true, wantSuccess: false, wantReason: "vertical_slice_smoke_failed"},
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
			if err := os.WriteFile(filepath.Join(temporary, "curl"), []byte("#!/usr/bin/env bash\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			evidence := filepath.Join(temporary, "evidence.json")
			command := exec.Command("bash", "../../scripts/deploy-ecs.sh")
			command.Env = append(os.Environ(),
				"PATH="+temporary+":"+os.Getenv("PATH"), "AWS_REGION=ap-south-1", "ECS_CLUSTER=planext4u-staging", "ECS_SERVICE=platform", "CONTAINER_NAME=platform",
				"IMAGE_URI=111111111111.dkr.ecr.ap-south-1.amazonaws.com/planext4u/platform@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				"DEPLOYMENT_EVIDENCE="+evidence, "DEPLOYMENT_ENVIRONMENT="+map[bool]string{true: "staging", false: "development"}[test.failSmoke], "FAKE_AWS_STATE="+state,
				"FAIL_FIRST_WAIT="+map[bool]string{true: "true", false: "false"}[test.failFirstWait],
				"VERTICAL_SLICE_SMOKE_ORIGIN="+map[bool]string{true: "https://staging.example.test", false: ""}[test.failSmoke])
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
	release := readFile(t, "../../scripts/deploy-release-ecs.sh")
	for _, marker := range []string{"STAGING_SMOKE_PROVIDER_TOKEN", "is required for a staging release", "rollback_release", "staging-vertical-slice-smoke.sh", "release_readiness_failed", "RELEASE_EVIDENCE"} {
		if !strings.Contains(release, marker) {
			t.Errorf("atomic release script is missing %q", marker)
		}
	}
}

func TestBEInfra001AtomicReleaseRollsBackEveryUpdatedService(t *testing.T) {
	temporary := t.TempDir()
	fakeAWS := filepath.Join(temporary, "aws")
	calls := filepath.Join(temporary, "calls")
	if err := os.WriteFile(fakeAWS, []byte(`#!/usr/bin/env bash
set -e
printf '%s\n' "$*" >>"$FAKE_AWS_CALLS"
operation="$1:$2"
service=""
definition=""
for ((index=1; index<=$#; index++)); do
  if [[ "${!index}" == "--service" || "${!index}" == "--services" ]]; then
    next=$((index + 1)); service="${!next}"
  fi
  if [[ "${!index}" == "--task-definition" ]]; then
    next=$((index + 1)); definition="${!next}"
  fi
done
case "$operation" in
  ecr:describe-images) printf '%s\n' 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' ;;
  ecs:describe-services) printf 'arn:aws:ecs:ap-south-1:111111111111:task-definition/planext4u-production-%s:7\n' "$service" ;;
  ecs:describe-task-definition)
    name=identity
    [[ "$definition" == *catalog* ]] && name=catalog
    printf '{"family":"planext4u-production-%s","taskRoleArn":"arn:aws:iam::111111111111:role/task","executionRoleArn":"arn:aws:iam::111111111111:role/execution","networkMode":"awsvpc","containerDefinitions":[{"name":"%s","image":"old@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"requiresCompatibilities":["FARGATE"],"cpu":"512","memory":"1024","taskDefinitionArn":"old","revision":7,"status":"ACTIVE"}\n' "$name" "$name"
    ;;
  ecs:register-task-definition) printf '%s\n' 'arn:aws:ecs:ap-south-1:111111111111:task-definition/candidate:8' ;;
  ecs:update-service) printf '%s\n' '{}' ;;
  ecs:wait) ;;
  *) printf 'unexpected fake aws operation %s\n' "$operation" >&2; exit 1 ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(temporary, "curl"), []byte("#!/usr/bin/env bash\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(temporary, "release.json")
	command := exec.Command("bash", "../../scripts/deploy-release-ecs.sh")
	command.Env = append(os.Environ(),
		"PATH="+temporary+":"+os.Getenv("PATH"), "FAKE_AWS_CALLS="+calls,
		"AWS_REGION=ap-south-1", "ECS_CLUSTER=planext4u-production",
		"ECR_REGISTRY=111111111111.dkr.ecr.ap-south-1.amazonaws.com/planext4u",
		"SOURCE_REVISION=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		`SERVICES_JSON=["identity","catalog"]`, "DEPLOYMENT_ENVIRONMENT=production",
		"SMOKE_URL=https://api.example.test/readyz", "RELEASE_EVIDENCE="+evidence)
	if err := command.Run(); err == nil {
		t.Fatal("failed final smoke returned a successful release")
	}
	var record struct {
		Deployed      bool   `json:"deployed"`
		FailureReason string `json:"failure_reason"`
		Rollback      []struct {
			Service string `json:"service"`
			Status  string `json:"status"`
		} `json:"rollback"`
	}
	contents, err := os.ReadFile(evidence)
	if err != nil || json.Unmarshal(contents, &record) != nil {
		t.Fatalf("release evidence unavailable or invalid: %v %s", err, contents)
	}
	if record.Deployed || record.FailureReason != "release_readiness_failed" || len(record.Rollback) != 2 ||
		record.Rollback[0].Service != "catalog" || record.Rollback[1].Service != "identity" ||
		record.Rollback[0].Status != "passed" || record.Rollback[1].Status != "passed" {
		t.Fatalf("release evidence = %s", contents)
	}
}

func TestBEInfra001WorkflowsPinActionsAndAvoidStaticCloudKeys(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"../../.github/workflows/backend-ci.yml", "../../.github/workflows/backend-delivery.yml"} {
		contents := readFile(t, path)
		for _, line := range strings.Split(contents, "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "uses:") {
				continue
			}
			parts := strings.Split(strings.Fields(trimmed)[1], "@")
			if len(parts) != 2 || len(parts[1]) != 40 {
				t.Errorf("%s has an action that is not pinned to a full commit: %s", path, trimmed)
			}
		}
		for _, forbidden := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "secrets.AWS_"} {
			if strings.Contains(contents, forbidden) {
				t.Errorf("%s contains forbidden static AWS credential marker %q", path, forbidden)
			}
		}
	}
	ci := readFile(t, "../../.github/workflows/backend-ci.yml")
	for _, marker := range []string{"actions: read", "security-events: read", "build-mode: manual", "go build ./...", "upload: false", "Enforce zero CodeQL findings", "Upload CodeQL evidence"} {
		if !strings.Contains(ci, marker) {
			t.Errorf("CI workflow is missing the explicit CodeQL control %q", marker)
		}
	}
	delivery := readFile(t, "../../.github/workflows/backend-delivery.yml")
	for _, marker := range []string{"id-token: write", "cosign sign", "cosign attest", "environment:", "./scripts/deploy-release-ecs.sh", "if: always()"} {
		if !strings.Contains(delivery, marker) {
			t.Errorf("delivery workflow is missing %q", marker)
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
