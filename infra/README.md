# Planext4u AWS infrastructure

This tree defines a greenfield multi-account AWS baseline. It does not contain account IDs, live email addresses, passwords, state, certificates or provider credentials.

## Layout

- `organization` requests dedicated development and staging accounts under an existing AWS Organizations OU. Account resources are protected against Terraform destruction.
- `bootstrap` creates the central versioned, KMS-encrypted S3 state bucket. Environment backends use native S3 lock files (`use_lockfile = true`), not the deprecated DynamoDB locking option.
- `modules/network` creates three-AZ public/private networking, NAT isolation, VPC flow logs and private AWS endpoints.
- `modules/data` creates encrypted Aurora PostgreSQL, Valkey, private media storage and empty Secrets Manager containers. RDS manages its own migration password; runtime database URLs and the approved managed NATS JetStream endpoint are populated by a separate secret-provisioning control so plaintext never enters Terraform state.
- `modules/artifacts` creates immutable, KMS-encrypted, scan-on-push ECR repositories.
- `modules/compute` creates private Fargate services, Service Connect discovery, a deliberate public TLS-only ALB protected by WAF, VPC-only service egress, least-privilege task roles, deployment rollback and autoscaling. The authenticated gateway is the public default; only the cookie/HMAC-protected administrator paths have a separate listener rule. Domain services are never directly routed from the public load balancer. Future public payment/provider calls must traverse a separately reviewed allowlisted egress proxy.
- `modules/delivery_identity` binds short-lived GitHub OIDC identities to the exact repository and protected environment. No static AWS access keys are accepted.
- `environments/development`, `environments/staging`, and `environments/production` provide isolated sizing and deletion-protection settings. Production uses three database instances, multi-AZ networking, 35-day backups, and deletion protection.

## Controlled bootstrap

Applying these files creates billable cloud resources and AWS accounts. A platform owner must supply an authorized Organizations/AWS session, unique account emails, account IDs, ACM certificates, permissions boundaries and an encrypted state bucket. Do not apply examples unchanged.

1. Copy `organization/terraform.tfvars.example`, replace every synthetic value, review the plan with the AWS organization owner, and apply once from the management account. Import existing accounts instead of recreating them.
2. Apply `bootstrap` from the security/tooling account. Configure each environment backend with partial `-backend-config` values for `bucket` and `kms_key_id`; credentials come from the federated session, never backend files.
3. Bootstrap the approved Terraform deployment role in each workload account, then plan the development, staging, and production roots with their reviewed `.tfvars` files.
4. Create/push every immutable service image before applying ECS services. Populate each service database secret with that service's restricted login, populate the separate migration database secret with the TLS-only migration credential, and populate the event-bus secret through the managed NATS provider control. Configure service-specific secret ARNs with `_FILE_VALUE` names; the minimal bootstrap materializes them into a writable task volume with mode `0600`, removes supplementary groups, and drops permanently to UID/GID `65532` before executing either the service or migration binary.
5. Configure GitHub `staging` and `production` environments with required reviewers, no self-review, protected branches and no administrator bypass. Store only role ARNs, region, cluster/service names, ECR repository and smoke URL as environment variables.
6. Run `make infra-check`; archive the signed plan and approval record before apply.

The delivery workflow remains safely dormant until the repository variable
`DELIVERY_ENABLED=true` is set. Each protected environment requires these
non-secret variables: `AWS_ARTIFACT_ROLE_ARN`, `AWS_DEPLOY_ROLE_ARN`,
`AWS_REGION`, `ECR_REGISTRY` (for example, `account.dkr.ecr.region.amazonaws.com/planext4u`),
`ECS_CLUSTER`, `ECS_MIGRATION_TASK_DEFINITION`, comma-separated
`ECS_PRIVATE_SUBNETS`, comma-separated `ECS_SECURITY_GROUPS`, and `SMOKE_URL`.
The migration values come from the environment Terraform outputs. Set
`ENABLE_GITHUB_ATTESTATIONS=true` only when the GitHub plan supports private
repository attestations; Sigstore keyless image and SBOM signing is always
performed when delivery is enabled.

Every release builds and signs the selected service set, runs all checksum-verified migrations in a one-off private Fargate task, and only then rolls services out by digest. The rollout is ordered and release-atomic: any service failure or final smoke failure restores every service already changed in that release. Staging requires `VERTICAL_SLICE_SMOKE_ORIGIN`, `STAGING_SMOKE_PROVIDER` (`firebase` or `oidc`), the protected secret `STAGING_SMOKE_PROVIDER_TOKEN`, and an approved published CMS baseline. Optional country and coordinates are supplied through `STAGING_SMOKE_COUNTRY`, `STAGING_SMOKE_LATITUDE`, and `STAGING_SMOKE_LONGITUDE`. A successful staging release exercises the real distributed `provider login -> location -> CMS bootstrap -> home -> catalog` path through the gateway; no synthetic identity route is deployed.

Official operating guidance: [Terraform S3 locking and permissions](https://developer.hashicorp.com/terraform/language/backend/s3), [Terraform sensitive-state controls](https://developer.hashicorp.com/terraform/language/manage-sensitive-data), [AWS Terraform provider practices](https://docs.aws.amazon.com/prescriptive-guidance/latest/terraform-aws-provider-best-practices/), and [GitHub deployment environments](https://docs.github.com/en/actions/reference/workflows-and-actions/deployments-and-environments).
