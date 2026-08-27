# Configuration and CMS foundation

Story: `BE-P2-006`

The `configcms` boundary owns immutable tenant/country snapshots and resolves mobile bootstrap state. It fails closed for missing regional configuration and supports Android/iOS minimum/latest semantic versions, maintenance windows, EN/Tamil locale negotiation, versioned consent policies, default-off feature flags and ordered home composition.

Publishing uses optimistic revision checks, append-only audit evidence and explicit cache invalidation. Read results are defensive copies with ETag and bounded private caching. No user PII or secret is part of a snapshot or audit payload.

The OpenAPI 3.1 contract and synthetic bootstrap fixture are compatibility-gated. Unit and handler tests cover required/optional/no-update decisions, active maintenance, locale fallback, invalid snapshots, conflict, cache invalidation, audit and safe error envelopes.
