# Terraform and secure delivery foundation

`BE-P2-014` provides reproducible development/staging account requests, encrypted remote state, private multi-AZ compute/data foundations and a digest-only deployment mechanism with automatic rollback evidence.

The container is a static scratch runtime. Its minimal bootstrap writes mounted secret values with mode `0600`, drops all supplementary groups and permanently changes to UID/GID `65532` before executing a service. CI performs Go verification, race tests, CodeQL, Terraform validation, IaC and filesystem vulnerability scans, a container scan, SPDX SBOM generation, keyless signing and build/SBOM provenance. Delivery exchanges GitHub OIDC tokens for short-lived environment-bound AWS roles, runs checksum-verified migrations in a one-off private Fargate task, updates every selected ECS service using the exact image digest, waits for service stability, performs the environment smoke check and restores the previous task definition on failure.

Staging deploys from `main`. Production promotion is manual through the `production` GitHub environment and must be configured with required reviewers, no self-review and no bypass. The deploy script emits a non-secret JSON record containing source commit, workflow run, image digest, previous/candidate task definitions, outcome and rollback reason.

Applying AWS Organizations or environment roots is intentionally not automatic from an untrusted workstation: it requires the customer's organization owner, unique account emails, billable-resource approval, target account IDs, ACM certificates, DNS and federated roles. The exact controlled bootstrap sequence is documented in `infra/README.md`.
