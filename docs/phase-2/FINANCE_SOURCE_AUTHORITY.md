# Fulfillment finance source authority

Privileged fulfillment ledger, payout and reconciliation reads now require a
live operator directory entry and source session for the exact tenant and
country. Manual payout approval, execution and human settlement seeding use
the same owning database authority. The check validates the current directory
revision, session roles, MFA and command freshness under locks held through
the fulfillment transaction. A claimed finance role in an HTTP header cannot
substitute for a current finance operator session. A separate payout worker
principal retains its worker path.

Migration `admin/000020_fulfillment_finance_authority` exposes only a scoped
security-definer check to the fulfillment runtime; it does not grant direct
directory-table reads. Disposable PostgreSQL tests cover two finance
operators, country/tenant/subject denials, forged finance and super roles,
missing/stale source sessions, current reads, a queued directory revocation,
approval replay, manual execution and the direct HTTP owner route. The
corrected isolated gate passed all 27 PostgreSQL targets and 157 fresh and
rollback migrations. The complete backend gate and local health/API receipts
are in the Phase 2 completion register.

Rider/vendor self-service ledger and payout history reads, plus payout
requests, now hold the live participant identity/source session through the
owning transaction. The check rejects disabled accounts, effective deletion,
forged roles, foreign parties and revoked-session retries; a queued revocation
cannot leave a successful read or payout request. Focused disposable
PostgreSQL, unit and vet checks pass. The final complete source-matched backend
gate is pending and will be recorded in the Phase 2 completion register.

No persistent payout or ledger row was changed for this verification. Provider
payout execution, external banking and release proof remain open. iOS stays
deferred.
