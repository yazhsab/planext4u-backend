# Planext4u AWS infrastructure

This tree defines a greenfield multi-account AWS baseline. It does not contain account IDs, live email addresses, passwords, state, certificates or provider credentials.

## Layout

- `organization` requests dedicated development and staging accounts under an existing AWS Organizations OU. Account resources are protected against Terraform destruction.
- `bootstrap` creates the central versioned, KMS-encrypted S3 state bucket. Environment backends use native S3 lock files (`use_lockfile = true`), not the deprecated DynamoDB locking option.
- `modules/network` creates three-AZ public/private networking, NAT isolation, VPC flow logs and private AWS endpoints.
- `modules/data` creates encrypted Aurora PostgreSQL, Valkey, private media storage and empty Secrets Manager containers. RDS manages its own migration password; runtime database URLs, the approved managed NATS JetStream endpoint, and the staging-only synthetic journey signing key are populated by a separate secret-provisioning control so plaintext never enters Terraform state.
- `modules/artifacts` creates immutable, KMS-encrypted, scan-on-push ECR repositories.
- `modules/compute` creates private Fargate services, Service Connect discovery, a deliberate public TLS-only ALB protected by WAF, VPC-only service egress, least-privilege task roles, deployment rollback and autoscaling. Future public payment/provider calls must traverse a separately reviewed allowlisted egress proxy.
- `modules/delivery_identity` binds short-lived GitHub OIDC identities to the exact repository and protected environment. No static AWS access keys are accepted.
- `environments/development` and `environments/staging` provide isolated sizing and deletion-protection settings.

## Controlled bootstrap

Applying these files creates billable cloud resources and AWS accounts. A platform owner must supply an authorized Organizations/AWS session, unique account emails, account IDs, ACM certificates, permissions boundaries and an encrypted state bucket. Do not apply examples unchanged.

1. Copy `organization/terraform.tfvars.example`, replace every synthetic value, review the plan with the AWS organization owner, and apply once from the management account. Import existing accounts instead of recreating them.
2. Apply `bootstrap` from the security/tooling account. Configure each environment backend with partial `-backend-config` values for `bucket` and `kms_key_id`; credentials come from the federated session, never backend files.
3. Bootstrap the approved Terraform deployment role in each workload account, then plan the development and staging roots with their reviewed `.tfvars` files.
4. Create/push the initial immutable platform image before applying ECS services. Populate each service database secret using the migration credential, populate the event-bus secret using the managed NATS provider control, and generate at least 32 random bytes for the staging synthetic-slice signing key. Never copy that key to production.
5. Configure GitHub `staging` and `production` environments with required reviewers, no self-review, protected branches and no administrator bypass. Store only role ARNs, region, cluster/service names, ECR repository and smoke URL as environment variables.
6. Run `make infra-check`; archive the signed plan and approval record before apply.

The delivery workflow remains safely dormant until the repository variable
`DELIVERY_ENABLED=true` is set. Each protected environment requires these
non-secret variables: `AWS_ARTIFACT_ROLE_ARN`, `AWS_DEPLOY_ROLE_ARN`,
`AWS_REGION`, `ECR_REPOSITORY`, `ECS_CLUSTER`, and `SMOKE_URL`. Set
`ENABLE_GITHUB_ATTESTATIONS=true` only when the GitHub plan supports private
repository attestations; Sigstore keyless image and SBOM signing is always
performed when delivery is enabled.

Staging also requires `VERTICAL_SLICE_SMOKE_ORIGIN` set to its public HTTPS origin. A successful deployment must pass the correlated `login -> location -> home -> catalog` smoke journey; any failure automatically restores the prior task definition. The synthetic staging executable refuses to start outside `APP_ENV=staging` or without its explicit enable flag and managed signing key.

Official operating guidance: [Terraform S3 locking and permissions](https://developer.hashicorp.com/terraform/language/backend/s3), [Terraform sensitive-state controls](https://developer.hashicorp.com/terraform/language/manage-sensitive-data), [AWS Terraform provider practices](https://docs.aws.amazon.com/prescriptive-guidance/latest/terraform-aws-provider-best-practices/), and [GitHub deployment environments](https://docs.github.com/en/actions/reference/workflows-and-actions/deployments-and-environments).
