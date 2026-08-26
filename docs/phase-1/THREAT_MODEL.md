# Threat model v1.0

Review status: accepted on 2026-08-26 for Phase 2 architecture. Provider due diligence and penetration testing remain release evidence, not unresolved design decisions.

## Protected assets

- Identity/session/MFA and role policies.
- KYC, bank, payment, wallet, tax and settlement records.
- Customer/vendor/rider PII, addresses, precise live location and emergency requests.
- Product/inventory/order/booking truth and promotion/commission rules.
- Private messages, media, moderation evidence and administrator audit.
- Provider credentials, signing keys, webhooks and deployment control plane.

## Trust boundaries

1. Mobile/admin clients to WAF/gateway.
2. Gateway/BFF to domain services.
3. Service to database/cache/event bus/object storage.
4. Platform to Firebase, payment, maps, WhatsApp, email, KYC and app-store providers.
5. Public media/search/feed projections versus private origin data.
6. Administrator/finance/support/field/franchise scopes.
7. Migration zone and legacy exports.

## Priority abuse cases and controls

| Abuse case | Primary controls | Verification |
| --- | --- | --- |
| OTP bombing/account takeover | Phone/IP/device limits, provider risk, generic errors, session/device review, refresh rotation and revocation | Rate/lockout tests and credential-stuffing simulation |
| Role/territory escalation | Server policy engine, resource ownership, deny-by-default, fresh policy data and audit | Cross-role/tenant contract tests and penetration test |
| Payment/webhook forgery or replay | Provider signature, timestamp/event uniqueness, idempotency, state validation and reconciliation | Replay/out-of-order/invalid-signature suites |
| Wallet/points farming | Immutable ledger, server event proof, visibility/duration/cooldown/device caps and risk review | Bot/device/replay/property tests |
| Stock/slot race and duplicate order | Atomic owner transaction, reservation TTL, optimistic/row locking and idempotency key | High-concurrency correctness tests |
| Settlement/refund fraud | Four-eyes approval, fresh MFA, immutable source entries, limits and anomaly alert | Approval bypass and reconciliation tests |
| KYC/media exfiltration | Private bucket, presigned TTL/scope, malware scan, purpose roles, masking and access audit | URL expiry/scope/IDOR tests |
| Precise-location stalking | Workflow/consent scoped access, raw TTL, coarse historical views and no public identifiers | Role/time-window and deletion tests |
| Fake rider/POD/GPS | Device/task binding, server timestamps, accuracy checks, OTP/signature/photo policy and anomaly signals | Spoof/replay/offline conflict tests |
| Social abuse/unsafe content | Visibility/block/report, rate limits, automated signals, human decisions, appeal and legal hold | Privacy graph and moderation E2E tests |
| Emergency misuse or harmful reliance | Clear disclaimer, verification/triage, responder vetting, escalation/public-service handoff and full audit | Operational simulation and legal review |
| Admin session compromise | MFA, fresh auth, short idle timeout, device/session view, least privilege, anomaly detection and audit | Session fixation/revocation/CSRF/privileged-action tests |
| Export/formula injection | Async authorised export, row/field policy, CSV prefix sanitisation, encryption, expiry and audit | Malicious cell and oversized-range tests |
| Event poisoning/replay | Authenticated producers, schema registry, partition/sequence/idempotency, inbox and DLQ controls | Invalid version, duplicate and replay tests |
| Migration leakage/corruption | Restricted zone, checksums, encryption, masked rehearsal, deterministic transform and reconciliation | Restore/replay/reject and access-control tests |

## Security-output mapping

- Data-flow and classification: `ARCHITECTURE.md`, `SERVICE_BOUNDARY_MAP.md`, `DATA_CLASSIFICATION_RETENTION.md`.
- Abuse-case verification, IAM/service-account baseline, secret inventory, provider register, logging and incident handling: [Security control plan](SECURITY_CONTROL_PLAN.md).
- Automated abuse, role, webhook, financial, location and migration suites: [Backend test strategy](TEST_STRATEGY.md).
