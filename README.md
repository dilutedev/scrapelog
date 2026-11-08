# scrapelog

## Grafana

- Log in at the Grafana URL as `admin` with the password in `GRAFANA_ADMIN_PASSWORD`.
- Set `GRAFANA_ROOT_URL` to the public URL Grafana is served from.
- Dashboards and the ClickHouse datasource are provisioned from `grafana/provisioning/` and are edited there, not in the UI.
- The 25 GB budget on the storage gauge is set in `grafana/provisioning/dashboards/overview.json`. Edit it there if the disk budget changes.
- Put Cloudflare Access in front of the Grafana hostname.

## Backups

- Set `BACKUP_S3_URL` (bucket base URL ending in `/`, for example `https://<account>.r2.cloudflarestorage.com/<bucket>/`), `BACKUP_S3_ACCESS_KEY_ID` and `BACKUP_S3_SECRET_ACCESS_KEY` in `.env`, then run `docker compose up -d clickhouse` to recreate ClickHouse with them.
- `scripts/backup.sh` writes a full backup of the `scrapelog` database to `backups/scrapelog-<UTC timestamp>` in the bucket and exits non-zero on failure. Run it once by hand to check the credentials.
- The S3 credentials live in a ClickHouse named collection (`clickhouse/config.d/backup-s3.xml`), so they never appear in the SQL statement, the query log or the command line.
- The backup includes the `api_keys` hashes, so keep the bucket private.
- Schedule with cron:

```
17 3 * * * /path/to/scrapelog/scripts/backup.sh >> /var/log/scrapelog-backup.log 2>&1
```

- Or with a systemd timer: `/etc/systemd/system/scrapelog-backup.service`

```
[Unit]
Description=scrapelog ClickHouse backup

[Service]
Type=oneshot
ExecStart=/path/to/scrapelog/scripts/backup.sh
```

  and `/etc/systemd/system/scrapelog-backup.timer`, enabled with `systemctl enable --now scrapelog-backup.timer`:

```
[Unit]
Description=Nightly scrapelog ClickHouse backup

[Timer]
OnCalendar=*-*-* 03:17:00
Persistent=true

[Install]
WantedBy=timers.target
```

- Retention: add a lifecycle rule on the bucket for prefix `backups/` that expires objects after 7 days. Backups are full, so expect up to 7 times the data size in the bucket.

### Restore

This deletes the target's `scrapelog` database. Replace `<name>` with a name printed by the backup script, for example `backups/scrapelog-20261108T163416Z`.

1. Deploy the stack on the target with the same `.env` (all passwords and `BACKUP_S3_*`) and run `docker compose up -d clickhouse`. The init script creates an empty `scrapelog` database. Stop `ingest-api` and `grafana` if they are running.
2. Drop the empty database:

```
docker compose exec -T clickhouse clickhouse-client --query "DROP DATABASE scrapelog SYNC" </dev/null
```

3. Restore the backup:

```
docker compose exec -T clickhouse clickhouse-client --query "RESTORE DATABASE scrapelog FROM S3(backup_s3, filename = '<name>')" </dev/null
```

4. Start everything: `docker compose up -d`.

Restored API keys take effect once the API key cache expires (35 seconds). Users and grants come from `clickhouse/users.d/users.xml`, not from the backup.

## Dependencies

- The ingest API's direct dependencies are `clickhouse-go/v2` and `klauspost/compress`.
- `klauspost/compress` is used only to decode `Content-Encoding: zstd` request bodies, because Go's standard library has no zstd. It is already an indirect dependency of `clickhouse-go`.
