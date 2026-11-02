# Participant rework and regional access

This increment implements correction paths for rejected vendor/rider onboarding and explicit regional participant permissions. iOS remains deferred. External provider, device and release evidence remains outside the verified local scope.

## Implemented behavior

- Vendors submit corrections through `POST /v1/vendor/application/resubmissions`. The owning transaction requires a current vendor identity, participation role and source session, the rejected application's current revision, an idempotency key, and a distinct business-registration/owner-identity document set. Bounded legacy document names remain supported. Current owned private evidence is checked and claimed atomically. Rework clears previous bank, visit, zone and verification state and returns to `DOCUMENTS_SUBMITTED`.
- Riders receive a structured rejection reason and an explicit server-owned rework flag. Only an onboarding rejection enables `RESUBMIT_APPLICATION`. An operational suspension stays ineligible. `POST /v1/rider/application/resubmissions` rechecks live identity/session authority and evidence, records correction history and audit, and returns to `KYC_REVIEW` with bank verification pending.
- Both paths preserve old evidence claims and append submission history. Audit failures roll back revisions, replacement claims, history and replay receipts. A replay needs current session authority and the original revision/payload fingerprint.
- Normal Flutter APIs and forms expose these corrections. Rider feedback is visible in the normal duty/onboarding screen. Providers must still be configured. Acknowledged corrections survive a failed subsequent status read, and disposed rider controllers discard late correction responses. Rework form progress survives a normal profile refresh.
- Country administrators can bind or unbind real participants to existing organization territories using the canonical English/Tamil admin. Bindings require fresh MFA, CSRF, expected revision and a reason, with append-only history and mandatory audit. Regional staff cannot change their own bindings.
- Regional lists contain only explicitly bound participants under active territory ancestors and current inherited `resource.read`. Rejection independently requires `resource.command`. Owning reads, commands and replay checks hold current directory/session and organization locks, serializing with revocation. Final approvals, verification transitions, bank verification and financial authority retain country administration checks.

## Verification checkpoint

The complete Linux backend rework run at `.local/completion/verification/run-jD9Wmu/report.json` passed quality, race/controlled-load/build checks, all 27 PostgreSQL targets, fresh/rollback checks for 120 migrations, dependency tests and the vulnerability scan. The later initial-onboarding source hardening passed a second complete source-matched run at `.local/completion/verification/run-p47HFY/report.json` with the same stages, 27 targets and 120 migrations. Its scan found zero reachable/imported vulnerabilities and three unused required-module notices. Both receipts identify their exact source images; later backend edits require new evidence.

The focused owning PostgreSQL run at `.local/completion/participant-rework-focused/run-tREKS5/report.json` also passed both owning suites and package tests with unchanged source overlays. Cases include retained evidence, rollback, stale revisions, restart/replay, revoked participant sessions, read-only regional denial, inherited scope, queued ancestor revocation, binding audit rollback and denied replay after unbinding.

The canonical admin passes type checking, lint, all 129 tests and its production build. Final coverage is 89.08% statements, 83.51% branches, 84.87% functions and 93.26% lines. Flutter passes 564 tests and all 12 analyses on 597 matching inputs; seven focused rework tests are included. These receipts predate later backend-only initial-onboarding hardening.

The deployed local backend passed readiness after migration. The real HTTPS admin, private S3/ClamAV, organization bindings and normal Flutter HTTP adapters passed 71 local checks, including retry after an acknowledged resubmission response was lost. Docker's Linux engine stopped during the first persistent API rerun on 28 October. WSL shutdown restored it without deleting persistent data; the stack rebuilt and passed all seven readiness checks and the existing 264 persistent API regressions. The newer backend-only initial-onboarding hardening passed its own isolated complete gate.

Customer, vendor and rider Android development/debug APKs compile against the current 597-input mobile source. Their package IDs, artifact hashes and unchanged source inventory are in `docs/completion/phase-2/evidence/android-participant-rework-regional-build-matrix.json`. This is compilation evidence, not a physical-device or provider acceptance result.

## Remaining acceptance

- Complete real provider/device journeys when a test provider and device are configured.
- Finish the broader provider/onboarding workflow and cross-service portable export, erasure, retention/legal holds and terminal deletion/recovery. These are separate open Phase 2 requirements. This increment does not establish full Phase 2 acceptance or production readiness.

Windows command startup became unusually slow during the final local verification work. Current runtime and verification failures must remain visible; no phase may advance based on a stalled or failed check.
