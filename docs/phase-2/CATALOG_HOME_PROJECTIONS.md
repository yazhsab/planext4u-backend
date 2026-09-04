# Catalog-read and customer-home projections

Story: `BE-P2-007`

The `catalog` read boundary provides ordered categories, item detail, category filtering, text search, opaque cursor pagination, a composed customer-home projection and consent-purpose-bound serviceability decisions. Money is integer minor units plus ISO currency. Legacy opaque media references remain additive compatibility fields, while category and item projections prefer typed media presentations with safe URLs, intrinsic dimensions, required alt text, responsive variants and nullable expiry.

The service holds bounded last-known-good projections. A dependency failure returns `STALE` only inside the configured freshness window and `DEGRADED` empty state after expiry. Responses expose projection status in both the body and `X-Projection-Status`, so clients never confuse stale data with fresh stock or availability.

Expired presentations are removed at the catalog request boundary, and unsafe
URLs are rejected when projections are stored. OpenAPI compatibility, synthetic
fixtures, cursor validation, fresh/stale/degraded transitions, search,
item/category reads, media expiry and location-purpose enforcement are
automated.

Published CMS `SERVICE_RAIL` blocks name a `collection_id`. The home boundary
resolves only matching materialized `SERVICE_COLLECTION` projections and
returns them in `service_collections`, keyed by that stable identifier. Every
service entry carries stable service/provider IDs, an optional typed media
presentation, server-owned money and localized `price_display`, a trust
summary, a bounded navigation target and serviceability computed from the
optional `postal_code` request query. The private postal-code scope remains in
the materialized catalog document and is never exposed to clients. Unknown CMS
collection IDs therefore produce no invented client content, while an absent
postal code fails closed with `serviceable: false`.
