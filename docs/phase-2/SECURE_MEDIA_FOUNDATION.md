# Secure media lifecycle foundation

BE-P2-008 implements an owner- and tenant-scoped media state machine:

`PENDING_UPLOAD -> PENDING_SCAN -> READY | REJECTED`

Uncompleted grants expire and deletion produces an inaccessible `DELETED`
tombstone. Upload grants last between one and thirty minutes and bind the
object key to the authenticated tenant, subject, purpose, media type, byte
length and SHA-256 digest. Completion compares object-store metadata before a
malware adapter is invoked. A mismatch or infected object is rejected and
deleted; scanner outages retain `PENDING_SCAN` for safe retry.

The repository intentionally returns not-found for cross-tenant and
cross-owner access to avoid identifier enumeration. API responses never expose
object keys. Provider-specific signing, object storage and malware products are
ports; their credentials remain outside this service and repository.

`BE-MEDIA-001` covers the clean path, idempotent completion, grant expiry,
metadata mismatch, malware rejection, retryable scanner failure, deletion and
IDOR denial. The OpenAPI compatibility baseline protects all four v1
operations and terminal states.

BE-009 adds the public presentation boundary without weakening the owner-only
lifecycle above. `POST /v1/media/presentations:resolve` returns only READY,
PUBLIC `CATALOG_IMAGE` assets for the trusted tenant and country. Each response
contains an HTTPS or same-origin URL, content type, intrinsic dimensions,
required alt text, typed responsive variants and a five-minute expiry; missing,
private, rejected and cross-country asset IDs are returned only as unavailable.
The service never exposes or asks a client to derive an object key.

Catalog-image upload grants now require bounded width, height and alt text.
PostgreSQL stores that metadata through migration
`media/000003_browser_presentations`, and the S3 adapter signs GET requests
through the same bounded presentation port. Production storage endpoints and
signing credentials remain environment-specific release configuration.
