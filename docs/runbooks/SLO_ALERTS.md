# Phase 2 SLO alert runbooks

Every response starts by assigning an incident owner, adding the alert time and dashboard link to the incident record, and preserving correlation IDs. Never paste credentials, tokens, contact values, request bodies or raw URLs into incident chat.

## Availability fast burn

1. Confirm the 5xx increase on the RED dashboard and identify the affected service, route and country without adding customer identifiers.
2. Compare the first failing trace with dependency-error and deployment markers. Check readiness, database saturation, queue lag and provider health.
3. Roll back the most recent release if failure begins at deployment and rollback is safe. Otherwise reduce traffic with the gateway/circuit breaker and page the owning service team.
4. Resolve only after the 5xx ratio remains below the fast-burn threshold for 30 minutes. Record budget consumed and create follow-up work.

## Availability slow burn

Follow the fast-burn diagnosis without emergency traffic reduction unless the error ratio accelerates. Open an owner ticket, attach sanitized trace IDs and verify the ratio has recovered for two hours before closure.

## Latency budget burn

1. Split p95 by static route and compare active requests, dependency latency, database pool use and queue lag.
2. Inspect representative traces for the slow span; do not use raw customer URLs as dimensions.
3. Scale the constrained tier or disable a non-essential feature flag. Roll back a correlated deployment.
4. Confirm p95 below the 400 ms overall budget for 30 minutes and document the bottleneck; stricter endpoint budgets remain authoritative.

## Queue lag high

1. Confirm consumer name, backlog growth, oldest message age, retry count and DLQ movement.
2. Check consumer readiness, NATS/JetStream health, database locks and downstream throttling.
3. Scale consumers only when processing is idempotent and the dependency can accept the load. Pause producers for unsafe backlog growth.
4. Use authenticated tenant-scoped DLQ replay only after the cause is fixed; preserve its audit record.

## Notification provider failure

1. Identify the adapter and error classification; confirm provider status and credential validity without exposing credentials.
2. Verify retryable errors remain bounded and permanent failures are not retried. Disable marketing traffic before essential security traffic.
3. Fail over only to an approved provider with current consent and template configuration.
4. Reconcile provider receipts and queued deliveries after recovery.
