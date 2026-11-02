# Deterministic local platform

This Compose project provides disposable local PostgreSQL, Redis, NATS
JetStream, MinIO-compatible object storage, and deterministic identity,
notification and geocoding provider doubles.

All credentials and identities are synthetic local fixtures. They must never be
reused in dev, staging or production.

```sh
make local-up
make local-status
make test-integration
make local-down
```

Service-owned schemas and the migration ledger can be installed with a
file-mounted local connection secret:

```sh
umask 077
mkdir -p .local/migrations
echo 'postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable' > .local/migrations/database.url
make migrate-local
make test-migrations-integration
```

Production and staging create a distinct LOGIN role per service and grant only
its matching `planext4u_<service>_runtime` group. See
`docs/phase-2/SERVICE_OWNED_MIGRATIONS.md`; local passwords below are synthetic
and must never be promoted.

`make local-up` builds and waits for every dependency to become healthy. The
default host endpoints are:

| Dependency | Endpoint |
| --- | --- |
| PostgreSQL | `postgres://planext4u_local:local-only-password@localhost:54320/planext4u_local` |
| Redis | `redis://localhost:63790` |
| NATS | `nats://localhost:42220` |
| NATS monitor | `http://localhost:48222` |
| Object API | `http://localhost:9000` |
| Object console | `http://localhost:9001` |
| Synthetic providers | `http://localhost:18081` |

The identity service uses explicit secret files and never commits local keys.
The completion launcher starts the persistent local APIs, media and admin BFF:

```sh
node scripts/completion/stack.mjs up
node scripts/completion/stack.mjs verify
node scripts/completion/smoke.mjs
node scripts/completion/verify-scanner.mjs
```

The media owner listens on loopback `8087` and is routed through gateway `8082`.
It uses its restricted PostgreSQL login and the private
`planext4u-phase2-private` bucket. Explicit local initialization creates only an
absent bucket; it never sets a public policy or resets objects/financial data.
MinIO root credentials here are synthetic local fixtures; deployment must use
an independently provisioned bucket-scoped runtime principal.

`clamav.compose.yaml` runs the pinned official engine without a network
interface or host listener. A separate updater validates official signatures.
The engine and media share only a private Unix-socket volume; the media
bootstrap retains explicitly configured supplementary group `101` after
dropping root. Other API services receive no scanner group or socket mount.
`S3_ENDPOINT` controls private reads/scans/deletes. `S3_PUBLIC_ENDPOINT` controls
the signing client and must match the origin supplied to mobile builds as
`PRIVATE_UPLOAD_ORIGIN`; signed hosts are never rewritten. Production public
and private endpoints require HTTPS. Provider/OCR/bank approval and evidence
retention are separate from a clean malware verdict.

The scanner has a separate lifecycle. Stop its dedicated project with
`docker compose -f deploy/local/clamav.compose.yaml stop` when local testing is
finished. The actual scanner check uses uniquely named temporary test buckets,
records the engine/signature version and verifies clean, EICAR quarantine,
unavailable-scan and retry behavior. It never downloads live malware.

Bank verification is a separate, optional trusted bridge. Configure
`BANK_VERIFICATION_BASE_URL` together with the privately mounted
`BANK_VERIFICATION_TOKEN_FILE` on the transaction owner. Outside development
the bridge must use HTTPS. The synthetic local launcher keeps this provider
inactive. An absent bridge returns retryable 503 on positive vendor bank
verification or rider approval; a supplied opaque token alone never verifies a
bank account. Negative/rejected decisions remain possible.

The bridge's read-only `POST /v1/bank-accounts/verification:resolve` accepts
`tenant_id`, `country`, `owner_id`, `reference`, and optional expected masked
`holder_name`, `last4`, `ifsc`. It resolves an existing verification record;
it must never initiate a transfer or a paid verification during this call.
Its authenticated JSON response contains those scope/reference/masked fields,
`verification_id`, `status`, `verified_at` and `expires_at`. Only VERIFIED
results from the last 15 minutes are accepted, with at most one minute clock
skew and a future expiry bounded to 24 hours. Ownership, reference and supplied
masked metadata must match exactly (holder names ignore case/outer spaces).
Redirects, unknown/oversized/malformed results and missing provider access fail
closed. The resulting provider verification ID is written into the owning
transaction's audit/timeline. Vendor final approval revalidates the bank again.
The bridge and native tokenization still need configured provider/device UAT;
local test doubles cannot establish real bank verification.

Identity-document KYC is a separate trusted bridge. Configure
`KYC_VERIFICATION_BASE_URL` and a privately mounted
`KYC_VERIFICATION_TOKEN_FILE` together on the transaction owner. The bridge's
authenticated `POST /v1/identity-documents/verification:resolve` must return
an existing VERIFIED attestation for the exact tenant, country, participant,
purpose `ONBOARDING_KYC` and current document kinds/assets. The attestation
must be less than 15 minutes old and expire within 24 hours. Media upload,
malware scanning and a human opening a document cannot substitute for this
decision. Missing, rejected and mismatched proofs leave review state intact.

The completion launcher keeps synthetic KYC **off by default**. To test the
local bridge, set `LOCAL_SYNTHETIC_KYC` to `"true"` in the ignored
`.local/completion/runtime/transaction.json` profile, run
`node scripts/completion/stack.mjs up`, then
`node scripts/completion/kyc-attestation-smoke.mjs`. The smoke issues one
explicit synthetic decision for the precise test document set and checks
unissued, foreign-owner and rejected decisions before the matching approval.
Set the switch back to `"false"` and run `stack.mjs up` again when finished.
The synthetic provider is excluded from production images, and its decisions
are never evidence of real provider approval.

Vendor document OCR uses a separate resolver at
`POST /v1/documents/ocr-verification:resolve`. Configure
`OCR_VERIFICATION_BASE_URL` and a private `OCR_VERIFICATION_TOKEN_FILE`
together on the transaction owner. A result must match the exact tenant,
country, owner, document revision, purpose `VENDOR_ONBOARDING_OCR` and document
kinds/assets. The local completion launcher keeps it off by default. For an
isolated synthetic test, set `LOCAL_SYNTHETIC_OCR` to `"true"` in the ignored
transaction profile, run `stack.mjs up`, then explicitly issue a decision to
`POST /synthetic/documents/ocr-attestations` on the local synthetic provider
with its local-only token. Unissued, rejected and mismatched document sets fail
closed. Set the switch back to `"false"` and restart the local stack after
testing. The synthetic decision is never proof of real OCR approval.

Create a private `.local/identity` directory, apply the schema once, then run
the service:

```sh
umask 077
mkdir -p .local/identity
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out .local/identity/jwt-private.pem
openssl rand -hex 32 > .local/identity/refresh-hmac.key
openssl rand -hex 32 > .local/identity/guest-session-hmac.key
echo 'postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable' > .local/identity/database.url
make identity-schema-local
APP_ENV=development HTTP_ADDRESS=:8083 \
DATABASE_URL_FILE=.local/identity/database.url \
PROVIDER_BASE_URL=http://127.0.0.1:18081 \
TENANT_ID=c688a212-50fc-4d5c-b370-35a8c1d04f55 ALLOWED_COUNTRIES=IN,GB \
JWT_ISSUER=http://identity.local JWT_AUDIENCE=planext4u-mobile \
JWT_KEY_ID=identity-local-01 JWT_PRIVATE_KEY_FILE=.local/identity/jwt-private.pem \
REFRESH_HMAC_KEY_FILE=.local/identity/refresh-hmac.key \
GUEST_SESSION_HMAC_KEY_FILE=.local/identity/guest-session-hmac.key make run-identity
```

The transaction runtime also owns durable commerce, checkout, payment, wallet,
order and service-booking orchestration. Create a private 32-byte booking OTP
encryption key, seed synthetic local catalog/booking data, and start it with the
restricted transaction login:

```bash
mkdir -p .local/transaction
openssl rand -base64 24 > .local/transaction/booking-otp.key
openssl rand -base64 24 > .local/transaction/emergency-data.key
openssl rand -base64 24 > .local/transaction/local-contact.key
make local-seed-transaction
APP_ENV=development DATABASE_URL='postgres://planext4u_transaction_login:local-transaction-only@127.0.0.1:54320/planext4u_local?sslmode=disable' \
TENANT_ID=afc1e0db-73cf-40b3-9927-33590133da0b SUPPORTED_COUNTRIES=IN \
BOOKING_OTP_KEY_FILE=.local/transaction/booking-otp.key EMERGENCY_DATA_KEY_FILE=.local/transaction/emergency-data.key LOCAL_VERTICALS_CONTACT_KEY_FILE=.local/transaction/local-contact.key make run-transaction
```

The customer web process is the only browser-facing platform boundary. It
stores encrypted platform credentials, issues the opaque `__Host-p4u_customer`
cookie and proxies `/web/*` plus `/platform-api/*`. Reuse the identity guest
HMAC key and create a separate 32-byte BFF encryption key:

```bash
mkdir -p .local/customer-web
openssl rand -base64 32 > .local/customer-web/token.key
APP_ENV=development HTTP_ADDRESS=:8091 \
DATABASE_URL='postgres://planext4u_customer_web_login:local-customer-web-only@127.0.0.1:54320/planext4u_local?sslmode=disable' \
CUSTOMER_WEB_ALLOWED_ORIGINS=http://localhost:8080 \
IDENTITY_BASE_URL=http://127.0.0.1:8083 PLATFORM_GATEWAY_URL=http://127.0.0.1:8082 \
GUEST_SESSION_HMAC_KEY_FILE=.local/identity/guest-session-hmac.key \
CUSTOMER_WEB_TOKEN_KEY_FILE=.local/customer-web/token.key make run-customer-web
```

`make test-identity-integration` applies and removes only the `identity` schema
inside the disposable local database, then exercises real transactional refresh
rotation, reuse revocation, profile concurrency, consent evidence and ownership
denials. Do not point that target at a shared or non-local database.

The broader Testcontainers suite starts isolated dependencies on random ports.
The identity integration target instead uses the explicitly configured local
PostgreSQL URL and resets only its owned schema.
