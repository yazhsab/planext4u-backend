# Service catalogue

The final deployment count is validated in Phase 1. These are bounded contexts, not permission to create one service per table.

| Context | Responsibilities |
| --- | --- |
| Edge and BFF | Customer/vendor/rider/admin aggregation, API versioning, rate limits, request identity and response shaping |
| Identity and access | OTP/OAuth/password, token/session/device lifecycle, roles, policies, MFA and account deletion |
| Customer profile | Profile, addresses, consent, preferences, localisation and saved entities |
| Vendor and franchise | Vendor/service-provider/franchise profile, KYC stages, field visits, bank verification, zones and subscriptions |
| Catalog | Categories, attributes, products, services, restaurant menus, variants, pricing, media links, approval and questions |
| Discovery and recommendation | Search, geo ranking, autocomplete, leaderboards, trends, personalisation and AI-assisted suggestions |
| Inventory | Stock, reservations, availability, SKU adjustments, low-stock alerts and reconciliation |
| Cart and promotion | Carts, coupons, campaign rules, stacking/exclusivity, sponsorship and fee/discount allocation |
| Order | Product and food order state machines, item snapshots, cancellation, returns, refunds and customer confirmation |
| Booking | Service schedules, slots, locks, reschedule/cancel, start OTP, completion/no-show and disputes |
| Payment | Razorpay/Paystack/COD orchestration, webhooks, signature verification, retries, reconciliation and chargebacks |
| Wallet and loyalty | Immutable point ledger, conversion, refills, FIFO expiry, referrals, engagement rewards, limits and anti-abuse |
| Settlement and finance | Commission hierarchy, cooling periods, vendor/rider payouts, invoices, credit notes, tax and reconciliation |
| Fulfilment and dispatch | Rider/service-agent availability, assignments, routing, live location, ETA, reassignment and POD |
| Homes | Property listings, amenities/localities, moderation, plans, inquiries, visits, reports, EMI and value estimation |
| Classifieds | Listings, media limits, moderation, expiry/repost, contact privacy, reports and featured upgrades |
| Socio graph and feed | Profiles, follows, privacy, posts, comments, reactions, saves, stories/reels and feed ranking |
| Messaging and calls | Threads, requests, order chat, media/voice notes, presence, blocking and WebRTC signalling |
| Media | Presigned uploads, policy validation, malware scan, WebP rewrite, transcode, thumbnails and private access |
| Notification | Push, email, WhatsApp, in-app inbox, templates, preferences, quiet hours, receipts and retries |
| Emergency and support | Emergency requests/responders/escalation, tickets, feedback, FAQs and contextual help |
| Configuration and CMS | Countries, currency/tax/gateway config, feature flags, home layouts, banners, splash, policies and app versions |
| Moderation and trust | Content/vendor/product/review reports, automated risk signals, queues, decisions and appeals |
| Audit and compliance | Append-only privileged events, session/login history, evidence export, retention and deletion orchestration |
| Reporting and analytics | Sales/GST/payments/settlements/customer/vendor/referral/points/day-book/credit-note reports, maps and exports |

## Initial deployable grouping

To avoid a distributed monolith, the first production cut groups strongly coupled contexts into roughly 10-12 independently deployable services. Split decisions require measured scaling, security or release-cadence evidence. Service-to-service calls are never used to reconstruct a single database transaction.
