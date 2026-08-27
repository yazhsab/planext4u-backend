# Catalog-read and customer-home projections

Story: `BE-P2-007`

The `catalog` read boundary provides ordered categories, item detail, category filtering, text search, opaque cursor pagination, a composed customer-home projection and consent-purpose-bound serviceability decisions. Money is integer minor units plus ISO currency; media is referenced by opaque ID.

The service holds bounded last-known-good projections. A dependency failure returns `STALE` only inside the configured freshness window and `DEGRADED` empty state after expiry. Responses expose projection status in both the body and `X-Projection-Status`, so clients never confuse stale data with fresh stock or availability.

OpenAPI compatibility, synthetic fixtures, cursor validation, fresh/stale/degraded transitions, search, item/category reads and location-purpose enforcement are automated.
