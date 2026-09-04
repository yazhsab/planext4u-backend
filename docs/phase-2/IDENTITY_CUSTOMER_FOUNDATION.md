# Identity and customer foundation

## Boundary and contracts

`cmd/identity` is the runnable identity/customer-profile service. Its reviewed
OpenAPI 3.1 authority is `api/openapi/identity.openapi.json`, with a checked-in
v1 compatibility baseline and synthetic authentication fixture. The service
owns provider bindings, platform identities, current coarse roles, device
sessions, profile concurrency and consent evidence. Provider assertions never
grant a platform role; a new identity receives only `CUSTOMER`.

The edge exposes only three exact anonymous commands: provider exchange,
refresh and revoke. All are still rate/body/time bounded. Profile, session and
consent routes require gateway-derived subject/session/tenant/country headers.
The identity service re-loads current session and role state and rejects any
cross-subject, cross-tenant, cross-country or cross-owner mismatch.

## Token and session security

- Approved provider assertions are verified through a bounded HTTP adapter
  with an allow-list, deadline, 64 KiB response cap and redirect refusal.
- Access tokens are RS256, use a configured `kid`, live at most 15 minutes and
  contain only subject, session, tenant, country, device, current coarse roles,
  auth time and assurance metadata required by the gateway.
- Refresh tokens contain 256 random bits and are returned only once. PostgreSQL
  stores an HMAC-SHA256 digest under a mounted secret key, never plaintext.
- Rotation locks the digest and session in one transaction. The old digest is
  retained as consumed evidence. Any reuse revokes the complete session family,
  including a token returned by a concurrent winning request.
- Logout/revoke is idempotent and deliberately cannot be used as a token oracle.
  Session expiry is absolute rather than sliding.
- Signing keys, refresh-HMAC keys and database URLs are mounted files. Staging
  and production require HTTPS provider/issuer URLs and PostgreSQL
  `sslmode=verify-full`.

## Profile, role and consent policy

Profiles support the approved English, Tamil, Hindi, Telugu, Kannada,
Malayalam, Marathi, Bengali and Gujarati locale codes, validated IANA time
zones, Unicode display names and strong ETag/`If-Match` version checks. Session
responses expose only an HMAC-derived device reference. Raw provider subjects,
provider tokens and device identifiers never enter API responses or general
logs.

Consent changes append immutable evidence with purpose, decision, policy
version, timestamp and sequence version. The current response is a projection
of the latest evidence per purpose. Identity-local security events are written
in the same PostgreSQL transaction as authentication, refresh/reuse, revoke,
profile and consent changes; the privileged cross-service audit view remains
owned by BE-P2-010.

## Persistence and scale controls

The service uses a bounded `pgxpool` and an identity-owned PostgreSQL schema.
Uniqueness protects provider bindings, one active refresh digest per session,
and consent evidence versions. Indexes cover identity session listing, active
expiry, current consent and security-event lookup. The memory repository is
explicitly a deterministic development/test adapter; the runnable service uses
only PostgreSQL.

## BE-AUTH-001 and BE-AUTHZ-001 evidence

Unit, race, HTTP and real-PostgreSQL suites cover provider rejection/outage,
provider-role distrust, access-token interoperability with the gateway, refresh
rotation, expiry, logout, concurrent reuse/family revocation, signer failure,
malformed/oversized input, correlation, role denial, subject/tenant/country
tampering, session ownership, profile conflicts, Tamil values, consent
grant/withdrawal and audit redaction.

The tagged PostgreSQL suite combines unit and integration coverage and enforces
the Phase 1 test-strategy floor of 80% statement coverage for this package.

Run:

```sh
make verify
make local-up
make test-identity-integration
make local-down
```
