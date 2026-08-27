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
