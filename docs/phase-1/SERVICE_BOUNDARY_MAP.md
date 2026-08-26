# Initial service-boundary map v0.1

These are initial deployable groups for Phase 2, not permanent organisational boundaries. Each service owns its write model, migrations, credentials, SLOs and public contracts. Direct cross-service table access is prohibited.

| Deployable | Owned capabilities | Primary data | Synchronous dependencies | Published events |
| --- | --- | --- | --- | --- |
| Edge and BFF | Customer/vendor/rider/admin aggregation, version negotiation, rate limit, request identity, response shaping | No domain truth; short response cache only | All exposed domain APIs | Request/security telemetry only |
| Identity and customer | OTP/OAuth/password, sessions/devices, roles/policies, customer profile, address, consent and deletion | Identities, sessions, roles, profiles, addresses, consent | OTP/OAuth provider, notification | IdentityCreated, RoleGranted, ConsentChanged, AccountLifecycleChanged |
| Supply | Vendors, service providers, restaurants, riders, franchises, field officers, KYC stages, bank verification, zones and subscriptions | Supply profiles, KYC references/status, territories, schedules, bank-verification reference | Identity, media, maps, notification | VendorStatusChanged, RiderStatusChanged, KycStageChanged, ZoneChanged |
| Catalog and discovery | Categories, attributes, products, services, menus, variants, approval, questions, search, geo ranking, recommendations and inventory availability projection | Catalog, approval workflow, search index projections; inventory stock/reservations in owned schema | Supply, media, configuration | CatalogItemChanged, CatalogApprovalChanged, StockChanged, SearchDocumentRequested |
| Commerce and order | Cart, pricing snapshot, promotions, fees/taxes, product/food order state, cancellation, returns and customer confirmation | Carts, promotion decisions, orders/items, return cases | Catalog, payment, wallet, fulfilment, configuration | OrderPlaced, OrderStatusChanged, ReturnRequested, CustomerConfirmedCompletion |
| Payment | Razorpay/Paystack/COD intent, provider order/reference, signed webhooks, reconciliation, refund commands and chargebacks | Payment intents/attempts/webhooks/refunds; no card data | Providers, order, notification | PaymentAuthorised, PaymentFailed, PaymentCaptured, RefundCompleted, ReconciliationException |
| Wallet and settlement | Point ledger/conversion/expiry/referrals/rewards, commissions, invoices, vendor/rider settlements and payout workflow | Immutable wallet/commission/settlement ledgers | Payment, order, supply, configuration | WalletEntryPosted, RewardGranted, SettlementCalculated, SettlementApproved, PayoutRecorded |
| Fulfilment and booking | Service slots/bookings, restaurant preparation projection, rider availability, dispatch, assignment, location/ETA, POD, attendance and emergency responder assignment | Slots/bookings, assignments, location TTL/projection, POD references, attendance/emergency cases | Order, supply, maps, media, notification | SlotReserved, BookingStatusChanged, AssignmentChanged, LocationUpdated, DeliveryCompleted, EmergencyStatusChanged |
| Local verticals | Homes, localities, amenities, visits, property intelligence, classifieds, expiry/repost, inquiries and contact privacy | Properties, classifieds, inquiries, visits, reports | Identity, supply, media, maps, moderation, payment/wallet | PropertySubmitted, ClassifiedPublished, InquiryCreated, ListingLifecycleChanged |
| Socio and realtime | Social profiles/graph, posts/reels/stories/comments/reactions/saves, feed, DMs, voice notes, presence and WebRTC signalling | Social graph/content metadata, messages, ephemeral presence | Identity, catalog, media, moderation, notification | SocialContentPublished, EngagementRecorded, ContentExpired, MessageCreated, CallLifecycleChanged |
| Communications, trust and administration | Push/email/WhatsApp/inbox, support, moderation, CMS, policies, countries, flags, platform variables, privileged audit and admin workflows | Templates/preferences/receipts, tickets, moderation, CMS/config, append-only audit | All event producers, external comms providers | NotificationDelivered, ModerationDecided, ConfigurationChanged, TicketStatusChanged, AuditExportReady |
| Reporting and intelligence | Finance/ops reports, exports, maps, heatmaps, leaderboards, performance and recommendation features | Event-fed read models, warehouse/lake data, export artefacts | Event bus, object storage, configuration | ReportReady, MetricThresholdBreached, RecommendationGenerated |

## Boundary rules

- Shared PostgreSQL infrastructure may be used initially, but ownership is enforced through separate databases/schemas, credentials and migration pipelines.
- Services exchange identifiers and immutable snapshots/events, not ORM/database models.
- Edge/BFF services never become an alternative source of business truth.
- Financial, wallet, stock and state transitions are commands against the owner, never client-calculated updates.
- Cross-domain workflows use an orchestrated saga or durable process manager with compensating commands.
- New service splits require evidence: independent scale, security isolation, separate release cadence or organisational ownership.

## Contracts to define before Phase 2

1. Common error envelope, pagination/cursor, idempotency and correlation headers.
2. Identity/session/role claims and policy-decision interface.
3. Media presign/complete/scan lifecycle.
4. Money, currency, points, tax and rounding representation.
5. Location/accuracy/timezone and consent representation.
6. Order, booking, payment, settlement and assignment command/event schemas.
7. Audit actor/target/reason/before/after schema.
8. Event metadata, versioning, partition key, causation/correlation and replay policy.
