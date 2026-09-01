# Gateway and BFF security foundation

## Boundary and ownership

`cmd/gateway` is the runnable stateless edge process. It verifies platform
tokens, applies distributed abuse controls and forwards only derived identity
context to an allow-listed private service selected by the longest exact path
prefix. It does not contain profile, consent,
catalog, order, money, stock, permission or workflow truth. Owning services
remain the only authorities for resource access and allowed actions.

The request path is:

```text
client -> correlation/security headers -> IP rate limit -> bounded body
       -> RS256 verification -> subject/session rate limit -> timeout
       -> credential-stripping reverse proxy -> BFF/domain API
```

## Authentication boundary

- Only `Authorization: Bearer <token>` is accepted.
- JWT `alg` is pinned to `RS256`; `none`, algorithm substitution, unknown
  `kid`, bad signatures, expired/future tokens and wrong issuer/audience fail.
- Required signed claims are `sub`, `sid`, `tenant_id`, `country`, `roles` and
  `exp`; optional `device_id` participates in rate partitioning.
- RSA public keys must be 2048 bits or stronger. The verifier accepts a keyed
  rotation set; the initial runtime reads a mounted public-key file and key ID.
- Client-supplied `X-Planext4u-*`, `Authorization` and `Cookie` headers are
  removed. The proxy injects subject, session, tenant, country, roles and device
  only from verified claims.

Resource ownership and current permissions are intentionally not evaluated at
the gateway. Those checks belong to the service that owns the resource.

The exact `POST /v1/auth/exchange`, `POST /v1/auth/refresh` and
`POST /v1/auth/revoke` routes are anonymous upstream routes. They still pass the
anonymous/IP limiter, body bound, timeout, correlation and header-stripping
pipeline. Other methods, path prefixes and suffixes remain authenticated, and
no empty or client-supplied trusted identity header is forwarded. Additional
anonymous POST routes, such as signed payment webhooks and notification
receipts, must be explicitly enumerated through `ANONYMOUS_ROUTES`. The provider
signature header is preserved for the owning service to verify; identity-scope
headers are always removed and regenerated from a verified JWT.

## Rate and request controls

- Anonymous/IP and authenticated-principal limits are separate gates.
- Rate keys are SHA-256 digests; raw IP, subject, session and device values are
  not stored as Redis keys or written to logs.
- The Redis fixed-window operation is one atomic Lua evaluation. Store failure
  fails closed with `RATE_LIMIT_UNAVAILABLE`; it never silently bypasses the
  safety control.
- A bounded in-memory adapter exists only for deterministic tests/local tools.
  Horizontally scaled deployments use `RedisLimiter`.
- Declared and streaming request bodies are rejected above the configured cap
  (1 MiB default, 16 MiB hard maximum) before proxying.

## Correlation, timeout and error mapping

- Valid `X-Correlation-ID` is preserved and returned; missing/invalid input gets
  a cryptographically random value.
- Valid W3C version-00 `traceparent`/`tracestate` is forwarded. Invalid trace
  context is removed.
- Requests have a bounded end-to-end deadline (10 seconds by default).
- Dependency timeouts map to `504 UPSTREAM_TIMEOUT`; connection failures map to
  `503 UPSTREAM_UNAVAILABLE`.
- Malformed, oversized or non-contract dependency errors map to
  `502 UPSTREAM_RESPONSE_INVALID`. Valid error envelopes are re-encoded through
  the common schema, their correlation ID is replaced with the edge value, and
  unknown top-level data is stripped.
- Credential values and full resource paths are absent from request logs. Only
  the first two path segments are recorded as a low-cardinality path group.

## Runtime security configuration

The process requires:

| Setting | Purpose |
| --- | --- |
| `UPSTREAM_URL` | Allow-listed BFF/service base URL |
| `UPSTREAM_ROUTES` | JSON map of exact path prefixes to allow-listed private service URLs |
| `ANONYMOUS_ROUTES` | JSON map of exact provider callback paths to the only allowed method (`POST`) |
| `JWT_ISSUER`, `JWT_AUDIENCE`, `JWT_KEY_ID` | Verification policy |
| `JWT_PUBLIC_KEY_FILE` | Mounted public key; never a private signing key |
| `REDIS_URL_FILE` | Mounted secret/config file containing the Redis URL |
| `APP_ENV`, `HTTP_ADDRESS` | Isolated environment and listener identity |
| `REQUEST_TIMEOUT`, `SHUTDOWN_TIMEOUT`, `MAX_REQUEST_BYTES` | Bounded lifecycle controls |
| `IP_RATE_LIMIT`, `PRINCIPAL_RATE_LIMIT`, `RATE_WINDOW` | Distributed abuse policy |

Staging/production require HTTPS for public upstream hosts; plaintext HTTP is
accepted only for single-label or `.internal` private service-discovery hosts.
The issuer must use HTTPS and Redis must use TLS.
Redis credentials are read from the mounted file and are never logged or placed
in Flutter configuration.

## Verification

The unit and race suite covers valid and tampered JWTs, missing/expired denial,
header spoofing, credential stripping, correlation and trace policy, declared
and streaming size limits, both rate tiers, fail-closed Redis degradation,
timeouts, unreachable services, unsafe dependency errors, safe error
normalisation, readiness degradation and no-credential logging.

Run:

```sh
make verify
make local-up
make test-gateway-integration
make local-down
```

The integration test uses a unique key prefix and deletes only that key.
