# Delegated field check-ins

The canonical organization workspace records a field visit through the fulfillment owner. It requests browser location only after the operator selects **Use current location**, explains the purpose, shows reported accuracy and asks for explicit confirmation. Permission denial, unavailable location and cancellation discard the capture. A changed account, selected country or available territory set unmounts the capture. Coordinates are not saved in browser storage.

The BFF exposes current command territory IDs and a point-only check-in command. It derives tenant, country, subject, fresh MFA, source session and account revision from the current server session. Caller authority fields are rejected. CSRF and an idempotency key are required. A global administrative role alone does not authorize attendance; a current explicit `resource.command` assignment is required.

The owning fulfillment transaction locks the live operator account and source session before checking the current organization assignment and territory. Both locks remain held through commit. Revocation, logout, changed country/revision, stale MFA and suspended assignments deny new commands and result replay. The bounded database function exposes no credentials or arbitrary account/session table access to fulfillment. Migration 000008 grants only execution of that function.

An unconfirmed network response retains the same point and reference for **Retry the same visit**. An accepted replay returns the original owning record; reusing a key with a different point conflicts. Success clears the captured point. Authorization denial clears it and refreshes current scope. Owner outages or malformed responses cannot become success.

Automated database tests exercise actual restricted runtime roles and queued revocation/logout races. The browser acceptance script injects a lost response after the real owner commits, then checks the normal retry, owning readback and current-scope revocation. Its signed local MFA and simulated browser coordinates are test fixtures. They do not establish real operator identity, physical presence, GPS integrity or production device acceptance.

Verification is recorded in the Phase 2 gate. This increment does not complete delegated exports, permissions in all other domains, all of AC-2.2 or Phase 2.
