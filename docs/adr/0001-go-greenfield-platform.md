# ADR-0001: Go greenfield platform

- Status: Accepted
- Date: 2026-08-26

## Context

Earlier product documents describe Node.js services and Firebase/Supabase-oriented prototypes. The product owner explicitly requires a new high-load backend implemented as Go microservices and forbids reuse of the existing application codebase.

## Decision

- All APIs, workers, schedulers, realtime gateways and domain services are implemented in Go.
- The platform is contract-first: OpenAPI for synchronous APIs, AsyncAPI/protobuf for events, and generated clients for Flutter and admin web.
- Services own their data and migrations. Initially they may share an RDS PostgreSQL cluster through isolated schemas/databases, but they may not query another service's tables.
- Cross-service state changes use transactional outbox events, idempotent consumers and workflow/saga orchestration.
- The legacy POC is not a runtime dependency. Migration is performed through authorised exports and repeatable ETL.

## Consequences

- The Node.js technology references in older documents are superseded.
- New contracts and data models must be approved against the requirements matrix.
- Distributed-system failure modes, observability, versioning and reconciliation are first-class deliverables.
