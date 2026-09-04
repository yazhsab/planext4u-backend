# Configuration and CMS foundation

Story: `BE-P2-006`

The `configcms` boundary owns immutable tenant/country snapshots and resolves native and web bootstrap state. It fails closed for missing regional configuration and supports Android/iOS/WEB minimum/latest semantic versions, maintenance windows, deterministic negotiation across English, Tamil, Hindi, Telugu, Kannada, Malayalam, Marathi, Bengali and Gujarati, versioned consent policies, default-off feature flags and ordered home composition.

WEB callers must send both their semantic application version and immutable deployment identifier. The configuration runtime receives the current `WEB_DEPLOYMENT_ID`; a stale identifier or version below the minimum produces a required `RELOAD` action, while native clients retain `STORE_UPDATE` semantics. Migration `configuration/000004_web_bootstrap` safely initializes existing snapshot and workspace documents with WEB versions before the stricter schema is enforced.

Publishing uses optimistic revision checks, append-only audit evidence and explicit cache invalidation. Read results are defensive copies with ETag and bounded private caching. No user PII or secret is part of a snapshot or audit payload.

The OpenAPI 3.1 contract and synthetic bootstrap fixture are compatibility-gated. Unit and handler tests cover required/optional/no-update decisions, stale web deployments, reload versus store actions, active maintenance, locale fallback, invalid snapshots, conflict, cache invalidation, audit and safe error envelopes.
