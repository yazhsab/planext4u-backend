# Target architecture

## Principles

1. Domain ownership over table-oriented services.
2. Server-owned truth for money, points, stock, permissions, settlements and state transitions.
3. Contract-first and backward-compatible interfaces.
4. Idempotent commands and events; immutable financial/audit ledgers.
5. Stateless horizontal scaling except for explicitly managed state stores.
6. Secure-by-default access, data minimisation and complete privileged-action auditing.
7. No direct dependency on the Lovable/Vercel codebase.

## Request and event path

```text
Flutter apps / Admin web
        |
   CloudFront + WAF
        |
 API Gateway / ALB
        |
 Customer BFF | Vendor BFF | Rider BFF | Admin BFF | Realtime Gateway
        |
 Go domain services ---- Transactional outbox ---- Kafka/MSK-compatible event bus
        |                                      |             |
 PostgreSQL/RDS                        Workflow workers   Notification/media/report workers
        |
 Redis/ElastiCache for cache, locks, rate limits and ephemeral presence

Media: client -> presigned S3 upload -> validation/transcode workers -> CloudFront
Search: domain events -> OpenSearch indexes; PostgreSQL remains system of record
Observability: OpenTelemetry -> metrics, logs, traces, Sentry-compatible mobile correlation
```

## Data platform

- PostgreSQL Multi-AZ with PITR; separate service ownership and credentials.
- Redis for short-lived cache, distributed rate limits, locks and presence—not financial truth.
- Kafka/MSK-compatible event bus for durable domain events; schemas versioned in-repository.
- Transactional outbox/inbox and deduplication for reliable event publication/consumption.
- S3 public/private buckets, CloudFront, encryption and short-lived signed access.
- OpenSearch for product/vendor/content discovery and reporting projections when PostgreSQL search no longer meets SLOs.
- Analytics lake/warehouse projections built from events; operational services are never queried by large exports.

## Reliability patterns

- Idempotency keys on checkout, payment, wallet, inventory, booking and delivery mutations.
- Orchestrated sagas for order/payment/inventory, returns/refunds, settlements and KYC.
- Optimistic concurrency or row locks for stock, booking slots and wallet ledgers.
- Retry budgets, exponential backoff, circuit breakers, bulkheads and dead-letter queues.
- Multi-AZ deployment, autoscaling, health/readiness checks, graceful shutdown and zero-downtime migrations.
- Per-service dashboards, burn-rate alerts, runbooks and trace propagation across HTTP, events and mobile sessions.

## Security

- Phone OTP through Firebase Auth or an approved provider; Go verifies the provider token and issues RS256 access/refresh tokens.
- RBAC plus policy checks per resource; fresh MFA/reauth for settlements, KYC and privileged administration.
- WAF, IP/user/device rate limits, bot controls, device risk signals and session revocation.
- Secrets Manager/KMS, TLS 1.2+, encryption at rest, signed URLs, payload validation and least-privilege IAM.
- DPDP consent/purpose records, retention/deletion workflows and evidence-grade audit events.
- Payment secrets and signatures exist only in payment services; no secret enters Flutter or admin clients.

## Deployment model

- AWS dev, staging and production accounts managed by Terraform.
- Containerised Go services on ECS Fargate initially; EKS requires a measured operational need.
- Blue/green or canary deployments with automated migration and rollback gates.
- GitHub Actions for lint, tests, SBOM, SAST, dependency/container/IaC scans, signed images and environment promotion.
- Local development through Docker Compose/Testcontainers with fake external providers and deterministic seed data.
