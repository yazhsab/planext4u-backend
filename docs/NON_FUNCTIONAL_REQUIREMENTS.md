# Non-functional requirements

These baselines come from the supplied product and technical documents. Phase 1 converts them into workload-specific budgets and test scenarios.

| Area | Baseline |
| --- | --- |
| Scale | 1M MAU and at least 10K concurrent active sessions |
| API performance | P95 <= 400 ms; endpoint-specific budgets for browse, checkout, tracking and chat |
| Availability | 99.9% monthly on authentication, checkout and payment critical paths |
| Checkout | Successful controlled checkout journeys >= 97% |
| Mobile performance | Cold start <= 2.5 s on target 4G device; image first paint <= 1.2 s; usable on 2 GB RAM Android devices |
| Detection and recovery | MTTD < 5 min; P1 MTTR < 30 min |
| Accessibility | WCAG 2.1 AA and Flutter semantics on every primary flow |
| Localisation | English plus Hindi, Tamil, Telugu, Bengali, Marathi, Gujarati, Kannada and Malayalam |
| Privacy | DPDP-aligned consent, purpose, access, correction, portability where applicable, deactivation and deletion within 30 days |
| Security | TLS 1.2+, rotating RS256 keys, least privilege, MFA/fresh reauth, signed media access, OWASP controls and immutable audit |
| Data protection | Multi-AZ, daily snapshots, PITR, tested restore, retention policy and separate dev/staging/prod accounts |

## Required verification

- Go unit/property/fuzz tests and race detector.
- Testcontainers integration suites and migration compatibility tests.
- OpenAPI/event consumer-driven contract tests and backward-compatibility gates.
- End-to-end role/state-machine tests across Flutter, admin and services.
- k6 load, spike, soak and degradation testing from capacity-model workloads.
- Chaos tests for database failover, event lag, Redis loss, provider timeout and partial-region dependency failure.
- SAST, SCA, SBOM, secret, IaC, container, DAST and independent penetration tests.
- Financial, wallet, inventory and migration reconciliation with zero unexplained variance.
- Disaster-recovery game day and production rollback rehearsal before launch.
