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

Planning baseline only. Service implementation begins after Phase 1 architecture and requirements sign-off.

- [Target architecture](docs/ARCHITECTURE.md)
- [Service catalogue](docs/SERVICE_CATALOGUE.md)
- [Non-functional requirements](docs/NON_FUNCTIONAL_REQUIREMENTS.md)
- [Greenfield backend ADR](docs/adr/0001-go-greenfield-platform.md)
- [Six-phase programme plan](https://github.com/yazhsab/planext4u-moble/blob/main/docs/PROGRAM_PLAN.md)
- [Flutter applications](https://github.com/yazhsab/planext4u-moble)
