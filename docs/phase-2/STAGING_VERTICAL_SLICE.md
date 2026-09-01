# Staging vertical slice

`BE-P2-016` provides both a local synthetic contract harness and a distributed
staging release gate. The cloud gate runs against the same independently
deployed services and gateway routing used by production.

## Journey and evidence

`BE-VSLICE-001` executes these contracts through the gateway:

1. `GET /health/ready`
2. `POST /v1/auth/exchange` for the fixed synthetic customer
3. `POST /v1/serviceability/check` for the Chennai synthetic zone
4. `GET /v1/bootstrap`
5. `GET /v1/home`
6. `GET /v1/catalog/items`

The test verifies signed-token issuance, token expiry and tamper denial,
gateway-derived tenant/country headers, response correlation IDs, incoming
trace propagation, feature configuration, location serviceability, and
deterministic catalog projections. The shell and Dart staging smoke clients
perform the same live journey without printing the access token or response
bodies.

## Local synthetic harness

The `cmd/staging-slice` executable is retained only for local contract and
mobile-development testing. It refuses to start unless all of the following
are true:

- `APP_ENV=staging`
- `SYNTHETIC_SLICE_ENABLED=true`
- `SYNTHETIC_SLICE_SIGNING_KEY` contains 32 to 1024 bytes

Terraform creates the signing-key container only in staging, encrypts it with
the environment data key, and injects it only into the staging platform task.
The key value is provisioned outside Terraform state. Production delivery
builds `cmd/platform`, not the synthetic executable.

It is not built or deployed by the protected cloud delivery workflow.

## Distributed delivery and rollback

The staging delivery workflow builds and signs every selected service image and
SBOM, applies checked migrations, updates the service set in a controlled
sequence, and then runs the complete journey against
`VERTICAL_SLICE_SMOKE_ORIGIN`. Login uses the approved staging identity provider
and protected `STAGING_SMOKE_PROVIDER_TOKEN`; synthetic provider tokens are not
accepted outside development. A failed migration, stabilization, readiness
check, or journey restores every service changed by the release. The uploaded
JSON evidence contains the immutable images, prior task definitions, final
smoke result, and per-service rollback outcomes.

For a localhost verification only:

```sh
APP_ENV=staging SYNTHETIC_SLICE_ENABLED=true \
  SYNTHETIC_SLICE_SIGNING_KEY=replace-with-a-local-32-byte-value \
  HTTP_ADDRESS=127.0.0.1:18080 go run ./cmd/staging-slice

ALLOW_INSECURE_LOCAL_SMOKE=true \
  ./scripts/staging-vertical-slice-smoke.sh http://127.0.0.1:18080
```

Cloud apply remains an owner-controlled operation because it creates billable
resources and requires the authorized AWS accounts, ACM certificate, protected
GitHub environment, role ARNs, staging DNS, and externally populated secrets.
