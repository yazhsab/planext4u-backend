# Phase 5 Socio and trust entry slice

The first Phase 5 vertical slice establishes the authority boundaries needed by
all later user-generated content and local-listing features.

## In scope

- additive Socio OpenAPI contract, synthetic feed fixture and compatibility baseline;
- service-owned tables for profiles, follows, blocks/mutes, posts, engagement,
  comments, reports, moderation decisions and idempotency;
- public/private profile visibility, follow request/acceptance and block precedence;
- published/pending/removed post states with deterministic ranking metadata;
- idempotent, revision-safe reactions, saves, comments and reports;
- moderator queue and MFA-protected decision endpoint;
- authenticated customer feed/create/engage flow in the controlled application.

## Non-negotiable rules

- Tenant and country are part of every authorization decision.
- Private profiles are visible in feed only to the owner or an accepted follower.
- A block in either direction overrides follows, feed visibility and interaction.
- Pending or removed content never appears in a customer feed.
- Clients render the server ranking version and moderation state; clients do not
  calculate reach or silently publish content.
- Privileged moderation requires both a moderation role and verified MFA.
- Media fields contain opaque private asset references, never public storage keys.

Stories, reels, realtime messaging, Homes, classifieds and emergency remain in
the executable backlog until this trust foundation is accepted.
