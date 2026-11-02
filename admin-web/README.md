# Administrator client

Node 24 and pnpm 11.25.0 are pinned by CI and package.json. Install with `pnpm install --frozen-lockfile`; run `pnpm verify` and `pnpm test:e2e`. The canonical lockfile is pnpm-lock.yaml. Dependency postinstall auto-configuration is disabled; Firebase public client settings are explicit in .env.example.

For provider-backed operator login, configure the matching Firebase project in the Go admin service, provision its server-owned operator account and enroll TOTP. The browser exchanges fresh verified MFA evidence for an HTTP-only secure cookie and clears its in-memory provider session. Firebase claims and browser fields never choose platform roles or countries.

The local synthetic stack continues to support isolated verification without external provider credentials. It is not evidence that live Google, SMS, MFA, KYC or banking providers passed acceptance.
