# Country-scoped master-data administration

Implemented locally on 26 October 2026. Accountable roles: Platform/Backend, Admin, Security and QA. This is a verified Phase 2 slice; the complete phase remains partial.

The canonical Go/admin stack now owns country labels, states, districts, cities, areas, occupations and reason codes. The Phase 2 plan explicitly requests these masters, and the legacy source contains the prior Supabase occupation and geography workflows (`src/lib/api.ts`, occupation and location methods). The new workflow uses no Lovable or Supabase calls.

`/masters` provides English/Tamil names, stable codes, literal name/code search, active/inactive filters, bounded keyset pagination, creation, revisioned edits, parent selection and per-entry change history. Geography follows country → state → district → city → area. Root country codes must match the selected country. Country labels are separate from the existing governed currency, tax, launch and allowed-country settings.

`PUT /admin/api/v1/masters/{master_id}` requires a current super/country administrator, MFA, fresh source authentication and CSRF. Tenant, country, subject and source-session authority come from the server session. The owning service locks and checks the current directory and source session through commit, including for reads/history. Auditors can read; regional, content and support roles cannot administer the country-wide directory. Client authority selectors are rejected.

Migration `admin/000011_master_data` creates the reference store and append-only revision evidence. Record changes and evidence commit atomically. Code, kind and parent links remain fixed after creation, preserving references. Active children prevent parent deactivation. There is no physical-delete endpoint, and the runtime role cannot update or delete history. Stale edits, duplicate scoped codes, invalid/cross-scope parents and audit failures cannot partially commit.

## Local acceptance

| Check | Result |
| --- | --- |
| Complete backend quality/race/load/build, 27 PostgreSQL targets, dependencies and security scan | Pass; 663 current core sources match the receipt |
| Two tenants/countries, country-wide role denials, literal search and real runtime-role execution | Pass |
| Full hierarchy, immutable references, concurrent revision conflict and audit rollback | Pass |
| Source-session revocation queued ahead of a command | Pass; command cannot commit |
| Canonical admin type/lint/coverage/production build | Pass; 119 tests, unchanged thresholds |
| Actual persistent BFF and desktop/mobile HTTPS browser | Pass; 42 checks, including geography edit, pagination, history, direct denials and revocation |
| Responsive layout and accessibility | Desktop/mobile screenshots inspected; no horizontal overflow or serious/critical axe violations |
| Existing persistent APIs and local services | Pass; 264 API checks and seven healthy APIs |

Receipts are in `docs/completion/phase-2/evidence`: `backend-master-data-full.json`, `backend-master-data-source.json`, `admin-master-data-full.json`, `local-master-data-browser.json`, `local-master-data-health.json` and `local-master-data-api-smoke.json`. The initial database-fixture type failure and browser-selector failure remain recorded separately. Local browser identity assertions and exact owned directory/session bindings are synthetic; they do not establish real Firebase MFA.

## Remaining work

The occupation consumer is now locally verified in the normal customer profile with owning-write validation, retired-reference handling and portable export; see [the 27 October acceptance](OCCUPATION_PROFILE_JOURNEY.md). Connect the remaining geography/reason references to registration, address and onboarding consumers with owning-write validation. Governed import/migration of authorized legacy master data and complete participant administration remain open. This slice does not establish cross-owner privacy/deletion, configured providers, Android release readiness or Phase 2 exit. iOS remains deferred. Continue locally without requesting additional user input.
