# Territory authorization, rider documents and durable privacy choices

Local continuation, 25 October 2026. Phase 2 remains partial and iOS remains deferred. The current acceptance receipts are linked from `docs/completion/phase-2/STATUS.md`.

## Fulfillment authority

The canonical admin can link a real delivery territory to an active organization territory. The BFF requires a platform administrator, fresh MFA, CSRF, a reason and the current binding revision. It derives tenant/country from the authenticated session. The fulfillment owner exposes a non-personal read view for target validation; administrators receive no business-table write permission. Binding changes and their append-only history commit with the organization audit event. Delegated staff cannot claim or reassign resources.

The fulfillment runtime receives only EXECUTE on a bounded organization-owned authorization function. It receives no staff, invitation or organization table SELECT privilege. Current active assignments, capabilities, resource binding and every active ancestor determine delegated territory access. An assignment can provide read without command authority. Existing franchise ownership and global operations roles remain separate server-derived grants.

Territory lists and regional dashboards use the resulting visible territories. Live rider counts/locations suppress postal codes shared with unseen territories; a postal code does not establish ownership. Delegated dashboards omit the country-wide pending-payout amount. Other-person attendance remains restricted to global operations administrators until an unambiguous per-entry territory owner is available.

Field check-in requires MFA, `resource.command`, an active binding/assignment and the geofence. It holds the shared organization advisory lock through commit, including idempotency readback. Assignment and binding mutations hold the exclusive lock. The field transaction uses READ COMMITTED with explicit row/command locks: a SERIALIZABLE snapshot established while waiting for revocation retained the old grant in the first PostgreSQL race test. The corrected test queues a real command behind an uncommitted revocation and requires denial after commit. Revocation also denies replay of an older successful command.

Direct customer/restaurant rider-location reads require an active assigned/picked-up order relationship. Only the two original order parties qualify; old riders retained in chat history do not. Completion ends customer tracking. Unrelated reads return the same not-found response as an absent location.

This increment does not establish scoped master/audience administration, normal configured operator identity/BFF journeys for delegated fulfillment access, or delegated commands in every business domain. Unimplemented domain access must not be inferred from a territory link.

## Native rider onboarding

Normal rider startup creates an account-bound private uploader using the deployment's explicit upload origin. Sign-out or account retirement disposes it. The document adapter captures identity for bicycles and identity, driver licence, registration and insurance for motor vehicles. The existing immutable upload, byte/type/digest validation, private object access and malware scan must return READY before a reference is accepted. Uploads never infer OCR, KYC approval or bank verification.

The rider screen exposes a separate document step when the bank adapter is unavailable. Submission remains disabled. This screen retains only opaque references in memory; unsubmitted progress is explicitly described as unsaved after leaving. Cancellation, replacement and late capture clean up only this attempt's unsubmitted references. Captured bytes are cleared, the exact temporary camera file is removed, and recovered camera results after process death are never attributed to the current account. Once submission begins, ambiguous results cannot trigger deletion of potentially attached evidence.

Configured bank tokenization/OCR and a privacy-processed POD upload adapter remain pending. Provider availability must not be fabricated to allow approval. Durable onboarding drafts, server evidence claims/retention and complete camera/process-death journeys remain separate work.

## Privacy choices

All three normal apps attach an account-scoped consent controller to Profile → account privacy. Users can withdraw existing choices; a new grant belongs in the relevant feature's current-policy explanation. Device permissions remain separate.

The secure-storage journal hashes the unambiguous tenant/country/account tuple for its key prefix and keeps one denial per purpose with no expiry. Android uses a dedicated full storage namespace and explicitly disables the SDK's default reset-on-error behavior; other session/cache resets cannot erase this journal's data, configuration or keys. A requested change immediately denies local use. Its journal write precedes the network call and proceeds independently of older blocked network requests. A failed grant never becomes an automatic grant after restart. Reconnection retries withdrawals only. Confirmed withdrawals retain a denial so stale server lists cannot restore access. An explicit acknowledged grant removes only its matching intent token; compare-and-confirm prevents an old controller from removing a newer denial.

Malformed or unavailable storage fails closed and is never silently deleted as an expired cache. A failed storage write is an unconfirmed failure, not a claim that a choice survived process death. Retired controllers discard late responses. Each app's OS storage is isolated: this is a device/app denial journal, not proof of cross-device consent propagation or a cross-owner erasure system. Reinstallation/storage removal, backend outbox ordering, contacts, portable export, retention/legal holds and deletion across service owners still require implementation/proof.

The SDK configuration guard and simulated corrupt-record tests are separate from real Android storage/readback journeys. Forced device KeyStore invalidation and involuntary process-death recovery have not been established by these checks.

The normal rider runtime also binds this controller to duty tracking. A current published `LOCATION_DELIVERY` policy and an explicit matching grant are required before the device location provider starts. Missing policy or failed consent storage fails closed while profile/onboarding inspection remains available. The duty view explains location use and records the current policy before enabling duty. Withdrawal stops the subscription immediately, discards queued callbacks, blocks manual sharing and prevents late device-permission completion from starting tracking. A request already sent before withdrawal cannot be recalled; server-side duty/location consent enforcement and cross-device propagation remain open. The sample-coordinate manual share action was removed. Customer serviceability responses also recheck consent before retaining success/error location state, so late responses cannot repopulate a withdrawn location cache.

## Verification boundaries

Keep unsuccessful runs in the work log. The first full backend run exceeded the unchanged 400 ms controlled load budget; a later PostgreSQL race test exposed the snapshot issue above. Neither is a pass. Acceptance requires a current source-matched backend run, admin quality/browser checks, the Flutter workspace gate and normal Android journeys. Debug artifacts do not establish release signing, Android release performance, actual Firebase/push, KYC/bank provider results or production readiness.
