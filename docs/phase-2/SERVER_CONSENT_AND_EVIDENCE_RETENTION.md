# Server consent, private evidence and saved rider forms

Local continuation, 25 October 2026. Phase 2 remains open. Android, backend and canonical admin are the implementation target; iOS stays deferred. External provider/device and release acceptance remain separate.

## Delivery consent and concurrent requests

The identity owner records new consent evidence with the authenticated country. Historical evidence with unknown country stays in the audit store and cannot authorize new location processing. Current consent reads return the authenticated country's decisions.

A grant requires `expected_version`, the last observed version for that purpose and country, or zero when no decision exists. The owner compares it atomically while holding the identity lock. A stale grant returns `409 CONSENT_VERSION_CONFLICT`; the user must refresh and explicitly choose again. Withdrawals do not depend on this precondition. The Android controller captures the observed version at the user's intent, before queued network work.

Fulfillment asks bounded security-definer functions for the published delivery-policy version and the current identity decision. It receives no identity/configuration table access. Missing policy/evidence, a withdrawn or obsolete-policy grant, a disabled/restricted account or a foreign scope denies processing. Start-duty commands and their replays, online locations and their replays, offline location recovery and direct live tracking enforce the decision. Regional live-location queries also filter withdrawn riders.

Location transactions use READ COMMITTED and hold shared policy/identity advisory locks through commit. Consent writes and configuration publication take the corresponding exclusive locks. A withdrawal cannot acknowledge while an earlier location command still holds its processing lock. A command queued behind the withdrawal reads its committed decision. No granted decision is cached in fulfillment. Existing active duties/tasks are retained for operational recovery when sharing stops.

The database suite covers actual restricted roles, replay, policy changes, withdrawal, stale grants, foreign scope and a location command queued behind an uncommitted withdrawal. Native grant/withdrawal UI and journal tests remain separate from Firebase, OS permission and physical-device evidence.

## Evidence ownership and deletion

Vendor document submission and rider registration claim immutable READY identity assets within the same PostgreSQL transaction as their application/replay state. The media owner validates tenant, country, owner, purpose, lifecycle, bounded unique asset IDs and the consumer type. A business rollback leaves no retained orphan. Sorted asset row locks serialize application claims with deletion.

The transaction runtime can execute its domain's bounded claim function but cannot read media tables or directly modify claim history. The media runtime can read claims and cannot insert, update, delete or truncate them. A database trigger protects claimed assets from accidental direct deletion/expiry. Bounded owner-service reference functions also protect current applications created before claims were introduced, without exposing their application tables to media.

An owner's asset DELETE returns `409 MEDIA_EVIDENCE_RETAINED` while application evidence is retained. Unsubmitted documents can be deleted. The media owner commits a tombstone before calling object storage, blocking new reads, URL issuance and claims even if storage cleanup fails. The same scoped DELETE retries cleanup; a different tenant/country/account cannot retry it. Previously issued private URLs have their existing two-minute lifetime and may remain usable during a storage outage until expiry or successful object cleanup.

Claims deliberately have no client-controlled expiry/release command. A complete configured domain retention, legal-hold and cross-owner erasure/release process is still required. These changes protect accepted evidence; they do not establish a full erasure implementation or statutory retention policy.

## Saved rider forms and cache recovery

Normal authenticated rider startup binds an encrypted form-progress store to tenant/country/identity. It retains bounded name, vehicle and postal-code fields for seven days, serializes operations across instances, resumes in the ordinary registration screen and provides a delete-saved-form action. The store excludes document bytes/references, bank data and consent grants. Documents must be captured again after leaving the screen. Successful server registration clears saved fields; a failed local clear does not misreport an accepted registration and is retried on profile refresh.

The form's read/write failures are visible. Late reads cannot populate a different controller/account. Store and screen tests cover restart, expiry, account/country/tenant isolation, queued clear, malformed/restricted fields and renewed explicit consent. Native local verification separately checks the actual encrypted store through normal startup.

A corrupt cache encryption key previously called a second serialized key operation from inside the first, deadlocking recovery. Recovery now removes the corrupt key within the existing operation. Invalid-base64 and wrong-length keys both have bounded completion and subsequent-operation regressions.

## Remaining acceptance

Account deactivation now takes the exclusive identity-processing lock before the identity row, matching consent writes. Existing duty/location transactions hold the shared counterpart through commit. The deactivation response therefore waits for earlier processing, and later delivery checks observe the disabled identity. An actual PostgreSQL HTTP regression reproduced the earlier premature 204 response and passes with the fix under the race detector. The affected identity package also passes race tests and vet. This adds processing coordination; complete deletion/erasure and retention remain open.

Full configured OCR/KYC/bank/POD adapters, evidence retention/hold release, device permissions/biometrics/contact matching, involuntary process death, published-introduction device proof, all-domain delegated operator workflows and provider/push/release acceptance remain tracked in the Phase 2 register. No production-ready or phase-exit claim follows from this increment.
