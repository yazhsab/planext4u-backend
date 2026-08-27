# Deterministic local platform

This Compose project provides disposable local PostgreSQL, Redis, NATS
JetStream, MinIO-compatible object storage, and deterministic identity,
notification and geocoding provider doubles.

All credentials and identities are synthetic local fixtures. They must never be
reused in dev, staging or production.

```sh
make local-up
make local-status
make test-integration
make local-down
```

`make local-up` builds and waits for every dependency to become healthy. The
default host endpoints are:

| Dependency | Endpoint |
| --- | --- |
| PostgreSQL | `postgres://planext4u_local:local-only-password@localhost:54320/planext4u_local` |
| Redis | `redis://localhost:63790` |
| NATS | `nats://localhost:42220` |
| NATS monitor | `http://localhost:48222` |
| Object API | `http://localhost:9000` |
| Object console | `http://localhost:9001` |
| Synthetic providers | `http://localhost:18081` |

The Testcontainers suite starts isolated dependencies on random ports and does
not rely on the long-lived Compose project. This prevents local developer state
from making integration results pass accidentally.
