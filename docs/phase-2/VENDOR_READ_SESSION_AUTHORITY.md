# Vendor read session authority

The normal supply service now checks the current vendor role, source session,
tenant, country and due-deletion state before returning an application, catalog,
work list or dashboard. These reads hold the identity source session in the same
transaction as their data queries and recheck it before commit. A queued read
loses to a source-session revocation that commits first. The dashboard gathers
application, catalog and work totals inside one authority window.

Country-wide catalog reads require a current, fresh-MFA country/super reviewer
source session. A role label alone cannot open the catalog. This does not grant
regional participants country-wide catalog access.

Disposable PostgreSQL tests cover absent source sessions for all four vendor
reads, effective deletion, foreign tenant, a reviewer without a source session,
normal vendor/reviewer catalog readback and a queued application read after
session revocation. A focused supply integration run and the supply unit package
passed. The complete source-matched backend and live stack gates are recorded
separately in the Phase 2 completion register.

These checks protect current supply reads. Other owning resources, provider
verification and terminal cross-service erasure remain Phase 2 work. iOS stays
deferred.
