# ADR-0002: Web client contract and session boundary

- Status: Accepted
- Date: 2026-09-01
- Scope: Customer web, customer BFF, configuration, identity, media, catalog
  and booking projections

## Context

The customer web application has a verified platform-backed storefront slice,
but five decisions were still preventing complete migration from the legacy
browser runtime:

1. whether signed-out customers may browse the storefront;
2. how web participates in bootstrap and feature configuration;
3. how opaque media references become safe browser assets;
4. how CMS `SERVICE_RAIL` blocks obtain service data; and
5. whether browser tokens are exposed to JavaScript or held by a BFF.

These are product and trust-boundary decisions. They must be stable before the
next additive OpenAPI changes and before additional web verticals migrate.

## Decision

### 1. Guest storefront access

Signed-out customers may browse public, country-scoped storefront content.
Guest access uses a short-lived, read-only platform session with the `GUEST`
role. The session is created and retained by the same-origin customer BFF; the
browser does not receive its platform access token.

The guest session contains only the tenant, country, locale, coarse
serviceability context and a random session identifier. It cannot access
customer resources or perform mutations. Cart, saved address, booking,
checkout, messaging, contact reveal and account actions require an authenticated
customer session. Authentication upgrades the browser session and never merges
guest data without an explicit, idempotent server operation.

### 2. Web bootstrap semantics

`WEB` is added to the configuration `Platform` enum. Web supplies a semantic
application version and an immutable deployment identifier. It consumes the
same maintenance, locale, feature and CMS policy as native clients, but native
store-update language is not exposed to web users. A required web update is
rendered as a refresh/reload gate for the current deployment.

### 3. Browser media presentation

Opaque `media_asset_id`, `media_ref` and `icon_ref` values are never converted
to storage URLs by clients. Public projections return a bounded media
presentation object containing:

- `asset_id`;
- an HTTPS or same-origin `url`;
- `content_type`, intrinsic `width` and `height`;
- required accessibility `alt_text`;
- zero or more responsive variants with URL, width and height; and
- nullable `expires_at` for short-lived presentations.

Private media continues to use authorised retrieval. A browser presentation
does not imply publication, ownership or permission to retrieve the underlying
object.

### 4. CMS service-rail projection

The customer home projection adds typed `service_collections` keyed by the CMS
block `collection_id`. Each item contains a stable service/provider identifier,
display title, media presentation, server-owned price display, serviceability,
trust summary and navigation target. Unknown or unavailable collections are
omitted; clients do not map catalog items or help shortcuts into service rails.

### 5. Customer browser session model

Authenticated customer web sessions use a same-origin BFF. The BFF performs
the provider assertion exchange and refresh rotation, stores platform tokens
server-side and issues a host-only `Secure`, `HttpOnly`, `SameSite=Lax` opaque
session cookie. State-changing browser requests require origin validation and a
CSRF token. The BFF forwards correlation context and derives tenant, country
and role from the server-held platform session.

Mobile applications continue to use the reviewed native bearer-token model.
The administrator BFF remains separate and retains its stricter administrator
session and MFA controls.

## Contract implementation sequence

1. Add `WEB` bootstrap contract and tests.
2. Add guest-session and customer-BFF session exchange contracts.
3. Add the reusable media presentation schemas and resolver/projection fields.
4. Add service collections to the home projection.
5. Regenerate TypeScript and Dart clients and run compatibility tests.
6. Migrate browser verticals only after the corresponding consumer tests pass.

## Security and failure behavior

- Guest sessions are rate-limited, read-only, short-lived and country-scoped.
- Browser JavaScript never receives platform access or refresh tokens.
- Unsafe origins, absent CSRF evidence and session-country mismatches fail
  closed.
- Media URLs must be HTTPS or same-origin outside local development.
- Expired media produces an explicit unavailable state; clients never guess a
  storage path.
- Missing service collections omit only the affected rail and cannot fabricate
  service data.

## Consequences

- `WEB-001` through `WEB-005` are resolved as architecture decisions, while
  their additive backend and BFF implementation remains Milestone 2 work.
- The current in-memory browser token implementation is transitional and must
  not be considered the final authenticated web session boundary.
- Complete legacy migration can proceed vertical by vertical against stable
  contracts without waiting for payment, Firebase, TURN, upload or deployment
  credentials.
