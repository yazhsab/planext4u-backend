# Backend, platform and contract test strategy

## Test pyramid and gates

| Layer | Required evidence | Gate |
| --- | --- | --- |
| Static/build | `gofmt`, `go vet`, lint, dependency/license/secret/IaC/container scans, reproducible signed image | Every pull request |
| Unit/property/fuzz | Domain policies, state machines, money/points/tax, validators, idempotency and event handlers | Changed packages >= 80%; critical invariants 100% branches/properties |
| Race | `go test -race` for services/libraries with concurrency | Pull request or bounded nightly according to duration; release mandatory |
| Integration | PostgreSQL/Redis/event/object-storage with Testcontainers; migrations forward/backward compatibility | Every service merge |
| Contract | OpenAPI/AsyncAPI compatibility, provider/consumer fixtures and role denial | Every contract/service merge |
| Workflow | Multi-service sagas, duplicate/out-of-order/retry/compensation and reconciliation | Staging release |
| Performance/resilience | k6 baseline/burst/soak, queue replay, provider timeout, Redis loss, DB failover | Phase-specific release gate |
| Security/privacy | Threat cases, DAST, authorization, webhook, export, deletion/retention and independent penetration | Staging/production readiness |

## Named Phase 2 suites

- `BE-CONTRACT-001`: common error, cursor, idempotency, money/time/location and compatibility fixtures.
- `BE-AUTH-001`: OTP/provider-token verification, platform token issue/refresh/revoke/reuse detection and device audit.
- `BE-AUTHZ-001`: cross-role, cross-tenant, resource-owner and privileged fresh-auth denial matrix.
- `BE-OUTBOX-001`: transaction commit/outbox publication, duplicate delivery, consumer inbox and DLQ/replay.
- `BE-MEDIA-001`: presign/upload/complete/scan/ready/reject, type/size, URL expiry, IDOR and deletion.
- `BE-CONFIG-001`: tenant/country flags, minimum version, CMS cache/invalidation and safe offline defaults.
- `BE-NOTIFY-001`: preference/consent, template validation, outbox, retry, provider degradation and receipt.
- `BE-AUDIT-001`: privileged event completeness, immutability, redaction and authorised export.
- `BE-VSLICE-001`: login -> location -> customer home -> catalog read through gateway/BFF with correlated trace.
- `BE-OBS-001`: RED/USE metrics, trace propagation, structured log redaction and actionable alert/runbook link.

## Critical invariant suites

- Order/payment/inventory: concurrent reservation, duplicate place order, webhook replay/out-of-order, compensation and zero double capture.
- Wallet/settlement: double-entry/immutable source, expiry/refund lots, four-eyes denial, payout retry and reconciliation with zero unexplained variance.
- Booking/assignment: unique slot/offer acceptance, TTL expiry, offline/conflicting command and valid server next actions.
- Privacy/security: deletion propagation, legal hold, location TTL/access window, KYC signed URL scope, CSV formula injection and event poisoning.
- Migration: deterministic transform, idempotent resume, checksum/count/referential/financial reconciliation and rejected-row accountability.

## Test environments and data

- Unit/contract fixtures are deterministic and synthetic.
- Integration tests create isolated containers/databases and apply real migrations from zero.
- Ephemeral preview environments use synthetic tenant seeds and fake/sandbox providers.
- Staging provides stable synthetic customer/vendor/rider/admin identities, signed webhook fixtures and resettable journey records.
- Production data and raw legacy exports never enter CI, developer laptops or general preview environments.

## Performance profiles

The accepted capacity model defines endpoint SLOs and minimum 1M-MAU/10K-session target. Tests include 30-minute baseline, 2.5x 10-minute burst, 8-24-hour soak, dependency degradation, event replay and AZ/database failover. Financial correctness and queue/reconciliation lag are pass/fail metrics alongside latency/error rate.

## Release policy

- P0/P1 correctness, authorization, privacy or financial defects block release.
- A flaky critical test is a failed gate; quarantine needs owner/issue/seven-day expiry and an equivalent reliable gate.
- Schema/contract golden updates require reviewed intent and compatibility evidence.
- Failed chaos/load tests require either a fix or an explicitly disabled rollout path; SLOs are not silently waived.
- Every production incident adds detection and regression coverage before closure.
