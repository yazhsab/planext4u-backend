# Append-only audit foundation

BE-P2-010 records privileged decisions in a tenant-isolated append-only stream.
Every entry requires an authenticated actor, action, target, outcome, reason,
country, correlation ID and occurrence time. User and service actors cannot
forge a different subject identity.

Each tenant has a monotonically increasing sequence and SHA-256 hash chain.
The repository exposes append, last and search only—no update or delete API.
Before/after evidence is cloned, bounded and recursively redacted for token,
password, secret, email, phone, cookie and authorization fields.

Search requires `audit.read`. Export requires `audit.export`, authentication
within five minutes and a human-readable reason; exported entries include a
chain-verification result. Cross-tenant filters are overwritten with the
principal's tenant scope.

`BE-AUDIT-001` exercises completeness, pagination, immutability, redaction,
chain verification, cross-tenant isolation, forged-actor denial and fresh-auth
export denial under the race detector.
