# Participant review administration

Locally verified on 27 October 2026. Phase 2 remains partial; Phase 3 has not started. iOS and real provider/device evidence remain deferred.

## Implemented behavior

The canonical admin has a Participant onboarding workspace for current country/super administrators. Vendor/rider lists support literal name search, status filters and stable UUID pagination, with at most fifty summaries per response. Responses contain application/participant identifiers, revision, name/category, workflow status, document count, bank-verification status and update time. Private asset IDs, extracted document data, phone details and bank references are excluded. The page labels and feedback support English/Tamil.

The admin can record a reasoned rejection for an eligible current application. Vendor rejection follows the existing OCR/KYC/bank-review transitions; rider rejection moves an application awaiting KYC review to SUSPENDED. An already approved participant cannot be suspended through this onboarding action. These commands carry current CSRF, fresh MFA, original expected revision and an idempotency key derived by the normal UI. A lost response freezes the original decision and offers the same-command retry. Successful decisions remain acknowledged when subsequent list refresh fails; failed/revoked reads hide cached applicants. Account/country changes retire the visible pending form.

The owning vendor and rider services lock the current active administrator directory row and exact source session through reads and privileged application commits/replays. The database verifies tenant, selected/allowed country, directory revision, source identity, current country-administrator roles, expiry and fresh MFA. A final check uses the database clock. Application writes use row locks, revisions, atomic decision evidence and idempotency. Vendor privileged fingerprints also bind the original expected revision. Same-identifier self-review is denied; broader separation of a person's identity across participation/operator accounts remains part of complete participant governance.

The bounded authority function grants execution to supply/fulfillment runtime roles without private admin-table read grants. Caller role hints cannot grant current country authority. Regional participant access remains denied until an owning participant-to-territory binding exists. Existing separately scoped fulfillment territory/field workflows retain their own authority model.

Rejected-rider command responses and subsequent profile reads now return the required empty allowed-actions array. The normal Flutter vendor and rider adapters read recorded decisions correctly; the vendor also reads the recorded rejection reason from its timeline.

## Acceptance evidence

| Gate | Result |
| --- | --- |
| Complete backend base | PASS: quality/race/controlled load/build, all 27 PostgreSQL targets, 114 migrations from zero and through rollback, dependency integration/security scan. [Receipt](../completion/phase-2/evidence/backend-participant-review-full.json) |
| Current backend source | All 693 inputs match the base plus exactly two final rider readback files. The source overlay reruns complete quality/race/load/build and the affected owning PostgreSQL suite. [Supplement](../completion/phase-2/evidence/backend-participant-readback-contract-fix.json), [current source](../completion/phase-2/evidence/backend-participant-review-current-source.json) |
| Admin | PASS: 126 tests, existing coverage thresholds, type/lint/API generation and production build. [Receipt](../completion/phase-2/evidence/admin-participant-review-gates.json) |
| Flutter workspace | PASS: 557 tests, all 12 analyses, generation/format/security/flavor/CI checks, 596 unchanged inputs. [Receipt](../completion/phase-2/evidence/flutter-participant-review-workspace.json) |
| Android compilation | PASS: customer/vendor/rider development/debug APKs, verified package IDs/checksums and unchanged sources. [Receipt](../completion/phase-2/evidence/android-participant-review-compilation.json) |
| Actual local journey | PASS: 40 checks across normal enrollment, private S3 upload/actual ClamAV readiness, current admin queues, actual HTTPS rejection after injected response loss, same-key replay, normal Flutter decision readback, stale revisions, logout/revocation, desktop/mobile accessibility and mobile/large-root-font text/control bounds. [Receipt](../completion/phase-2/evidence/local-participant-review-admin-flutter-journey.json) |
| Existing persistent API regressions | PASS: all 264 checks, no financial seed/reset. [Receipt](../completion/phase-2/evidence/local-participant-review-api-regression.json) |

The PostgreSQL cases use actual owning runtime roles and exercise denied private-table grants, missing/foreign/stale source scope, queued directory suspension, old-session denial after reactivation, audit-failure rollback and restart replay. The actual browser/Flutter acceptance uses uniquely owned synthetic local identities/MFA and small synthetic document images. Vendor pending-review state is prepared through the real owning transition with its current synthetic operator source; it does not establish a complete normal admin review/approval workflow. No external KYC or bank approval is asserted. Retained synthetic applications/document evidence remains subject to the existing retention controls; test-session cleanup does not pretend to erase retained evidence.

The first persistent journey caught the suspended-rider profile array error and failed. Its failed receipt is preserved. A source-matched owning fix and fresh complete journey establish the final pass; no client parser was weakened. Earlier Go formatting, fixture-composition, TypeScript/lint, asynchronous-query and automatic-worker timing failures remain recorded in the work log.

## Remaining Phase 2 work

- Complete participant status administration and current regional participant bindings.
- Audited private evidence review, governed reason references, configured OCR/KYC approvals, field/bank workflows and complete rejection correction/resubmission.
- Governed legacy import and remaining delegated owning commands/resource scope.
- Cross-owner portable export, resumable large-account workflows, configurable retention/legal holds and terminal erasure.
- Configured Firebase/provider/device and release evidence, explicitly separate from these local checks.

The complete Phase 2 exit and production-readiness flags stay false. These remaining items require implementation and verification; no additional user decision is currently requested.
