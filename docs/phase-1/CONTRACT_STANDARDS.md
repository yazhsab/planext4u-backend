# API and event contract standards

## Authority and formats

- External and BFF HTTP APIs are defined in OpenAPI 3.1 before implementation.
- Durable domain events are defined in AsyncAPI with JSON Schema; protobuf is reserved for measured high-throughput internal streams or gRPC.
- Generated clients/models are outputs. The reviewed contract source is authoritative.
- APIs exchange domain identifiers and immutable snapshots, never database/ORM representations.

## HTTP conventions

| Concern | Standard |
| --- | --- |
| Base path | `/v1/{resource}`; additive evolution within a major version |
| Identity | `Authorization: Bearer`; server derives actor/tenant/role, never trusts client role fields |
| Correlation | `traceparent`, `tracestate`, `X-Correlation-ID`; generate when absent and return it |
| Idempotency | `Idempotency-Key` required for financial, order, booking, assignment and other retryable commands |
| Concurrency | Strong ETag/version plus `If-Match` for editable resources; conflict returns `409` |
| Time | RFC 3339 UTC instants; explicit IANA timezone for local schedules; no ambiguous local timestamp |
| Money | Integer minor units plus ISO 4217 currency; no floating-point money |
| Points | Signed 64-bit integer units; rule/conversion version included where relevant |
| Location | WGS84 decimal coordinates, accuracy metres, captured-at instant and consent/workflow purpose |
| Pagination | Opaque cursor with stable ordering; `next_cursor`, `has_more`; no client-derived offset for large/live sets |
| Filtering | Documented allow-list; server rejects unknown/unsafe fields |
| Large exports | Accepted as async jobs; short-lived signed artifact after row/field authorization and CSV sanitisation |

## Command response

- Synchronous completion returns the final resource or projection with `200/201`.
- Durable asynchronous acceptance returns `202`, command/workflow ID, current state and polling/subscription link.
- Duplicate idempotent requests return the original semantic result, not a second mutation.
- Clients receive `allowed_actions` for stateful resources; they never infer actions from a status string.

## Error envelope

```json
{
  "error": {
    "code": "ORDER_STATE_CONFLICT",
    "message": "The order changed before this action completed.",
    "correlation_id": "opaque-id",
    "retryable": false,
    "field_errors": [
      {"field": "delivery_slot_id", "code": "NO_LONGER_AVAILABLE", "message": "Choose another slot."}
    ],
    "details": {}
  }
}
```

- `code` is stable, documented and safe for client decisions.
- `message` is user-safe and localisable; stack traces/provider payloads never cross the boundary.
- `details` has an explicit schema and cannot contain secrets or unrestricted provider data.
- `401` means authentication is absent/invalid; `403` means authenticated but denied; `404` may conceal existence when required.
- `409` covers state/concurrency/idempotency conflict; `422` covers valid JSON that violates business validation; `429` includes bounded retry guidance.

## Versioning and compatibility

- Adding optional response fields, new enum values with documented unknown handling and new endpoints is additive.
- Removing/renaming fields, changing meaning/type/requiredness, reducing authorization or altering event partition semantics is breaking.
- Consumers must tolerate unknown additive fields and enum values through an explicit `UNKNOWN` path.
- Breaking changes require a new major contract, migration plan, dual-support window and telemetry-confirmed consumer retirement.
- CI runs OpenAPI/AsyncAPI/protobuf compatibility checks against the default branch and consumer fixtures.

## Event envelope

```json
{
  "event_id": "uuid",
  "event_type": "planext4u.order.status_changed.v1",
  "occurred_at": "2026-08-26T12:00:00Z",
  "producer": "commerce-order",
  "aggregate_type": "order",
  "aggregate_id": "uuid",
  "aggregate_version": 7,
  "tenant_id": "uuid",
  "country": "IN",
  "correlation_id": "opaque-id",
  "causation_id": "opaque-id",
  "traceparent": "w3c-trace-context",
  "classification": "internal",
  "schema_version": 1,
  "data": {}
}
```

- Partition key is the aggregate ID unless an approved ordering rule specifies another key.
- Producers publish through a transactional outbox. Consumers persist inbox/deduplication state with their side effect.
- Delivery is at least once; every handler is idempotent and safe under duplicate/out-of-order delivery.
- Events are facts in past tense. Commands are not broadcast as events.
- Payloads contain the minimum immutable snapshot needed by consumers; restricted data uses references and authorised retrieval.
- Replay uses an explicit job identity, time/range bounds, rate control, audit and consumer replay mode.
- Poison events enter a DLQ with reason/attempt metadata; repair and replay are audited.

## Security and authorization contracts

- Access tokens contain stable subject/session/tenant/country and coarse roles; resource ownership and current permission are evaluated server-side.
- Privileged APIs declare `fresh_auth` and `approval_policy` requirements in contract extensions.
- BFFs may reduce/compose responses but cannot grant actions that an owning service denied.
- Webhooks use provider-specific signature/timestamp validation, raw-body verification and unique provider event IDs.
- Media contracts use presign -> upload -> complete -> scan -> ready/rejected; an upload alone never makes media public.

## Contract ownership and review

- Each contract names owning service, reviewers, data classifications, SLO class and deprecation policy.
- Money/wallet/tax/settlement contracts require Finance reviewer; identity/KYC/location/admin contracts require Security/Privacy reviewer.
- Examples use synthetic data and cover success, validation, denial, conflict, idempotent replay and dependency degradation.
- Contract implementation is complete only when provider and consumer tests pass in CI.

## Phase 2 contract freeze order

1. Common metadata/error/pagination/idempotency/money/time/location schemas.
2. Identity/session/device/role and audit schemas.
3. Configuration/CMS/media/notification schemas.
4. Customer home/location/catalog-read BFF schemas.
5. Outbox/event metadata and service health/observability conventions.
