# Portable identity export

`GET /v1/me/data-export` defaults to schema version 1 with an explicit coverage
manifest. Profile, roles, all own sessions and all consent decisions are read
from one committed PostgreSQL REPEATABLE READ snapshot. Withdrawals and earlier
grants are included. Decisions in other countries belong to the same account;
older evidence with no recorded country is exported as `country: null`. Only
current-country evidence contributes to the existing `consents` summary.

The snapshot checks the current session inside the transaction. The service
checks it again before returning, discarding a result assembled during account
disablement or session revocation. Tenant/account/session/country come from
trusted authentication. Query fields cannot select a different account.
Responses remain `Cache-Control: no-store`.

`GET /v1/me/data-export?schema=2` adds the account's contact snapshot metadata
to the same committed identity snapshot. Each row contains only country,
revision, contact count, optional policy version and expiry. Withdrawn contact
snapshots remain visible as count-zero revision tombstones. The query uses the
current authenticated identity and tenant; it cannot select another owner.
Phone numbers, phone HMACs, key IDs and request fingerprints are never selected
or returned. The Android document parser accepts versions 1 through 4, validates
the metadata, and allowlists the safe fields before saving. Unknown or repeated
schema selectors are rejected. The default version 1 response and existing
mobile clients keep their previous contract.

`GET /v1/me/data-export?schema=3` also adds the owner's deletion request
history: request ID, status, reason, request time and effective time. The
tenant-scoped query runs within the same committed snapshot and does not select
another account's requests. Provider credentials and owner IDs are not repeated
in these rows. The Android parser validates each record and allowlists only
those five fields before saving. Schemas 1 and 2 retain their prior shape.

The normal mobile adapter sends the version through `ApiRequest.query` with
the query-free `/v1/me/data-export` path. A transport regression and actual
local Flutter HTTP verify the request plus empty, scheduled and cancelled
deletion history. The synthetic request is cancelled during test cleanup.

SQL selects explicit fields. Neither credential digests, raw device identifiers,
provider subject/binding credentials, access tokens nor refresh tokens appear
in the export. The mobile document parser projects supported fields again;
unknown root or nested fields are excluded from the saved file.

The synchronous snapshot permits up to 10,000 sessions, 10,000 consent
decisions, 10,000 contact snapshot rows and 10,000 deletion requests, with a two-MiB HTTP budget including
the trailing newline. An
oversized snapshot returns `409 EXPORT_REQUIRES_PAGINATION`; it does not produce
a truncated export. A resumable export for larger accounts remains required.

All three normal Android applications use the shared storage plugin and system
document picker. The user explicitly chooses the destination. File names have
a bounded UTC timestamp and contain no account identifiers. The application
keeps no plaintext temporary file, asks for no general storage permission and
does not persist the destination URI. Cancellation creates no data write. An
account retired while the picker is open cannot write its previous response.
The controller accepts only its own latest export response, prevents duplicate
actions and clears its transfer bytes after the attempt. The native writer
confirms a completed stream or reports a retryable destination failure; cleanup
of a failed newly created document depends on the selected provider. A saved
copy remains under the user's control at the chosen location.

Native plugin state belongs to one Flutter engine. Activity interruption
cancels an open picker; another attempt must select a destination again. JSON,
destination URIs, object references and provider exception causes are not
logged. The implementation follows Android's
[Storage Access Framework](https://developer.android.com/training/data-storage/shared/documents-files)
and Flutter's [plugin lifecycle guidance](https://docs.flutter.dev/packages-and-plugins/developing-packages).

The schema 3 file's coverage currently excludes orders/payments, wallets,
applications, delivery/location, bookings, social/messages, media and security
audit. Cross-owner aggregation, resumable export, configured retention/holds,
erasure execution and complete deletion/recovery remain Phase 2 work. This
identity export does not establish complete erasure or full-product portability.
Local/native evidence is recorded separately in the completion register; iOS
remains deferred.

## Schema 4 owner onboarding metadata

`?schema=4` is additive to schema 3. It includes the current vendor application
and rider profile for the authenticated account in every country under its
tenant. The identity runtime can execute only narrow supply/fulfillment
`SECURITY DEFINER` functions; it cannot select the owner tables. Each owner
function validates the exact current identity session and derives the account
from that session. Historical onboarding remains exportable if the VENDOR or
RIDER role was later removed. The response is assembled in one read-only
repeatable-read transaction and rejected if its source session is revoked
before the service returns it.

This version includes business/contact name, business type, rider name,
vehicle and zone metadata, status, revision and timestamps. Bank details,
masked phone, OCR, private evidence, reviewer details, field visits, historical
submissions and live delivery/location are deliberately outside this version.
The coverage manifest names these exclusions. Current records are limited to
10,000 rows per section and the same two-MiB total budget. An oversized file
fails; it is never silently truncated. Order/payment, wallet, booking, social,
media and security audit exports remain open.

## Deletion completion barrier

The existing recovery period and due-session block remain in place. Identity
migration 14 adds a database completion barrier: a deletion cannot become
`COMPLETED` until its effective time and each configured owner retention
interval have passed, every registered service owner has an explicitly
configured tenant/country retention policy and current erasure or
no-data receipt, and every legal hold is explicitly released. No retention
period is seeded. The identity application runtime has no write privilege on
the policy, hold or receipt tables. A bounded identity worker creates one
durable task per owner for each due request, then recomputes its state from the
policy, retention interval, legal holds and independent owner receipts. It uses
row locking with `SKIP LOCKED`, can resume after a crash, and leaves task
revisions untouched when the state has not changed. `READY` is a handoff state;
`ACKNOWLEDGED` only reflects an independently stored receipt. The coordinator
never writes a receipt or changes a request to `COMPLETED`.

The complete owner erasure workers and terminal attestation writer are not
implemented yet; requests remain `SCHEDULED` after their recovery period.
This is a fail-closed orchestration foundation, not proof of cross-service
erasure or production readiness.

## Opt-in owner onboarding scrub

Supply and fulfillment now expose owner-role database functions that can scrub
bounded onboarding data only after a due request, explicit tenant/country/owner
retention policy, elapsed interval, READY owner task and no unresolved legal
hold. The request ID determines tenant and subject; every country for that
account is handled in one transaction. The functions lock the request and
onboarding rows, reject more than 1,000 affected rows per section, record an
owner audit row, and write the matching `RETAINED` receipt atomically. A retry
returns the existing audit evidence without another mutation. The identity
runtime can execute these functions but cannot update the owner tables
directly. No HTTP route or background worker invokes them; an explicit
operational integration is required before production use.

Supply clears applicant contact and bank references, submission contact and
document metadata, and vendor document rows. Fulfillment clears rider profile
name, masked phone, vehicle number, bank reference, review text and document
metadata, plus submission document metadata. Both leave business, catalog,
order, delivery, location, financial, audit and private media owner records as
applicable. Their receipt is `RETAINED`, so the database completion barrier
continues to reject `COMPLETED`. This is partial erasure with specific evidence;
whole-account deletion still requires the remaining owner implementations and
legal review of retention rules.

## Governed retention and legal holds

Identity migrations 17–18 add immutable policy proposals and a published
tenant/country/owner policy. No default interval is supplied. A current
country administrator with a live operator session and fresh MFA proposes a
policy version; a different current administrator publishes that exact version
at the expected revision. Publication is audited in the same transaction and
rechecks the current operator directory/session under locks. A changed policy
invalidates older owner receipts by version. The older tenant-only policy rows
remain historical and are never accepted by the new completion or scrub gate.

The same governed path places a legal hold immediately on a scheduled deletion
and permits only a different, freshly authenticated country administrator to
release it with an audited reason and expected revision. A hold placement
locks the deletion request, serializing with due owner scrubs and completion.
Existing holds without an attested placer cannot be released through the new
command and require an explicit migration/review. New deletion requests capture
their country from the locked source session. Historical requests without a
trustworthy country fail closed.

The four commands are currently repository/database capabilities. They are not
exposed through an admin BFF or UI, and no owner scrub is run automatically.
Migration 19 adds read-only, live-authorized admin functions for the 100 most
recent scoped proposals, 100 active holds and 100 scheduled deletion requests,
plus exact proposal/request lookup. The admin runtime can execute these
functions but has no direct table read or write permission through this grant.
The bounded lists need pagination before they can serve a large review queue.
The account-wide request may have data in multiple countries; complete
jurisdiction mapping and every owner-specific retention/release workflow are
still needed before terminal `COMPLETED` can represent whole-account erasure.
