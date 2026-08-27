# Service-owned migrations and credentials

`BE-P2-012` establishes independently owned PostgreSQL schemas for identity, configuration, catalog, media, audit, messaging and notification. A platform bootstrap migration creates NOLOGIN owner and runtime group roles; a deployment identity receives owner membership, while each application login receives only its matching runtime membership.

| Service | Schema | Owner group | Runtime group |
| --- | --- | --- | --- |
| Identity | `identity` | `planext4u_identity_owner` | `planext4u_identity_runtime` |
| Configuration | `configuration` | `planext4u_configuration_owner` | `planext4u_configuration_runtime` |
| Catalog | `catalog` | `planext4u_catalog_owner` | `planext4u_catalog_runtime` |
| Media | `media` | `planext4u_media_owner` | `planext4u_media_runtime` |
| Audit | `audit` | `planext4u_audit_owner` | `planext4u_audit_runtime` |
| Messaging | `messaging` | `planext4u_messaging_owner` | `planext4u_messaging_runtime` |
| Notification | `notification` | `planext4u_notification_owner` | `planext4u_notification_runtime` |

Runtime logins are provisioned outside migrations. Their random passwords or short-lived IAM credentials must be stored as separate secret-manager records, mounted as files, and rotated independently. Owner groups are never application login roles. `PUBLIC` receives no schema usage, and the migration policy rejects cross-service schema references.

## Operation

The migration command reads its privileged connection URL only from `MIGRATION_DATABASE_URL_FILE`; URLs on command lines are intentionally unsupported.

```sh
umask 077
mkdir -p .local/migrations
echo 'postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable' > .local/migrations/database.url
make migration-check
make migrate-local
make test-migrations-integration
```

Every file has an explicit down pair. Applied versions and SHA-256 checksums are recorded in `platform_migrations.applied`; an edited applied migration is rejected. Each migration is executed in its own database transaction, so a failed upgrade writes neither schema state nor ledger state. Down migrations execute services in reverse dependency order and remove platform roles last.

## Compatibility policy

Version-one migrations create the greenfield schemas. Later upgrades must use expand-and-contract: add nullable columns/tables and dual-read or dual-write first, deploy compatible readers, backfill with a restartable job, then remove obsolete data only in a separately approved later phase. The automated check rejects direct drops, renames and new `NOT NULL` constraints in upgrade migrations.
