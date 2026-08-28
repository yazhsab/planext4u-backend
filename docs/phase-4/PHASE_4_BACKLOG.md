# Phase 4 backend executable backlog

## Outcome

Deliver supply onboarding, service booking, food ordering, fulfilment, routing,
settlement, attendance, franchise operations and their administrator controls.
All capacity, lifecycle, location, money, commission and payout decisions remain
server-owned, idempotent and auditable.

| ID | Story | Depends on | Acceptance evidence |
| --- | --- | --- | --- |
| `BE-P4-001` | Publish additive service/supply/food/fulfilment/settlement contracts and service-owned schemas | Phase 3 contracts and ledgers | Compatibility, synthetic fixture and from-zero migration checks pass |
| `BE-P4-002` | Implement geo-filtered service discovery, provider trust, weekly schedules and atomic expiring slot locks | 001 | Timezone, overlap, capacity-race, hold-expiry and cross-zone denial tests pass |
| `BE-P4-003` | Implement service booking, advance/full payment, payment recovery, one-free-reschedule and cancellation/refund compensation | 001-002, Phase 3 payment/wallet | Idempotency, revision, cutoff, refund and zero-stranded-capacity tests pass |
| `BE-P4-004` | Implement provider arrival, start OTP, completion evidence, confirmation, no-show, dispute and verified rating | 003, media/audit/notification | OTP replay/expiry, evidence authorization, no-show and dispute suites pass |
| `BE-P4-005` | Implement vendor registration, OCR-assisted document review, staged KYC, field visits, zones and bank verification | 001, media/audit | Private-document scope, transition, bank and privileged approval tests pass |
| `BE-P4-006` | Implement vendor profile, product/service catalog, inventory/schedule and product/booking queues | 002-005 | Owned-resource, revision, approval and safe-transition suites pass |
| `BE-P4-007` | Implement restaurant discovery, menus/customisation, food cart, cut-offs and restaurant queue | 001, Phase 3 commerce | Authoritative pricing, customisation, cut-off, reject/timeout and refund tests pass |
| `BE-P4-008` | Implement rider onboarding/duty, atomic offers, routing, live location, offline command recovery, POD and reassignment | 001, 005-007 | Concurrent accept, location TTL, offline ordering, blur/OTP/signature and reassignment suites pass |
| `BE-P4-009` | Implement time-bounded order chat, contact policy and safe communication receipts | 003-004, 007-008 | Actor scope, expiry, block, redaction and retry tests pass |
| `BE-P4-010` | Implement immutable vendor/rider settlement, commission, cooling, payout, attendance and earnings ledgers | 003-008 | Calculation-version, four-eyes, payout retry and zero-variance reconciliation tests pass |
| `BE-P4-011` | Implement franchise/field operations, regional dispatch, live maps and supply/SLA administrator controls | 005-010 | Territory isolation, geo check-in, RBAC/MFA/audit and dashboard projection tests pass |
| `BE-P4-012` | Complete controlled service, food, vendor and rider journeys plus performance/resilience gates | 001-011 | `MOB-E2E-004` through `007`, load, offline recovery, battery and 2 GB device gates pass |

## Entry slice

Implementation begins with `BE-P4-001` through `BE-P4-004`. The slice is not
accepted by the backend merely because a booking row exists: the service must
prevent double holds under concurrency, expire capacity safely, reject stale
revisions, verify payment state server-side, compensate wallet/provider money,
protect start OTP material, require completion evidence and enforce actor scope.
