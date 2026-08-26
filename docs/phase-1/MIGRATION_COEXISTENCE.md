# Legacy data migration and coexistence strategy v1.0

Approval: accepted on 2026-08-26. No authorised legacy export has been supplied; migration is therefore disabled until the Phase 6 inventory gate in [Legacy export inventory](LEGACY_EXPORT_INVENTORY.md) is satisfied.

## Boundary

The Lovable/Vercel application code is never imported, linked or used as an implementation reference. Migration discovery uses authorised schema/data/media exports, business-owner interviews, and black-box behaviour evidence.

## Migration stages

### 1. Inventory and authority

- Identify current systems of record for identity, roles, customers, addresses, vendors, KYC references, catalog, orders, payments, wallet, settlements, Socio, Homes, classifieds, notifications, configuration and audit.
- Record table/object counts, identifiers, relationships, enums/statuses, null/duplicate/orphan rates, media locations and external provider references.
- Name a business owner and reconciliation rule for every domain.

### 2. Canonical mapping

- Map legacy identifiers to new UUIDs while preserving an immutable external-reference table.
- Define enum/state mapping, currency/points rounding, timezone, phone/email normalisation and soft-delete semantics.
- Separate migratable truth from derived/cache/test/POC-only data.
- Version mapping specifications and fixtures in the backend repository without copying legacy application code.

### 3. Repeatable extraction and transformation

- Obtain read-only, timestamped exports or approved API snapshots.
- Land encrypted raw exports in a restricted migration zone with checksums and retention.
- Run deterministic transformations with validation/reject reports and no manual production editing.
- Import through service-owned loaders or migration APIs so invariants and audit are preserved.

### 4. Rehearsal and reconciliation

- Execute multiple full-size rehearsals against staging.
- Reconcile counts, referential integrity, financial/wallet totals, stock, open orders/bookings, KYC status, media availability and sampled user journeys.
- Record performance, downtime estimate, reject handling and rollback point.

### 5. Coexistence and cutover

- Prefer legacy read-only or bounded change-freeze window for financial/order cutover.
- If dual-running is required, define a single writer per domain; do not use uncontrolled bidirectional sync.
- Route an internal cohort and one-city pilot to the new platform, compare outcomes and expand progressively.
- Freeze, final-delta export, import, reconcile, switch DNS/app remote config and monitor business/technical gates.

### 6. Rollback and retirement

- Rollback changes routing and writer ownership; it never attempts ad-hoc reverse replication.
- Preserve final legacy export, mapping, reconciliation sign-off and legally required audit evidence.
- Retire the POC only after the rollback window, finance reconciliation, support closure and product sign-off.

## Reconciliation gates

| Domain | Required evidence |
| --- | --- |
| Identity/roles | Every active identity maps once; role/portal access samples pass; orphan report resolved |
| Customer/vendor/rider | Active/inactive/deleted counts, key-field completeness and ownership/territory samples |
| Catalog/inventory | Item/variant/SKU counts, price/stock comparisons and media availability |
| Orders/bookings | Count and total by date/status/vendor; every open workflow has valid mapped state |
| Payments/refunds | Provider references unique; captured/refunded totals and exception report signed by Finance |
| Wallet/loyalty | Sum of ledger entries equals migrated balances; expiry lots and reversals reconcile with zero unexplained variance |
| Settlement/tax | Source entries, commission rules, payout state, invoices/credit notes and GST totals signed by Finance |
| Social/listings/media | Ownership, privacy/status, expiry, moderation/takedown and object/checksum samples |
| Configuration | Country, tax, gateway, feature flag, CMS, policy and minimum-version values approved by Product/Ops |

## Data safety

- No production secrets or raw restricted exports enter Git or developer laptops.
- Non-production rehearsals use masked/synthetic data unless a controlled environment is approved.
- Migration jobs are idempotent, resumable, observable and produce signed checksums/reports.
- Every rejected row has a reason, owner and disposition; silent dropping is prohibited.
