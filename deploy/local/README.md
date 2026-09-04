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
