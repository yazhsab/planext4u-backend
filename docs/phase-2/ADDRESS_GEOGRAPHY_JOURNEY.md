# Governed customer address geography

Verified locally on 27 October 2026. Phase 2 remains partial; Phase 3 has not started. iOS is deferred.

The normal customer saved-address editor can select active country → state → district → city → area entries authored in the canonical English/Tamil admin. The owning service accepts only the selected area ID and captures its five-entry ancestry atomically. Caller-supplied snapshots, unknown/foreign/wrong-kind/inactive references fail closed. Saved historical references remain readable after retirement and are preserved when editing an existing address. Legacy addresses remain compatible.

Address reads and mutation/replay acknowledgements lock the current owning identity/customer-role/source session. PostgreSQL checks freshness at commit. The admin hierarchy is accessed through bounded functions; commerce/configuration runtime roles cannot read private admin tables. Address serviceability reads only the owning postal-zone configuration. Saving/loading an address no longer depends on wallet/pricing publication. An unserviceable address can be saved without promising delivery availability.

Implicit default changes and promotion receive new revisions. Cross-device stale writes conflict, the final saved address can be removed, and acknowledged client writes refresh the authoritative address list. An acknowledged write followed by a failed refresh remains acknowledged with a visible refresh action. Account changes and disposed controllers suppress late results. Address text is bounded by Unicode characters; the new hierarchy picker supports English/Tamil and large text.

| Checkpoint gate | Result and receipt |
| --- | --- |
| Complete backend | PASS: quality/race/controlled load/build, all 27 PostgreSQL targets, 113 fresh-install/rollback migrations, dependencies/security scan; 684 matching core inputs. [Receipt](../completion/phase-2/evidence/backend-address-geography-full.json) |
| Complete Flutter workspace | PASS: 557 tests/all 12 analyses and generation/format/security/flavor/CI checks; 596 matching inputs. [Receipt](../completion/phase-2/evidence/flutter-address-geography-workspace.json) |
| Android compilation | PASS: customer/vendor/rider development/debug APKs, verified IDs/checksums and unchanged sources. [Receipt](../completion/phase-2/evidence/android-address-geography-compilation.json) |
| Persistent normal journey | PASS: 32 real HTTPS admin/normal Flutter HTTP assertions including source revocation, retirement, readback, default promotion and final removal. [Receipt](../completion/phase-2/evidence/local-address-geography-admin-flutter-journey.json) |
| Existing persistent regression suite | PASS: all 264 API checks. No financial configuration seed/reset. [Receipt](../completion/phase-2/evidence/local-address-geography-api-regression.json) |

Retained failures include a browser accessible-name selector and a synthetic-provider fixture token mismatch; both were corrected before the full journey passed. The Android wrapper initially treated native SDK warning stderr as a terminating PowerShell error; it now preserves warnings and checks the actual process exit status. Docker Compose encountered a Windows temporary-metadata lock; a workspace-scoped temporary directory allowed the normal rebuild/migration/deployment to finish. No database reset or Windows restart was used.

These are local implementation proofs with synthetic provider identities/MFA, separate from Android device, external provider, release-signing and production recovery evidence. Account export/erasure across owning stores, complete reason consumers, participant administration and governed legacy import remain Phase 2 work.
