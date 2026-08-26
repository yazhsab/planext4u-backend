# Legacy export inventory

## Phase 1 disposition

No authorised schema, database, object-store or application export was supplied in Phase 1. The deployed POC UI is not a data-authority substitute. The approved inventory state is therefore `NOT_AVAILABLE — MIGRATION DISABLED`.

Greenfield Phase 2 development uses synthetic fixtures only. No migration loader may accept production data until an authorised export package passes the intake gates below.

## Expected domain packages

| Package | Minimum expected objects | Reconciliation owner |
| --- | --- | --- |
| Identity/access | identities, roles, account state, consent; no password/token plaintext | Security + Product |
| Customer/supply | profiles, addresses, vendors, riders, franchises, KYC references/status, zones | Product + Ops |
| Catalog/inventory | categories, attributes, products/services/menu, variants/SKUs, stock | Commerce owner |
| Transactions | carts if required, orders/items, bookings, payments/refunds, POD references | Commerce + Finance |
| Wallet/settlement/tax | complete ledgers, expiry lots, rules, commission, payouts, invoices/credit notes | Finance |
| Content/local verticals | social metadata, Homes, classifieds, moderation/status/expiry | Product + Trust |
| Media | object manifest, ownership, classification, checksum, size/type, lifecycle status | Security + Data |
| Configuration/audit | countries, tax/gateway/flags/CMS/policies, audit/event exports | Product + Security |

## Intake gate

Every supplied package must include:

- Written owner authorization and purpose.
- Source system/version, export timestamp, consistency point and extraction method.
- Encrypted delivery path, checksum manifest and restricted access list.
- Schema/data dictionary, identifiers, enums, timezone/currency/points representation and deletion semantics.
- Counts by status/date/tenant plus known duplicate/orphan/null/test-data report.
- Retention/deletion deadline for the raw restricted zone.
- Explicit confirmation that secrets, password plaintext, raw card data and unnecessary restricted fields are absent.

## Mapping register template

| Source object/field | Classification | New owner/service | New field/state | Transform/default | Reject condition | Reconciliation rule | Owner | Status |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| To be completed from an authorised export | TBD | TBD | TBD | TBD | TBD | TBD | TBD | Not available |

## Activation rule

The Data owner opens a reviewed change with the completed mapping register, synthetic/masked fixtures, count/checksum evidence and reconciliation tolerances. Security and the domain owner must approve before any restricted package is landed. Finance approval is additionally mandatory for payment, wallet, settlement and tax data.

Until then:

- There is no legacy runtime dependency or bidirectional sync.
- Production migration infrastructure remains disabled.
- UI observations are useful for feature parity only and never generate inferred records.
- Phase 6 rehearsal/cutover cannot pass, but Phase 2 greenfield foundations may proceed safely.
