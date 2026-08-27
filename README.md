# Planext4u Backend

Greenfield, production-grade Go microservices platform for Planext4u.

This repository is intentionally independent of the Lovable/Vercel proof-of-concept. It contains no reused legacy source code. Existing behaviour and data will be represented through approved contracts, traceability, and migration jobs.

## Planned workspace

```text
cmd/                 service entry points
internal/            bounded-context implementation
pkg/                 narrowly shared technical libraries
api/                 OpenAPI, AsyncAPI and protobuf contracts
migrations/          service-owned database migrations
deploy/              Helm/ECS manifests and environment overlays
infra/               Terraform modules and environment compositions
admin-web/           separately deployable administrator console
tests/               contract, integration, performance and resilience suites
docs/                architecture, service catalogue and decisions
```

The system will use Go for all backend services and workers. The administrator console is a web client that communicates only through an administrator BFF; it is kept here initially to preserve the requested two-repository structure.

## Current status

Phase 1 architecture and target definition is complete. The approved Phase 2 backlog establishes the Go platform, contracts and staging vertical slice.

Phase 2 implementation includes the greenfield Go platform, versioned
contracts, deterministic local dependencies, secure gateway, identity/customer,
configuration, catalog, media, append-only audit, reliable messaging,
notification, service-owned PostgreSQL migrations, OpenTelemetry/SLO operations,
and validated AWS Terraform foundations.

## Local development

Prerequisite: Go 1.25.14 or newer. The module toolchain directive prevents
builds with earlier vulnerable Go 1.25 patch releases.

```sh
make verify
make run
```

`make infra-check` validates all Terraform roots with their locked provider
versions. It requires Terraform 1.13 or newer. `make container-build` builds the
minimal non-root release container.

The reviewed OpenAPI 3.1 and AsyncAPI 3.0 documents under `api/` are the
contract authority. `make contract-generate` refreshes deterministic Go and
Dart consumer fixtures; `make contract-check` fails on stale output or a
breaking change to the checked-in v1 compatibility baseline.

The default development listener is `:8080`:

- `GET /healthz` reports process liveness.
- `GET /readyz` reports whether the process is ready to accept traffic.

The deterministic container platform is documented in
[`deploy/local/README.md`](deploy/local/README.md). `make local-up` starts and
health-checks PostgreSQL, Redis, NATS JetStream, object storage and synthetic
provider adapters; `make test-integration` verifies isolated dependencies with
Testcontainers.

Supported non-secret environment variables are `SERVICE_NAME`, `APP_ENV`,
`HTTP_ADDRESS`, `SHUTDOWN_TIMEOUT`, and `LOG_LEVEL`. Secret material must come
from the environment-specific secret provider introduced in the platform
infrastructure work; it does not belong in this configuration type or in local
environment files.

- [Target architecture](docs/ARCHITECTURE.md)
- [Service catalogue](docs/SERVICE_CATALOGUE.md)
- [Non-functional requirements](docs/NON_FUNCTIONAL_REQUIREMENTS.md)
- [Phase 1 service-boundary map](docs/phase-1/SERVICE_BOUNDARY_MAP.md)
- [Critical state machines](docs/phase-1/STATE_MACHINES.md)
- [Data classification and retention](docs/phase-1/DATA_CLASSIFICATION_RETENTION.md)
- [Capacity and SLO model](docs/phase-1/CAPACITY_SLO_MODEL.md)
- [Migration and coexistence strategy](docs/phase-1/MIGRATION_COEXISTENCE.md)
- [Threat model](docs/phase-1/THREAT_MODEL.md)
- [Contract standards](docs/phase-1/CONTRACT_STANDARDS.md)
- [Security control plan](docs/phase-1/SECURITY_CONTROL_PLAN.md)
- [Backend test strategy](docs/phase-1/TEST_STRATEGY.md)
- [Legacy export inventory](docs/phase-1/LEGACY_EXPORT_INVENTORY.md)
- [Phase 2 executable backlog](docs/phase-1/PHASE_2_BACKLOG.md)
- [Phase 1 exit review](docs/phase-1/PHASE_1_EXIT_REVIEW.md)
- [Greenfield backend ADR](docs/adr/0001-go-greenfield-platform.md)
- [Gateway foundation](docs/phase-2/GATEWAY_FOUNDATION.md)
- [Identity/customer foundation](docs/phase-2/IDENTITY_CUSTOMER_FOUNDATION.md)
- [Six-phase programme plan](https://github.com/yazhsab/planext4u-moble/blob/main/docs/PROGRAM_PLAN.md)
- [Flutter applications](https://github.com/yazhsab/planext4u-moble)
