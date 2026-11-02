# Occupation profile journey: locally verified

27 October 2026. Phase 2 remains partial; iOS is deferred.

The canonical master-data admin owns occupation codes and English/Tamil names. Authenticated customers can search the active country directory from their normal profile screen, save a choice, read it after signing in again and clear it. Saved retired choices remain readable; new retired choices are rejected. Schema-3 portable exports include only safe occupation snapshots and cleared revisions. Earlier export schemas retain their contracts.

The backend provides `GET /v1/occupations` and `GET`/`PUT /v1/me/occupation`, scoped database functions and identity-owned persistence. Current identity/source-session locks, expected revisions, reference locks and atomic security events protect writes. Admin migration 12 and identity migration 9 are deployed. Real PostgreSQL/runtime-role tests cover isolation, revocation, audit rollback and concurrent commands. Normal HTTP tests cover strict command/query fields, missing provider and revoked sessions.

The complete backend gate passes quality/race/controlled load/build, all 27 PostgreSQL targets, dependencies and security scan on 673 matching core source inputs. Identity coverage is 81.1%, above the unchanged 80% requirement. The earlier 79.9% failure is retained. The scan reports zero reachable/imported vulnerabilities and an unused required-module notice.

The Flutter workspace passes 549 tests and all 12 analyses across 593 unchanged source inputs. Customer, vendor and rider development/debug APKs compile. Actual HTTPS admin authoring and normal Flutter/IoApiTransport save, fresh-sign-in readback and portable export pass in the persistent stack. The complete 17-check driver also verifies retirement, retained snapshots, clear, stale/unknown/forged commands and revocation. Provider/MFA assertions are synthetic; current native-device and release acceptance are not inferred.

Docker recovery restored normal Windows registration using the signed same-version official installer in a native process context. The documented WSL data-root option points to the existing physical disk directory; no uninstall or reset was used. Installer-time disk sizes, write timestamps and first/last MiB samples stayed unchanged. This is not a full independent backup/restore claim. Existing containers/volumes and current updated API health were verified. Launch Docker Desktop from Windows Start so it uses the normal Windows profile.

To rerun checks, inspect each result before continuing, from the backend repository:

```powershell
node scripts/completion/verify.mjs
node scripts/completion/stack.mjs up
node scripts/completion/stack.mjs verify
node scripts/completion/occupation-smoke.mjs
```

The actual acceptance driver uses uniquely owned synthetic fixtures, revokes sessions and preserves master revision evidence during cleanup. It never resets the business/financial database. Tokens are not written to retained reports.

See [the consolidated acceptance receipt](../completion/phase-2/evidence/occupation-journey-checkpoint.json) and [the Phase 2 register](../completion/phase-2/STATUS.md). Continue remaining geography/reason consumers, participant administration, onboarding, cross-owner privacy/lifecycle and other open Phase 2 acceptance. Phase 3 has not started and production readiness has not been established.
