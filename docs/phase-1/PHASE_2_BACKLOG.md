# Phase 2 executable backlog

## Outcome

Deliver a contract-first Go platform and automated staging vertical slice supporting mobile login -> location -> customer home -> catalog read, plus the secure administrator shell foundation.

Target window: 2026-09-24 to 2026-11-04. Estimates are Fibonacci story points for one backend/platform squad; infrastructure and admin-web work may run in parallel when dependencies permit.

| ID | Story | Points | Depends on | Acceptance evidence |
| --- | --- | ---: | --- | --- |
| `BE-P2-001` | Bootstrap Go workspace, toolchain, service template and engineering commands | 8 | None | Clean clone builds/tests; health/readiness/shutdown/config/log template passes |
| `BE-P2-002` | Publish common OpenAPI/AsyncAPI schemas and compatibility gates | 13 | 001 | `BE-CONTRACT-001`; generated Go/Dart fixtures and breaking-change gate pass |
| `BE-P2-003` | Create local PostgreSQL/Redis/event/object-store/provider sandbox environment | 8 | 001 | One command starts deterministic dependencies; Testcontainers suite passes |
| `BE-P2-004` | Implement gateway/BFF authentication, rate limits, correlation and error mapping | 13 | 002-003 | Denial/rate/size/timeout/correlation tests; no domain truth in edge |
| `BE-P2-005` | Implement identity/customer foundation: provider token, platform sessions, devices, roles, profile/consent | 21 | 002-003 | `BE-AUTH-001`, `BE-AUTHZ-001`; refresh rotation/revoke/reuse and audit pass |
| `BE-P2-006` | Implement configuration/CMS service for tenant/country/flags/version/home composition | 13 | 002-003 | `BE-CONFIG-001`; safe defaults, cache invalidation and audit pass |
| `BE-P2-007` | Implement catalog-read foundation and customer-home projection fixtures | 13 | 002-003, 006 | Cursor/search/category/item read contracts and stale/degraded tests pass |
| `BE-P2-008` | Implement media presign/complete/scan lifecycle foundation | 13 | 002-003 | `BE-MEDIA-001`; private/public isolation, scan and deletion pass |
| `BE-P2-009` | Implement notification preferences/template/outbox/provider adapter foundation | 13 | 002-003, 011 | `BE-NOTIFY-001`; consent/retry/degradation/receipt pass |
| `BE-P2-010` | Implement append-only audit ingestion, search and authorised export foundation | 8 | 002-003 | `BE-AUDIT-001`; privileged-event completeness and redaction pass |
| `BE-P2-011` | Implement transactional outbox/inbox, schema registry rules, DLQ and replay tooling | 13 | 002-003 | `BE-OUTBOX-001`; duplicate/out-of-order/crash/replay tests pass |
| `BE-P2-012` | Establish service-owned PostgreSQL migrations, ownership credentials and zero-downtime gate | 8 | 001, 003 | From-zero/upgrade/backward-compatibility/failed-migration tests pass |
| `BE-P2-013` | Establish OpenTelemetry logs/metrics/traces, SLO dashboards and alert/runbook templates | 8 | 001, 004-011 | `BE-OBS-001`; end-to-end trace and redaction evidence pass |
| `BE-P2-014` | Create Terraform AWS account/network/compute/data foundations and CI/CD security gates | 21 | 001, security plan | Plan/scan/sign/promote/rollback evidence in dev and staging |
| `BE-P2-015` | Create admin-web shell with MFA-ready auth, RBAC navigation, country context and audit view | 13 | 002, 004-006, 010 | Cross-role denial, fresh-auth placeholder and accessible shell tests pass |
| `BE-P2-016` | Integrate and deploy the staging vertical slice with synthetic data | 13 | 004-015, mobile contract | `BE-VSLICE-001`; automated staging deploy and rollback smoke pass |

Total baseline: 199 points.

## Delivery slices

1. Engineering/contract substrate: `001-003`, `012`.
2. Trust and platform primitives: `004-006`, `008`, `010-011`.
3. Read vertical and clients: `007`, `009`, `015`.
4. Operability and automated staging: `013-014`, `016`.

## Phase 2 exit criteria

- Contracts generate compatible Go/Dart clients and pass provider/consumer gates.
- Identity/session/role denial and administrator fresh-auth foundations pass security suites.
- Customer vertical slice deploys automatically to staging with synthetic data and no manual environment mutation.
- Service-owned migrations, outbox/inbox, redacted telemetry, dashboards and actionable alerts pass.
- Signed/scanned artifacts promote through dev/staging with tested rollback.
- The paired mobile `MOB-E2E-001` journey passes end to end.
