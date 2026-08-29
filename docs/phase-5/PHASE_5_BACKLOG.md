# Phase 5 backend executable backlog

## Outcome

Deliver engagement, local verticals and complete governance with server-owned
privacy, moderation, ranking, contact policy, expiry and emergency decisions.
User-generated media stays private until policy permits publication, and every
privileged decision is attributable and auditable.

| ID | Story | Depends on | Acceptance evidence |
| --- | --- | --- | --- |
| `BE-P5-001` | Publish additive Socio, moderation, Homes, classifieds and emergency contracts plus service-owned schemas | Phase 4 media, identity, maps, notification and audit | Compatibility, fixture and from-zero migration checks pass |
| `BE-P5-002` | Implement social profiles, private accounts, follow requests, block/mute and privacy controls | 001 | Cross-tenant, private-profile, block and relationship-race suites pass |
| `BE-P5-003` | Implement posts, media references, feed eligibility and versioned deterministic ranking | 001-002 | Visibility, tombstone, pagination, ranking-version and sponsored-label tests pass |
| `BE-P5-004` | Implement reactions, saves/collections, nested comments, mentions, hashtags and product stickers | 002-003 | Idempotency, depth, actor scope, counter reconciliation and safe-link tests pass |
| `BE-P5-005` | Implement stories/highlights, reels, DMs, voice notes, presence and WebRTC signalling | 001-004, messaging | Expiry, mutual-follow, request inbox, presence TTL and signalling authorization pass |
| `BE-P5-006` | Implement media processing, automated/manual moderation, reports, appeals and retention/takedown jobs | 001-005, media/audit | Quarantine, malware/blur/transcode, four-eyes, purge and cache-tombstone suites pass |
| `BE-P5-007` | Implement Homes listing types, owner KYC, geo search, amenities, estimates, visits, plans and upgrades | 001, supply/maps/payment | Ownership, KYC, geo, estimate-version, inquiry and feature-expiry suites pass |
| `BE-P5-008` | Implement classifieds posting, moderation/auto-publish, masked contact, WhatsApp handoff, expiry/repost and upgrades | 001, media/payment | Contact consent, abuse, expiry, report and entitlement tests pass |
| `BE-P5-009` | Implement emergency assistance, responder assignment, live location, escalation and SLA reporting | Phase 4 fulfilment/maps | Consent, atomic assignment, location TTL, escalation and audit tests pass |
| `BE-P5-010` | Complete governance administration for moderation, policy, configuration, flags, sessions and countries | 001-009 | RBAC, fresh MFA, four-eyes, audit and country-isolation tests pass |
| `BE-P5-011` | Deliver all finance/operations reports, exports, maps, heatmaps, leaderboards and intelligence projections | 001-010 | Reconciliation, export authorization, masking, freshness and large-data tests pass |
| `BE-P5-012` | Complete parity, realtime/media/reporting soak, resilience and security acceptance | 001-011 | Controlled E2E, soak and P0/P1 parity register pass |

## Entry slice

Implementation starts with `BE-P5-001` through `BE-P5-004`: social profiles,
privacy relationships, a moderated feed and core engagement. The entry slice is
accepted only when private posts cannot leak, blocks override follows, pending
content cannot rank, mutations are idempotent/revisioned and moderator actions
require an MFA-authenticated privileged role.

## Completion

All stories `BE-P5-001` through `BE-P5-012` are implemented. Contract,
migration, domain, gateway, concurrency, race, administrator UI and controlled
load evidence is recorded in the [Phase 5 exit review](PHASE_5_EXIT_REVIEW.md).
