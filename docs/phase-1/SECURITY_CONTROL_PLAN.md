# Security control plan

## Service identity and IAM baseline

| Principal | Permitted access | Explicitly prohibited |
| --- | --- | --- |
| Edge/BFF | Invoke allow-listed service APIs, verify tokens, rate-limit cache | Domain database writes, provider secrets, unrestricted event publish |
| Domain service | Own schema/migrations, own outbox, named dependency APIs | Cross-service tables/credentials, wildcard object-store access |
| Worker/consumer | Named topics/queues, own inbox/read model, scoped object path | Producing as another service, arbitrary replay, admin impersonation |
| Admin web | Admin BFF only through user token and CSRF protections | Direct service/database/provider access, embedded secrets |
| CI builder | Build/test, ephemeral registry push, signed artifact provenance | Long-lived production credentials, production data |
| Deployment role | Promote signed digest and approved migration to one environment | Source mutation, secret plaintext read, cross-environment deploy |
| Break-glass role | Time-limited audited recovery actions with incident/change ID | Routine operations, standing assignment |

- Deny by default; separate AWS accounts and KMS keys for dev/staging/production.
- Workloads use short-lived identities. Human access uses SSO, MFA, just-in-time elevation and session recording/audit where appropriate.
- IAM policies are generated/tested from explicit resource/action matrices; wildcard actions/resources require documented exception and expiry.

## Secret and key inventory

| Secret family | Storage | Rotation/revocation baseline |
| --- | --- | --- |
| Platform JWT signing | KMS/HSM-backed asymmetric keys; public JWKS | Versioned overlap; emergency revoke/runbook; no private export |
| Database credentials | Secrets Manager or workload identity | Automated rotation; one identity per service/environment |
| Provider/webhook credentials | Secrets Manager, readable only by owning integration | Provider-supported rotation; dual-secret transition when available |
| Object/media signing | Workload identity/KMS | Short-lived URLs and key rotation; never mobile/admin embedded |
| CI/deployment signing | OIDC federation and protected environment policy | No static cloud key; revoke trust/policy on compromise |

CI checks source, images, logs and configuration fixtures for secret patterns. Secret values never appear in error payloads, tracing attributes, analytics or support exports.

## External-provider register

| Provider capability | Phase 1 candidate | Data categories | Required pre-production evidence |
| --- | --- | --- | --- |
| Phone OTP | Firebase Auth | Phone, device/risk metadata | Security/cost spike, DPA, residency/transfer, abuse limits, outage/recovery |
| Payments | Razorpay; Paystack for enabled countries | Payment reference, amount, identity/contact minimum | Contract/DPA, signature docs, sandbox, reconciliation, refund/incident contacts |
| Maps/geocoding | Google Maps or approved alternative | Search/address/coordinates | Purpose minimisation, retention, key restriction, regional coverage and fallback |
| Push | FCM/APNs | Device token, notification content | Token lifecycle, sensitive-content policy, delivery/opt-out handling |
| Messaging | WhatsApp/email provider | Contact and template/message metadata | Consent/template rules, retention, delivery receipts, opt-out and outage handling |
| KYC/IDV | HyperVerge or approved alternative | Restricted identity/KYC evidence | DPA, security review, data flow/residency, deletion, human review/appeal and breach SLA |
| Object/CDN | AWS S3/CloudFront | Public/private media, KYC references | Bucket/key separation, malware scan, signed access, logging and deletion propagation |

No provider is production-approved merely by appearing in this register. The owning story must attach evidence and a fallback/degradation plan.

## Security logging

Required events:

- Authentication, MFA, refresh rotation/reuse, revoke, lockout and device/session lifecycle.
- Role/permission/policy changes and denied privileged attempts.
- KYC/bank/media access and decisions without raw sensitive payloads.
- Payment/webhook/refund/reconciliation state and signature result without secrets.
- Wallet/settlement/tax adjustments, approvals and before/after references.
- Export creation/download/expiry, configuration/policy publication and break-glass use.
- Migration intake/transform/import/reject/reconciliation access and checksums.

Every event includes UTC time, actor/service, target reference, action/outcome, tenant/country, correlation/trace, auth/session assurance and reason/change/incident reference when required. Phones, email, precise coordinates, KYC content, bank data, tokens, OTPs, private messages and provider secrets are prohibited in general logs.

## Incident classes and response

| Class | Examples | Initial response target |
| --- | --- | --- |
| `SEC-P1` | Active account takeover campaign, key/provider compromise, financial manipulation, restricted-data exfiltration | Immediate paging; contain/revoke/isolate; executive/legal/privacy/finance escalation |
| `SEC-P2` | Confirmed authorization bypass with limited exposure, persistent abuse, exploitable critical dependency | Page security/service owner; mitigate or disable affected feature |
| `SEC-P3` | Suspicious event, low-impact misconfiguration, blocked exploit attempt | Ticket with evidence and bounded remediation SLA |

The response runbook requires: preserve immutable logs/snapshots/checksums; establish incident commander; stop further harm; rotate/revoke; identify affected subjects/transactions; meet legally reviewed notification duties; reconcile financial/data correctness; recover through signed changes; document timeline/root cause; add regression/detection tests.

## Phase 2 security gates

- Threat-model controls become acceptance tests in `TEST_STRATEGY.md`.
- Identity, BFF and admin shell ship with MFA-ready assurance claims, CSRF/session protections and role-denial suites.
- Every service template includes secure headers, payload/size limits, structured redacted logs, dependency timeouts, health/readiness and audit hooks.
- CI requires SAST, dependency/license, secret, IaC, container and SBOM/provenance checks.
- Staging uses synthetic data and provider sandboxes; production accounts/keys are not prerequisites for development.
- A provider failure cannot silently bypass authorization, signature, ledger or state-machine rules.
