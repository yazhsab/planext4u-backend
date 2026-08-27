# Secure administrator web shell

`BE-P2-015` establishes the administrator browser security boundary and the first contract-backed operational view. It is greenfield code and does not reuse the Lovable/Vercel implementation.

## Runtime boundary

The BFF exposes only `GET /admin/api/v1/session`, `PUT /admin/api/v1/session/country`, and `GET /admin/api/v1/audit/events`. An opaque `__Host-p4u_admin` cookie is Secure, HttpOnly, SameSite=Strict, host-only and path-bound to `/`; plaintext session tokens are never stored server-side. Mutations require an origin allow-list and constant-time CSRF-token validation. Responses disable caching, framing, content sniffing, referrer disclosure, cross-origin opener sharing, and unused browser permissions.

The BFF owns role-to-capability expansion and navigation. The browser uses that navigation for clarity, but every API operation independently authorizes the role, tenant and selected country. `SUPPORT_ADMIN` and `CONTENT_ADMIN` cannot read audit events; customer or unknown roles cannot create an administrator session. Audit queries ignore caller-supplied country attempts and are always rewritten to the authenticated selected country. Production configuration can require an MFA authentication method, while the session view records MFA and five-minute fresh-auth assurance for future sensitive operations.

The in-memory opaque session store is a deterministic adapter for tests and the synthetic staging slice. The interface intentionally permits the distributed encrypted session adapter to be connected to the identity service without changing browser contracts.

## Client

`admin-web/` is a separately deployable React, TypeScript, Vite and Tailwind application. OpenAPI generates compile-time DTOs; Zod validates untrusted runtime responses. TanStack Query owns bounded request caching/cancellation, React Hook Form validates the audit filter, TanStack Table renders the server-paginated audit page, and React Router enforces the available routes. The browser sends same-origin cookies only and does not use local or session storage.

Routes:

- `/` shows authenticated role, country and assurance context without fabricated operational metrics.
- `/audit` provides country/tenant-scoped immutable audit activity, action filtering, opaque cursor pagination and explicit loading, empty, error and permission states.

The layout has skip navigation, semantic landmarks, labelled controls, visible focus, non-colour status labels, reduced-motion handling, a focusable horizontal table region, and responsive navigation at mobile widths. The visual language is restrained, dense and consistent with the existing Planext4u green/neutral product direction captured in Phase 1.

## Verification evidence

- Go tests cover authentication, MFA enforcement, server-issued navigation, cross-role denial, country isolation, cross-site/CSRF denial, secure cookie attributes, hashed token storage and expiry.
- Frontend unit/component tests cover authenticated, permission, sign-in, MFA, service error/recovery, country switch, runtime contract validation, filtering, pagination, empty state and responsive navigation.
- Frontend coverage is 90.64% statements, 77.53% branches, 90.47% functions and 93.04% lines.
- Playwright covers desktop and mobile audit review, keyboard skip navigation and the mobile drawer. Axe reports no serious or critical violations at either viewport.
- The production Vite build and browser console complete without warnings or errors.

The upstream identity callback that exchanges a verified provider/MFA result for this administrator cookie remains an integration point, not browser-owned authentication logic. No provider secret or raw bearer token is introduced into the frontend.
