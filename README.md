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
- [Six-phase programme plan](https://github.com/yazhsab/planext4u-moble/blob/main/docs/PROGRAM_PLAN.md)
- [Flutter applications](https://github.com/yazhsab/planext4u-moble)
