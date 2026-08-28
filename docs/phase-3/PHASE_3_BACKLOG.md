# Phase 3 backend executable backlog

## Outcome

Deliver the marketplace transaction core from discovery through a server-authoritative cart, checkout, payment recovery, order lifecycle, wallet ledger and the corresponding administrator operations. All money uses integer minor units; client-supplied prices are never trusted.

| ID | Story | Depends on | Acceptance evidence |
| --- | --- | --- | --- |
| `BE-P3-001` | Publish commerce contracts, service-owned schema and compatibility fixtures | Phase 2 contracts/migrations | Contract generation, compatibility and from-zero migration checks pass |
| `BE-P3-002` | Enrich catalog reads for production PDP variants, seller trust, availability and reviews | 001 | Search/PDP contract and stale/degraded projection tests pass |
| `BE-P3-003` | Implement customer cart with server price/stock resolution, idempotency and optimistic revision control | 001-002 | Authoritative price, replay/conflict, stock and concurrent-revision tests pass |
| `BE-P3-004` | Add promotion, tax, fee and delivery-slot pricing orchestration | 003 | Deterministic reprice and stale-price warning fixtures pass |
| `BE-P3-005` | Implement inventory reservation and release lifecycle | 003-004 | Stock-race, expiry and compensation tests pass |
| `BE-P3-006` | Implement checkout and Razorpay/Paystack/COD orchestration | 004-005 | Signed webhook, duplicate delivery, retry and reconciliation tests pass |
| `BE-P3-007` | Implement order, cancellation, return, refund and proof lifecycle | 006 | State-machine, partial refund and wallet reversal tests pass |
| `BE-P3-008` | Implement immutable wallet/points ledger, expiry, referral and anti-abuse controls | 001, 006-007 | `BE-WALLET-001` earn/redeem/expiry/reversal reconciliation passes |
| `BE-P3-009` | Complete admin catalog/order/payment/wallet/campaign/support/reporting operations | 002-008 | RBAC, MFA/four-eyes and privileged audit tests pass |
| `BE-P3-010` | Load, resilience, observability and staging release evidence | 003-009 | SLO load, dependency failure, rollback and mobile paired E2E gates pass |

All ten Phase 3 source stories are implemented. The local engineering evidence
and the separate environment-owned activation gates are recorded in
`PHASE_3_EXIT_REVIEW.md`.

## Entry slice

Implementation starts with `BE-P3-001` through `BE-P3-003`: search/PDP contracts and a server-authoritative cart. The slice is complete only when the backend refuses client money, returns allowed actions, replays identical idempotency keys safely, rejects conflicting reuse and detects stale cart revisions.
