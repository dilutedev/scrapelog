# scrapelog

scrapelog collects one row per HTTP request your scrapers make. Scrapers send records to an ingest API, the API stores them in ClickHouse, and Grafana shows dashboards and a request explorer on top. Everything runs from one Docker Compose file.

## Quickstart

1. Copy the environment file and fill in the secrets:

```
cp .env.example .env
openssl rand -base64 32
```

   Run the second command once per empty value and paste the result: `CLICKHOUSE_ADMIN_PASSWORD`, `CLICKHOUSE_INGEST_PASSWORD`, `CLICKHOUSE_READER_PASSWORD`, `ADMIN_TOKEN` and `GRAFANA_ADMIN_PASSWORD`. Use `openssl rand -base64 32 | tr -d '\n/+='` to avoid characters that need quoting. `.env` is gitignored; never commit it.

2. Start the stack and wait for every healthcheck:

```
docker compose up -d --wait
```

3. Create an ingest key for a project. The key is printed once; only its SHA-256 hash is stored:

```
docker compose exec ingest-api /scrapelog keys create --project my-project --name my-scraper
```

4. Send data with one of the [examples](#examples), using the printed `key` as `SCRAPELOG_KEY`.
5. Open Grafana at `http://127.0.0.1:3000` (or your `GRAFANA_PORT`) and log in as `admin`.

Upgrades: v1 has no schema migrations. The ClickHouse init script in `clickhouse/initdb/` runs only when the data volume is empty, so a column added by a newer version (for example the request body columns) exists only in a freshly initialised volume. To pick up a schema change, take a [backup](#backups) if you need the data, then recreate the volume with `docker compose down -v && docker compose up -d --wait`. This deletes all stored requests and API keys.

## Architecture

- `clickhouse`: a single ClickHouse node. It publishes no host port and is reachable only on the `scrapelog` Docker network.
- `ingest-api`: the Go HTTP API. It is published on `127.0.0.1:${INGEST_API_PORT:-8080}`.
- `grafana`: Grafana OSS with the ClickHouse plugin, published on `127.0.0.1:${GRAFANA_PORT:-3000}`.
- Host ports bind to loopback only. To expose them, put a reverse proxy in front (see [Cloudflare and reverse proxy](#cloudflare-and-reverse-proxy)).
- ClickHouse users, all with passwords from `.env`:
  - `ingest_writer` (used by `ingest-api`): can insert into and read `scrapelog.requests` and `scrapelog.api_keys`, and read `system.parts` for the storage measure.
  - `grafana_reader` (used by Grafana): read-only on `scrapelog.requests` and `scrapelog.requests_hourly`.
  - `scrapelog_admin`: full access, for operations such as [TTL changes](#ttl-tuning) and restores.

## Configuration

All configuration is environment variables in `.env`; Compose passes them to the containers.

| Variable | Default | Meaning |
| --- | --- | --- |
| `CLICKHOUSE_ADMIN_PASSWORD` | none, required | Password of the `scrapelog_admin` user. |
| `CLICKHOUSE_INGEST_PASSWORD` | none, required | Password of the `ingest_writer` user. |
| `CLICKHOUSE_READER_PASSWORD` | none, required | Password of the `grafana_reader` user. |
| `CLICKHOUSE_URL` | `http://clickhouse:8123` | ClickHouse HTTP endpoint used by the API. |
| `CLICKHOUSE_DATABASE` | `scrapelog` | Database name. |
| `CLICKHOUSE_INGEST_USER` | `ingest_writer` | ClickHouse user of the API. |
| `CLICKHOUSE_DISK_BUDGET_BYTES` | `25000000000` | Storage budget enforced by the API. See [Storage measure](#storage-measure). |
| `ADMIN_TOKEN` | none, required | Bearer token for `/v1/admin/*` routes and the `keys` command. |
| `MAX_BATCH_RECORDS` | `5000` | Maximum records per batch request. |
| `RATE_LIMIT_RECORDS_PER_MINUTE` | `600000` | Per-key ingest limit in records per minute (10000 per second on average). Must be at least `MAX_BATCH_RECORDS`. |
| `MAX_REQUEST_BYTES` | `10485760` | Maximum request size on the wire and, for the stream endpoint, the maximum size of one line. |
| `MAX_BODY_BYTES` | `2097152` | Maximum size of a request body or response body after decoding. |
| `REDACT_HEADERS` | `authorization,cookie,proxy-authorization` | Comma-separated header names to redact. See [Redaction](#redaction). |
| `S3_ENDPOINT` | empty | S3-compatible endpoint (Cloudflare R2 or MinIO) for response bodies. Empty disables body storage. |
| `S3_BUCKET` | empty | Bucket for response bodies. Required when `S3_ENDPOINT` is set. |
| `S3_ACCESS_KEY_ID` | empty | Access key id. Required when `S3_ENDPOINT` is set. |
| `S3_SECRET_ACCESS_KEY` | empty | Secret access key. Required when `S3_ENDPOINT` is set. |
| `S3_REGION` | `auto` | Signing region (`auto` for R2). |
| `S3_BODY_PREFIX` | `bodies/` | Object key prefix for response bodies. |
| `INGEST_API_PORT` | `8080` | Host port of the ingest API (loopback). |
| `GRAFANA_ADMIN_PASSWORD` | none, required | Password of the Grafana `admin` user. |
| `GRAFANA_ROOT_URL` | `http://localhost:3000/` | Public URL Grafana is served from. |
| `GRAFANA_PORT` | `3000` | Host port of Grafana (loopback). |
| `BACKUP_S3_URL` | empty | Bucket base URL for [backups](#backups). |
| `BACKUP_S3_ACCESS_KEY_ID` | empty | Access key id for backups. |
| `BACKUP_S3_SECRET_ACCESS_KEY` | empty | Secret access key for backups. |

## API reference

Ingest routes authenticate with an ingest key: `Authorization: Bearer <key>`. Admin routes use `Authorization: Bearer <ADMIN_TOKEN>`. The project of a record always comes from the key, never from the request body. Errors are JSON: `{"error": "<message>"}`.

### POST /v1/requests

Sends a batch: a JSON array of records, at most `MAX_BATCH_RECORDS` records and `MAX_REQUEST_BYTES` bytes on the wire. Send `Content-Encoding: gzip` or `zstd` to compress; a decoded body larger than four times `MAX_REQUEST_BYTES` is rejected.

Record fields:

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `url` | string | yes | Absolute URL. Credentials in the URL are stripped. |
| `method` | string | yes | HTTP method. |
| `ts` | RFC 3339 string | no | Defaults to the receive time. Rejected if more than 60 days old or more than 1 hour ahead. |
| `scraper` | string | no | Scraper name. |
| `request_id` | string | no | Defaults to a generated UUIDv7. See [Response bodies](#response-bodies). |
| `domain` | string | no | Defaults to the lowercased host of `url`. |
| `status` | integer | no | 0 to 999. Use 0 when no response was received. |
| `latency_ms` | integer | no | Unsigned 32 bit. |
| `proxy` | string | no | Credentials in the proxy URL are stripped. |
| `http_version`, `tls_ja3`, `tls_ja4`, `tls_profile` | string | no | Stored as given. |
| `request_headers`, `response_headers` | object of strings | no | Header values are redacted per `REDACT_HEADERS`. |
| `response_size` | integer | no | Unsigned 32 bit. |
| `error` | string | no | Error text. A row counts as an error when `status` is 0 or `error` is not empty. |
| `tags` | object of strings | no | Free-form labels. |
| `request_body`, `request_body_encoding` | string | no | See [Request bodies](#request-bodies). |
| `body`, `body_encoding` | string | no | Response body, stored in S3. `body_encoding` is `utf8` (default) or `base64`. |

Response (200): a summary with partial acceptance. Valid records are stored even when others are rejected.

```
{"accepted": 998, "rejected": 2, "errors": [{"index": 4, "error": "url is required"}], "warnings": [{"index": 7, "warning": "body upload failed"}]}
```

- `errors` lists rejected records by their index in the request. A record with a body that fails validation, for example invalid base64, is rejected.
- `warnings` lists accepted records whose response body was not stored (S3 not configured, upload failed or timed out, body above `MAX_BODY_BYTES`). The record itself is stored without a body.

Status codes:

| Status | Meaning |
| --- | --- |
| 200 | At least one record accepted. |
| 400 | No record accepted, the body is not a JSON array, or the gzip body is invalid. |
| 401 | Missing, unknown or revoked key. |
| 413 | Request over `MAX_REQUEST_BYTES`, more than `MAX_BATCH_RECORDS` records, or decoded size too large. |
| 415 | Unsupported `Content-Encoding`. |
| 429 | Rate limit exceeded. `Retry-After` holds the seconds to wait. |
| 503 | ClickHouse unavailable, an insert timed out, or a stream failed part way. `Retry-After` is set. Some rows may already have been stored; retry the batch. |
| 507 | The storage budget is exceeded. See [Storage measure](#storage-measure). |

Retries can store a row twice. Set `request_id` yourself on every record and resend the same ids when you retry, so a retried record keeps the id of the original.

### POST /v1/requests/stream

Newline-delimited JSON, one record per line. See [Streaming](#streaming).

### GET /v1/stats

Returns the calling key's project: `{"project", "row_count", "oldest_ts", "newest_ts", "estimated_bytes_on_disk"}`.

### GET /v1/health

No authentication. 200 `{"status":"ok"}` when ClickHouse answers, 503 otherwise.

### Admin routes

All need the admin token. The `scrapelog keys` command inside the `ingest-api` container calls the first three.

- `POST /v1/admin/keys` with `{"project": "...", "name": "..."}` returns 201 with `{"key_id", "key", "project"}`. `project` matches `^[a-z0-9][a-z0-9_-]{0,63}$`. The key is shown only in this response.
- `GET /v1/admin/keys` lists keys (`key_id`, `project`, `name`, `created_at`, `revoked`).
- `DELETE /v1/admin/keys/{key_id}` revokes a key (204).
- `GET /v1/admin/storage` returns `{"used_bytes", "budget_bytes", "projects": [{"project", "row_count", "estimated_bytes_on_disk"}]}`.

## Streaming

`POST /v1/requests/stream` accepts an NDJSON body (`Content-Type: application/x-ndjson`, `Transfer-Encoding: chunked`), one record per line, with the same record fields, limits and compression options as the batch endpoint.

- Records are written in groups: every 2000 lines or every 2 seconds, whichever comes first.
- The response is sent when the request body ends, with the same summary as the batch endpoint. Indexes are line numbers starting at 0, counting only non-empty lines. If a flush fails (storage budget, rate limit or ClickHouse), the stream stops and the response carries that status plus an `error` field; the summary shows what was committed before the failure.
- `MAX_REQUEST_BYTES` is also the maximum size of one line. A line of exactly `MAX_REQUEST_BYTES` bytes terminated by CRLF is answered with 413, because the limit counts the `\r`.
- Cloudflare closes a proxied request after about 100 seconds, so open a new connection regularly. The Python stream example reconnects every 60 seconds.

## Redaction

- `REDACT_HEADERS` is matched case-insensitively against the names in both `request_headers` and `response_headers`; matching values are stored as `[REDACTED]`.
- The default list (`authorization,cookie,proxy-authorization`) does not include `set-cookie`, so responses that set cookies are stored in full. If your scrapers send response headers, add it: `REDACT_HEADERS=authorization,cookie,proxy-authorization,set-cookie`. Setting the variable replaces the default list, it does not extend it.
- Credentials in `url` (`https://user:pass@host/`) and in `proxy` are removed before storing.
- Request bodies, response bodies, query strings and other header-like data in `tags` are not redacted. Anyone with Grafana access can read stored request bodies, so scrapers must not send secrets in bodies, URLs or tags.

## Grafana

- Log in at the Grafana URL as `admin` with the password in `GRAFANA_ADMIN_PASSWORD`.
- Set `GRAFANA_ROOT_URL` to the public URL Grafana is served from.
- Dashboards and the ClickHouse datasource are provisioned from `grafana/provisioning/` and are edited there, not in the UI.
- The 25 GB budget on the storage gauge is set in `grafana/provisioning/dashboards/overview.json`. Keep it equal to `CLICKHOUSE_DISK_BUDGET_BYTES`, the budget the ingest API enforces.
- Put Cloudflare Access in front of the Grafana hostname.

## Request bodies

- Records may carry `request_body` and `request_body_encoding` (`utf8`, the default, or `base64`). Base64 uses the standard alphabet and the size is measured after decoding.
- A body larger than `MAX_BODY_BYTES` (default 2097152) rejects that record with an error; it is never truncated.
- Bodies are stored verbatim and are visible to anyone with Grafana access, so scrapers must not put credentials in bodies. They are never logged.
- The `request_body` columns exist only in a freshly initialised ClickHouse volume; recreate the volume to pick them up.

## Response bodies

- Records may carry `body` (and `body_encoding`, `utf8` or `base64`). The API gzips the body and uploads it to the S3-compatible bucket configured with `S3_ENDPOINT`, `S3_BUCKET`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` and optionally `S3_REGION` and `S3_BODY_PREFIX`. The ClickHouse row stores the object key in `body_key`.
- Object key: `{S3_BODY_PREFIX}{project}/{yyyy}/{mm}/{dd}/{request_id}.gz`, with the date taken from the time the API received the record (UTC).
- Without `S3_ENDPOINT` the API still runs; records are stored and each body is dropped with a warning in the response.
- A body that cannot be stored is dropped with an entry in `warnings`; the record is never rejected for it.
- Bodies are stored verbatim and are not redacted.
- Send a short `request_id` using only `A-Za-z0-9._-`. Any other character becomes `_` in the object key, so two different ids can map to the same object, and S3 limits keys to 1024 bytes.
- Retention: add a lifecycle rule on the bucket for the `S3_BODY_PREFIX` prefix that expires objects after 14 days, matching the 14 day TTL on successful rows in ClickHouse. Use a different number if you change the [TTL](#ttl-tuning).

## Cloudflare and reverse proxy

`deploy/examples/Caddyfile` is an example of a Caddy site that sends `/v1/*` to the ingest API and everything else to Grafana, and passes the client address as `X-Real-IP`. Replace `clickhouse.example.com` with your hostname.

- Cloudflare Access protects the Grafana UI. It must not cover `/v1/*`: scrapers authenticate with API keys and cannot pass an Access login. Exclude `/v1/*` with a bypass policy, or serve the API on a separate hostname.
- To make `{client_ip}` the real visitor address behind Cloudflare, Caddy must trust Cloudflare's proxy ranges. Caddy's `trusted_proxies cloudflare` source comes from the `caddy-cloudflare-ip` module, which is not in stock Caddy. With stock Caddy, add a global option listing Cloudflare's published ranges:

```
{
	servers {
		trusted_proxies static <range> <range>
	}
}
```

- The ingest API does not use the client address today; `X-Real-IP` is passed for your proxy logs and future use.
- Cloudflare closes proxied requests after about 100 seconds; see [Streaming](#streaming).

## TTL tuning

`scrapelog.requests` expires rows by two rules: successful rows (status 200 to 399) after 14 days, and every row after 60 days. The current rules are in `clickhouse/initdb/001_schema.sql`. Change them on a running stack as the admin user:

```
docker compose exec clickhouse sh -c 'clickhouse-client --user scrapelog_admin --password "$CLICKHOUSE_PASSWORD" --query "ALTER TABLE scrapelog.requests MODIFY TTL toDateTime(ts) + INTERVAL 7 DAY DELETE WHERE status BETWEEN 200 AND 399, toDateTime(ts) + INTERVAL 30 DAY DELETE"'
```

Rules apply to existing rows after the change as ClickHouse rewrites the affected parts in the background. Update `clickhouse/initdb/001_schema.sql` to match so a recreated volume gets the same rules, and keep the bucket lifecycle rule for [response bodies](#response-bodies) in step. The `requests_hourly` aggregate is not subject to the TTL, so dashboards keep hourly history after raw rows expire.

## Storage measure

The budget in `CLICKHOUSE_DISK_BUDGET_BYTES` counts the bytes of active parts (`system.parts`) in the `scrapelog` and `system` databases, so it includes ClickHouse's own logs as well as your data. The API refreshes the figure every 30 seconds, logs a warning above 90 percent and answers ingest requests with 507 above 100 percent. `GET /v1/admin/storage` and the storage gauge in Grafana show the same number; keep the gauge's budget in sync with the variable (see [Grafana](#grafana)).

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

## Examples

All examples read `SCRAPELOG_KEY` (required) and `SCRAPELOG_URL` (default `http://127.0.0.1:8080`), and print the JSON summary. They use only the standard library of each language.

- Python batch, 1000 records gzip-compressed: `SCRAPELOG_KEY=... python3 examples/python/send_batch.py`
- Python stream, about 200 records per second, reconnecting every 60 seconds until interrupted: `SCRAPELOG_KEY=... python3 examples/python/send_stream.py`
- Node.js 18 or newer, batch: `SCRAPELOG_KEY=... node examples/node/send_batch.mjs`
- Go, batch: `cd examples/go && SCRAPELOG_KEY=... go run send_batch.go`

The batch examples include an `Authorization` request header to show that it is stored as `[REDACTED]`. Check delivery with `curl -H "Authorization: Bearer $SCRAPELOG_KEY" $SCRAPELOG_URL/v1/stats`.

## Load test

`ingest-api/cmd/loadtest` measures ingest throughput. It creates its own keys in project `loadtest` through the admin API, sends batches in parallel (one sender per key, paced to the target rate) and revokes the keys when it ends, including on failure and on Ctrl-C.

```
cd ingest-api
ADMIN_TOKEN=<token> go run ./cmd/loadtest -url http://127.0.0.1:8080 -keys 6 -rate 50000 -duration 2m
```

Flags: `-url`, `-keys` (6), `-rate` total records per second (50000), `-duration` (2m), `-batch` records per request (5000, the default `MAX_BATCH_RECORDS`). Each key sends about 8300 records per second, under the 10000 per second per-key limit; keep `-rate` divided by `-keys` below that or expect 429 responses.

It prints one JSON line with `target_rate`, `achieved_rate` (accepted records divided by elapsed seconds), `duration_seconds`, `accepted`, `rejected`, `status_counts` and `transport_errors`, and exits 0 only if the achieved rate is at least 95 percent of the target and there were no non-200 responses, transport errors or rejected records. A shortfall usually means the host is the limit (CPU, disk or a busy machine), not the API: check the load average and `docker stats` while it runs.

Revoked keys keep working for up to 35 seconds because of the key cache. The test leaves its rows in the `loadtest` project; remove them with `ALTER TABLE scrapelog.requests DELETE WHERE project = 'loadtest'` as the admin user, or run it against a scratch stack and `docker compose down -v`.

## Dependencies

- The ingest API's direct dependencies are `clickhouse-go/v2`, `klauspost/compress` and `aws-sdk-go-v2` (`service/s3` and `credentials`).
- `klauspost/compress` is used only to decode `Content-Encoding: zstd` request bodies, because Go's standard library has no zstd. It is already an indirect dependency of `clickhouse-go`.
- `aws-sdk-go-v2` uploads response bodies to S3-compatible storage (Cloudflare R2, MinIO), which needs AWS Signature V4 request signing that the standard library does not provide.
