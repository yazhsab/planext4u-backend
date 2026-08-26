# Data classification and retention draft v0.1

This is an engineering baseline, not legal advice. Legal, Finance, HR/Operations and Security must approve retention periods before Phase 2 storage policies are frozen.

| Class | Examples | Storage/access baseline | Retention proposal |
| --- | --- | --- | --- |
| Restricted secrets | Password hashes, refresh-token hashes, provider secrets, signing keys, webhook secrets | Secrets Manager/KMS or hardened identity store; never logs/analytics/client | Secret-specific rotation; revoke immediately on compromise; no historical plaintext |
| Restricted identity/KYC | Aadhaar/PAN/GST documents, face/IDV evidence, field-verification artefacts | Private S3, malware scan, short-lived signed access, purpose-bound roles, full access audit | Regulatory/business need only; exact period legal decision; deletion/hold workflow required |
| Restricted financial | Bank account, payment references/tokens, refunds, wallet/settlement ledgers, tax documents | Encrypted service-owned data, masked UI, fresh auth, four-eyes controls | Statutory finance/tax retention decision; immutable ledger plus correction entries |
| Confidential PII | Name, phone, email, DOB, addresses, device identifiers, support cases | Field encryption where justified, least privilege, purpose/consent and redacted non-prod data | Active account plus approved post-deletion legal window; account deletion workflow <= 30 days |
| Confidential location | Home/service address, rider/service live location, emergency coordinates, geo check-ins | Explicit consent, short TTL for raw pings, access by active workflow only, no advertising reuse without consent | Raw live pings short-lived (TBD); derived task evidence/report retention separately approved |
| Confidential communications | DMs, voice notes, order chat, support messages and call metadata | Participant/policy access, block/report controls, encryption, moderation/legal-hold separation | Order chat closes 1 h after delivery; message/content retention and deletion legal decision |
| Internal commercial | Prices, commission rules, campaigns, inventory, analytics, vendor performance | Role/tenant scoped; change history and rule versions | Business need plus audit window; superseded rules retained for financial explainability |
| Internal security/audit | Login/session events, admin before/after audit, risk signals, moderation decisions | Append-only/tamper-evident store, restricted export, correlation IDs | Document baseline: 3 months hot and 12 months cold; legal/security approval required |
| Public content | Approved catalog, vendor public profile, public social/listings/reviews | CDN/search cache; origin remains authoritative; moderation/takedown controls | Until deleted/expired/moderated; cache purge and search tombstone required |
| Ephemeral | OTPs, slot/cart holds, presence, story progress, idempotency locks | Redis/TTL or short-lived database row; never analytics source of truth | OTP/hold/presence TTL by workflow; stories expire at 24 h unless saved as highlights |

## Required controls

- Data inventory links every field to owner service, classification, purpose, lawful basis/consent, residency, retention, deletion and downstream consumers.
- Non-production environments use synthetic or irreversibly masked data only.
- Backups and analytics projections participate in deletion/retention policies; deletion is not limited to primary tables.
- Legal hold is explicit, scoped and audited.
- Media deletion invalidates signed access, origin objects, CDN cache, thumbnails/transcodes and search/feed projections.
- Exports are time-limited, encrypted, watermarked/identified where appropriate and audited.
- No precise location, KYC, bank, OTP, token or private message content enters general logs or metrics.

## Decisions required

1. DPDP data-fiduciary/processor roles and consent purposes by feature.
2. KYC, GST, invoice, settlement and payment statutory retention.
3. Social/message deletion, moderation evidence and appeal/legal-hold periods.
4. Rider/field-officer location and attendance retention with worker-policy review.
5. Emergency request/medical-adjacent information handling and responder access.
6. Data residency and future-country separation.
