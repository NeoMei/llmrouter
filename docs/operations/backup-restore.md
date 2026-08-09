# Backup and restore

PostgreSQL and the S3 object store are durable data. Redis is disposable coordination/cache state and is not a substitute for either backup.

## Backup

Create an encrypted backup directory outside the repository:

```bash
umask 077
backup_id=$(date -u +%Y%m%dT%H%M%SZ)
mkdir -p "${BACKUP_DIR}/${backup_id}"
pg_dump --format=custom --no-owner --no-acl --dbname="${SQL_DSN}" \
  --file="${BACKUP_DIR}/${backup_id}/database.dump"
aws s3api list-object-versions --bucket "${OBJECT_STORE_BUCKET}" \
  --output json > "${BACKUP_DIR}/${backup_id}/object-versions.json"
sha256sum "${BACKUP_DIR}/${backup_id}"/* > "${BACKUP_DIR}/${backup_id}/SHA256SUMS"
```

Enable bucket versioning and lifecycle retention at the provider. Copy database dumps to a separate account/region and encrypt them with the organization's backup key. Record application image digest, schema version, object-store bucket/version IDs, and backup checksum together.

## Restore rehearsal

Restore only into an isolated database and bucket. Never point a rehearsal at production.

```bash
cd "${BACKUP_DIR}/${backup_id}"
sha256sum --check SHA256SUMS
createdb "${RESTORE_DATABASE}"
pg_restore --clean --if-exists --no-owner --no-acl \
  --dbname="${RESTORE_SQL_DSN}" database.dump
psql "${RESTORE_SQL_DSN}" -c 'select count(*) from users;'
psql "${RESTORE_SQL_DSN}" -c 'select count(*) from channels;'
```

Restore the referenced object versions to an isolated bucket, start the exact recorded image digest against the restored services, then verify `/readyz`, login, a known file checksum, API-key decryption, one relay request, and billing totals. Delete the rehearsal environment only after evidence is retained.

## Production recovery

Declare an incident, stop writes, take a final snapshot if possible, restore the latest verified backup, replay only audited post-backup changes, and resume with one canary. Missing object versions or a failed credential-decryption sample is a hard stop.
