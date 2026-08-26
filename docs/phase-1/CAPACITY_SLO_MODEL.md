# Capacity and SLO model v0.1

## Document baselines

| Measure | Baseline |
| --- | --- |
| Monthly active users | 1,000,000 target capacity |
| Concurrent active sessions | 10,000 minimum design target |
| API latency | P95 <= 400 ms overall; endpoint budgets required |
| Critical-path availability | 99.9% monthly for auth, checkout and payment |
| Checkout controlled success | >= 97% |
| Push delivery | >= 95% where provider accepts message |
| Settlement batch | < 10 minutes at launch-period volume |
| MTTD / P1 MTTR | < 5 minutes / < 30 minutes |
| Mobile cold start / image first paint | <= 2.5 s / <= 1.2 s on agreed reference device/network |

## Workload model to validate in Phase 1

Traffic inputs are not yet approved. Product/Data must provide current POC registrations, DAU/MAU, peak-hour ratio, home refreshes, searches, feed views, messages, media uploads, order/booking conversion, location pings and report volumes. Capacity tests will then model:

```text
peak requests/sec = peak concurrent sessions x actions/session/minute / 60
event throughput = commands/sec x average emitted events/command x retry/replay factor
database write IOPS = domain writes/sec x rows/write x index amplification
media egress = daily views x average rendition bytes x cache-miss ratio
location events/sec = online agents / configured ping interval
```

## Initial endpoint budgets

| Path family | P95 target | Availability class | Degradation strategy |
| --- | ---: | --- | --- |
| Token refresh / session validation | 150 ms | Critical | Cached keys/policies; fail closed for mutation |
| Home/catalog/search read | 300 ms | Important | Cached/stale-safe sections; omit recommendations before core content |
| Cart reprice / checkout validation | 350 ms | Critical | No stale financial result; actionable retry |
| Place order / payment command acceptance | 400 ms | Critical | Idempotent accepted/process response; durable workflow |
| Order/booking status read | 250 ms | Critical during active task | Read projection and bounded stale indicator |
| Location ingest | 150 ms | Important | Buffer/backpressure; drop superseded intermediate ping, never final state |
| Feed read | 350 ms | Important | Cached cursor page; omit lower-priority enrichment |
| Send message | 250 ms command acceptance | Important | Durable outbox and pending state |
| Admin list/report preview | 500 ms list; async large report | Operational | Server pagination; queue export |

## Test profiles

- Baseline: expected peak for 30 minutes after warm-up.
- Burst: at least 2.5x expected peak for 10 minutes without data corruption.
- Soak: expected peak for 8-24 hours; measure leaks, queue lag, cache churn and database bloat.
- Dependency degradation: payment/maps/FCM/WhatsApp/search unavailable or slow.
- Event replay: controlled replay at production peak while normal traffic continues.
- Regional/AZ failure: maintain critical reads/commands or meet documented RTO/RPO.

## SLO implementation requirements

- Per-service and end-to-end SLIs for availability, latency, correctness, queue lag and business outcomes.
- Error-budget policy gates risky releases and prioritises reliability work.
- Correlation from Flutter/admin action through gateway, service, event, provider and notification.
- Alerts use multi-window burn rates and link to tested runbooks; raw CPU alone is not a service alert.
- Financial correctness, duplicate avoidance and reconciliation exceptions are reliability indicators, not only business reports.

## Open inputs

- Peak city/country distribution and campaign/notification fan-out sizes.
- Online rider/service-agent count and location frequency by task state.
- Product/media/catalog sizes and feed/view ratios.
- Order, food and service transaction mix plus payment-method split.
- RPO/RTO by domain and legally required recovery evidence.
- Reference low-end Android devices and representative Indian network profiles.
