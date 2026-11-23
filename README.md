# scrapelog

scrapelog collects one row per HTTP request your scrapers make. Scrapers send records to an ingest API, the API stores them in ClickHouse, and the API also serves a web UI for searching requests, charting bandwidth and sending alerts. Everything runs from one Docker Compose file.

## Quickstart

1. Copy the environment file and fill in the secrets:

```
cp .env.example .env
openssl rand -base64 32
```

   Run the second command once per empty value and paste the result: `CLICKHOUSE_ADMIN_PASSWORD`, `CLICKHOUSE_INGEST_PASSWORD`, `CLICKHOUSE_READER_PASSWORD`, `CLICKHOUSE_ARCHIVER_PASSWORD` and `ADMIN_TOKEN`. Use `openssl rand -base64 32 | tr -d '\n/+='` to avoid characters that need quoting. `.env` is gitignored; never commit it.

2. Start the stack and wait for every healthcheck:

```
docker compose up -d --wait
```

3. Create an ingest key for a project. The key is printed once; only its SHA-256 hash is stored:

```
docker compose exec ingest-api /scrapelog keys create --project my-project --name my-scraper
```

4. Send data with one of the [examples](#examples), using the printed `key` as `SCRAPELOG_KEY`.
5. Open the UI at `http://127.0.0.1:8080` (or your `INGEST_API_PORT`) and sign in with username `admin` and `ADMIN_TOKEN` as the password. See [Web UI](#web-ui).

Upgrades: v1 has no schema migrations. The ClickHouse init script in `clickhouse/initdb/` runs only when the data volume is empty, so a column added by a newer version (for example the request body columns) exists only in a freshly initialised volume. To pick up a schema change, take a [backup](#backups) if you need the data, then recreate the volume with `docker compose down -v && docker compose up -d --wait`. This deletes all stored requests and API keys. To upgrade an install from before the web UI without losing data, follow [Upgrading an existing install](#upgrading-an-existing-install).

## Upgrading an existing install

1. Take a [backup](#backups).
2. Pull the new version.
3. Edit `.env`:
   - Add `CLICKHOUSE_ARCHIVER_PASSWORD`. It is required; Compose refuses to start without it.
   - Delete `GRAFANA_ADMIN_PASSWORD`, `GRAFANA_ROOT_URL` and `GRAFANA_PORT`.
   - Optionally set `SCRAPELOG_PUBLIC_URL`.
   - Make sure `S3_ENDPOINT` has no trailing slash.
4. Recreate ClickHouse so it picks up the new environment, `config.d/archive-s3.xml` and `users.xml` (the `ui_reader` and `archiver` users and the `ingest_writer` grant on `scrapelog.users`; `grafana_reader` is removed):

```
docker compose up -d --wait --force-recreate clickhouse
```

5. Create the new tables. The files in `clickhouse/initdb/` run only on an empty volume, so apply them by hand, in order:

```
docker compose exec -T clickhouse clickhouse-client --multiquery < clickhouse/initdb/002_alerts.sql
docker compose exec -T clickhouse clickhouse-client --multiquery < clickhouse/initdb/003_archive.sql
docker compose exec -T clickhouse clickhouse-client --multiquery < clickhouse/initdb/004_restore.sql
docker compose exec -T clickhouse clickhouse-client --multiquery < clickhouse/initdb/005_users.sql
```

   Apply steps 4 and 5 before restarting `ingest-api`. Otherwise sign-in for user accounts returns 503 until you apply them and restart, while ingest and the `admin` sign-in keep working. Everyone signs in again once after this upgrade.

   Each fails with "already exists" if that file was applied before; that is harmless.

6. Rebuild the API and remove the old Grafana container:

```
docker compose up -d --build --remove-orphans --wait
```

7. Delete Grafana's data volume: `docker volume rm <project>_grafana_data`. The project is the directory name by default, so usually `scrapelog_grafana_data`.
8. Update the reverse proxy to the single route in `deploy/examples/Caddyfile`, keeping `header_up X-Real-IP {client_ip}`, and add `/docs` to the Cloudflare Access bypass.
9. If you later restore a database from a backup, re-create `requests_restored` (see [Restore](#restore)).

The Grafana dashboards have no direct replacement panel for disk use, latency percentiles or top-error tables: use Requests filters, Bandwidth splits and `GET /v1/admin/storage`.

## Architecture

- `clickhouse`: a single ClickHouse node. It publishes no host port and is reachable only on the `scrapelog` Docker network.
- `ingest-api`: the Go HTTP API. It is published on `127.0.0.1:${INGEST_API_PORT:-8080}`. It also serves the web UI and `/docs`, and runs the alert dispatcher and the hourly archive job.
- Host ports bind to loopback only. To expose them, put a reverse proxy in front (see [Cloudflare and reverse proxy](#cloudflare-and-reverse-proxy)).
- ClickHouse users, all with passwords from `.env`:
  - `ingest_writer` (used by `ingest-api`): can insert into and read `scrapelog.requests`, `scrapelog.api_keys`, `scrapelog.alert_rules`, `scrapelog.alert_events` and `scrapelog.users`, and read `system.parts` for the storage measure.
  - `ui_reader` (used by the web UI): read-only on `scrapelog.requests`, `scrapelog.requests_hourly` and `scrapelog.requests_restored`, with a 60 s, 2 GB and 100,000-row limit per query.
  - `archiver` (used by the archive and restore jobs): reads `scrapelog.requests`; writes `archive_days`, `requests_restored` and `archive_restores`; reads and writes S3 only under the bucket's `archive/requests/` prefix through the `archive_s3` named collection; each query is limited to 1 GiB, 2 threads and 900 s.
  - `scrapelog_admin`: full access, for operations such as [TTL changes](#ttl-tuning) and restores.

## Configuration

All configuration is environment variables in `.env`; Compose passes them to the containers.

| Variable | Default | Meaning |
| --- | --- | --- |
| `CLICKHOUSE_ADMIN_PASSWORD` | none, required | Password of the `scrapelog_admin` user. |
| `CLICKHOUSE_INGEST_PASSWORD` | none, required | Password of the `ingest_writer` user. |
| `CLICKHOUSE_READER_PASSWORD` | none, required | Password of the `ui_reader` user. |
| `CLICKHOUSE_URL` | `http://clickhouse:8123` | ClickHouse HTTP endpoint used by the API. |
| `CLICKHOUSE_DATABASE` | `scrapelog` | Database name. |
| `CLICKHOUSE_INGEST_USER` | `ingest_writer` | ClickHouse user of the API. |
| `CLICKHOUSE_READER_USER` | `ui_reader` | ClickHouse user the UI reads with. |
| `CLICKHOUSE_ARCHIVER_PASSWORD` | none, required | Password of the `archiver` user. Required even without S3. |
| `CLICKHOUSE_ARCHIVER_USER` | `archiver` | ClickHouse user of the archive and restore jobs. |
| `CLICKHOUSE_DISK_BUDGET_BYTES` | `25000000000` | Storage budget enforced by the API. See [Storage measure](#storage-measure). |
| `ADMIN_TOKEN` | none, required | Bearer token for `/v1/admin/*` routes and the `keys` command. Also the password of the `admin` UI sign-in and the key that signs UI sessions. |
| `SCRAPELOG_PUBLIC_URL` | empty | Public base URL of the UI, for example `https://scrapelog.example.com`. Alert messages link back to it; empty leaves the link out. |
| `MAX_BATCH_RECORDS` | `5000` | Maximum records per batch request. |
| `RATE_LIMIT_RECORDS_PER_MINUTE` | `600000` | Per-key ingest limit in records per minute (10000 per second on average). Must be at least `MAX_BATCH_RECORDS`. |
| `MAX_REQUEST_BYTES` | `10485760` | Maximum request size on the wire and, for the stream endpoint, the maximum size of one line. |
| `MAX_BODY_BYTES` | `2097152` | Maximum size of a request body or response body after decoding. |
| `REDACT_HEADERS` | `authorization,cookie,proxy-authorization` | Comma-separated header names to redact. See [Redaction](#redaction). |
| `S3_ENDPOINT` | empty | S3-compatible endpoint (Cloudflare R2 or MinIO) for response bodies and [archives](#archive). No trailing slash: with one, archiving is disabled and a warning is logged. Empty disables both. |
| `S3_BUCKET` | empty | Bucket for response bodies. Required when `S3_ENDPOINT` is set. |
| `S3_ACCESS_KEY_ID` | empty | Access key id. Required when `S3_ENDPOINT` is set. |
| `S3_SECRET_ACCESS_KEY` | empty | Secret access key. Required when `S3_ENDPOINT` is set. |
| `S3_REGION` | `auto` | Signing region (`auto` for R2). |
| `S3_BODY_PREFIX` | `bodies/` | Object key prefix for response bodies. |
| `INGEST_API_PORT` | `8080` | Host port of the ingest API (loopback). |
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

### GET /docs

No authentication. An HTML page for scraper authors: authentication, record fields, batch and stream usage, status codes and examples. Example URLs use the request's `Host`, `X-Forwarded-Proto` and `X-Forwarded-Prefix`, so a proxy that serves the API under a path prefix (for example `/api`) should set `X-Forwarded-Prefix` to that prefix.

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
- Request bodies, response bodies, query strings and other header-like data in `tags` are not redacted. Anyone who can sign in with `view_requests` can read stored request bodies, so scrapers must not send secrets in bodies, URLs or tags.

## Web UI

### Sign in

Open `/` (it goes to the sign-in page, or to your first permitted page when you are already signed in) and sign in as `admin` with `ADMIN_TOKEN` as the password, or as a user account with its password. Permissions are checked on every request:

- A permission change applies on the user's next request.
- A password reset or a disable ends that user's sessions.
- An admin session ends only on expiry or when `ADMIN_TOKEN` is rotated. Rotating signs everyone out, and users sign back in with their unchanged password.
- Signing out clears the browser cookie but does not revoke a copied cookie.
- User changes assume a single `ingest-api` instance.

The session cookie lasts 7 days and is marked `Secure` behind HTTPS (`X-Forwarded-Proto: https`). Sign-in allows 10 attempts per minute per client address; see [Cloudflare and reverse proxy](#cloudflare-and-reverse-proxy) for why the proxy must set `X-Real-IP`.

### Requests

- A query bar, a time range (default the last hour), a status tape and a result list. Clicking a row opens its headers, request body and, when S3 is configured, its response body.
- Terms are joined with AND. An unknown field returns an error that lists the valid ones.

| Term | Matches |
| --- | --- |
| `project:` `scraper:` `domain:` `method:` `proxy:` `ja3:` `ja4:` | exact value |
| `tls:` `http:` | `tls_profile` and `http_version`, exact value |
| `status:` | `404`, `400..499` or `4xx` |
| `latency:` `size:` | `>500`, `<500` or `100..500` (milliseconds and response bytes) |
| `url~` `error~` | contains, case-insensitive |
| `res.<header>` `req.<header>` | `:` exact, `~` contains |
| `tag.<name>` | tag value, `:` exact, `~` contains |
| `id:` | request id |
| `-term` | negates a term |
| `word`, `"quoted words"` | URL contains |

- A bare word with a colon is read as `field:value`, so `http://shop.example.com` is the field `http`. Quote URLs: `"http://shop.example.com/p"`.
- Successful rows expire after 14 days. For a range that starts earlier the list shows a note, and the status tape switches to the hourly rollup. The rollup can filter only on `status`, `project`, `scraper`, `domain`, `proxy` and `tls`; other terms are ignored by the tape, which is then marked approximate.
- Download: the current query as NDJSON or CSV, the newest 100,000 rows at most. The browser holds the download in memory.

### Bandwidth

Bytes (`response_size`) and responses over time from `requests_hourly`, so history is kept after raw rows expire. Split by `proxy` (the default), `project`, `scraper` or `domain`, showing the top 8 plus "Other". Hourly or daily steps; the range is aligned to the step.

### Alerts

- A rule has a name, an optional project chosen from a list (names come from API keys; empty means all projects), one or more conditions (all must match), a channel (Discord webhook, or Telegram bot token plus chat id) and a window from 10 to 3600 s (default 60).

| Field | Operators |
| --- | --- |
| `status` | `equals`, `not_equals`, `between`, `greater_than`, `less_than`, `class` |
| `latency_ms`, `response_size` | `equals`, `between`, `greater_than`, `less_than` |
| `method`, `domain`, `scraper`, `proxy`, `tls_profile` | `equals`, `not_equals` |
| `url`, `error`, `body` | `contains`, `not_contains`, `regex` |
| `response_header`, `request_header`, `tag` (with a name) | `equals`, `contains`, `not_contains`, `regex`, `exists`, `missing` |

- Text matching is case-insensitive except regex (use `(?i)`). Regex is Go RE2, checked on save.
- The first match opens the window, and one message per window is sent with the match count and up to 3 samples. It carries an "Open in scrapelog" link when `SCRAPELOG_PUBLIC_URL` is set.
- Only rows that were stored alert. Matching runs on ingest, so restored rows never alert.
- Body conditions are checked last and scan the first 1 MiB of the decoded body, so a body between 1 MiB and `MAX_BODY_BYTES` (2 MiB) is only partly scanned. They work without S3.
- Cost: contains and literal-alternation regexes are cheap. Other body regexes run inline in the ingest request at roughly 30-60 MB/s per core, so keep them for low-volume projects.
- Up to 10,000 matches are queued; past that they are dropped and the next message says how many. A 429 from Discord or Telegram is retried once.
- The Test button sends a sample message.
- Sent and failed deliveries are listed under the rule and kept 30 days (`alert_events`).
- Secrets at rest: webhook URLs and bot tokens are stored in plain text in `scrapelog.alert_rules` and so are in every backup. The API never returns them and they are never logged. Keep the backup bucket private.

### Projects and API keys

- Only the admin sees the Projects page (`/projects`) and can use `GET` and `POST` on `/ui/api/keys` and `DELETE` on `/ui/api/keys/{key_id}`; every other signed-in account gets a 403. They are the session counterparts of the `/v1/admin/keys` routes.
- A project exists once it has a key, so "New project" creates the project together with its first key. Project removal is not supported; revoke its keys instead.
- The full key is shown once, right after it is created, and cannot be retrieved later. Copy it before dismissing the panel or leaving the page.
- Revoking asks for confirmation. Revoked keys can keep working for up to 35 seconds on other instances because of the key cache.

### Users

- Only the admin sees the Users page (`/users`) and can use `GET`, `POST` and `PUT` on `/ui/api/users`; every other signed-in account gets a 403.
- Add a user with a username (or an email address, lowercased on save) and a password of at least 12 characters that you set and share yourself. `admin` is reserved for the `ADMIN_TOKEN` sign-in.
- Edit a user to change permissions, set a new password (leave it blank to keep the current one) or disable the account. There is no delete: a disabled user cannot sign in, and the account and its name stay.
- Permissions:
  - `view_requests`: the Requests page, request bodies and exports.
  - `view_bandwidth`: the Bandwidth page.
  - `view_alerts`: see alert rules and fired alerts.
  - `manage_alerts`: create, change, test and delete alert rules (includes viewing).
  - `view_archives`: list and download archives.
  - `estimate_restores`: estimate an archive restore.
  - `manage_restores`: start and delete restores (includes the estimate).

### Archive page

Archived days and restores are on the Archive page; see [Archive](#archive).

### App icon

The UI is installable as a web app (Add to Home Screen, or Install in Chrome) from an HTTPS address. The icon is `ingest-api/internal/ui/assets/icon.svg`. The PNG sizes next to it are derived from it and committed, so there is no build step. To regenerate them after changing the SVG, run this from `ingest-api/internal/ui/assets`:

```
for size in 180 192 512; do magick -background none -density 288 icon.svg -resize ${size}x${size} -strip icon-${size}.png; done
```

### Disk use

The UI does not chart disk use. Use `GET /v1/admin/storage`.

## Request bodies

- Records may carry `request_body` and `request_body_encoding` (`utf8`, the default, or `base64`). Base64 uses the standard alphabet and the size is measured after decoding.
- A body larger than `MAX_BODY_BYTES` (default 2097152) rejects that record with an error; it is never truncated.
- Bodies are stored verbatim and are visible to anyone who can sign in with `view_requests`, so scrapers must not put credentials in bodies. They are never logged.
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

`deploy/examples/Caddyfile` is an example of a Caddy site that sends the whole host to the ingest API (the API, `/docs` and the UI) and sets `X-Real-IP` to the client address. Replace `clickhouse.example.com` with your hostname.

- Keep `header_up X-Real-IP {client_ip}`. It overwrites any `X-Real-IP` the client sent. The sign-in rate limit keys on it; without it a client can choose its own address, bypass the limit and grow the limiter's memory. The API trusts the header because its port is bound to loopback, so only the proxy can reach it.
- Cloudflare Access can protect the UI as a second login layer. It must not cover `/v1/*` or `/docs`: scrapers authenticate with API keys and cannot pass an Access login, and `/docs` is meant for scraper authors. Exclude both with a bypass policy, or serve the API on a separate hostname.
- To make `{client_ip}` the real visitor address behind Cloudflare, Caddy must trust Cloudflare's proxy ranges. Caddy's `trusted_proxies cloudflare` source comes from the `caddy-cloudflare-ip` module, which is not in stock Caddy. With stock Caddy, add a global option listing Cloudflare's published ranges:

```
{
	servers {
		trusted_proxies static <range> <range>
	}
}
```

- Cloudflare closes proxied requests after about 100 seconds; see [Streaming](#streaming).

## TTL tuning

`scrapelog.requests` expires rows by two rules: successful rows (status 200 to 399) after 14 days, and every row after 60 days. The current rules are in `clickhouse/initdb/001_schema.sql`. Change them on a running stack as the admin user:

```
docker compose exec clickhouse sh -c 'clickhouse-client --user scrapelog_admin --password "$CLICKHOUSE_PASSWORD" --query "ALTER TABLE scrapelog.requests MODIFY TTL toDateTime(ts) + INTERVAL 7 DAY DELETE WHERE status BETWEEN 200 AND 399, toDateTime(ts) + INTERVAL 30 DAY DELETE"'
```

Rules apply to existing rows after the change as ClickHouse rewrites the affected parts in the background. Update `clickhouse/initdb/001_schema.sql` to match so a recreated volume gets the same rules, and keep the bucket lifecycle rule for [response bodies](#response-bodies) in step. The `requests_hourly` aggregate is not subject to the TTL, so Bandwidth keeps hourly history after raw rows expire.

## Storage measure

The budget in `CLICKHOUSE_DISK_BUDGET_BYTES` counts the bytes of active parts (`system.parts`) in the `scrapelog` and `system` databases, so it includes ClickHouse's own logs as well as your data. The API refreshes the figure every 30 seconds, logs a warning above 90 percent and answers ingest requests with 507 above 100 percent. `GET /v1/admin/storage` shows the same number. Restores are refused when they would take it past 80 percent; see [Archive](#archive).

## Backups

- Set `BACKUP_S3_URL` (bucket base URL ending in `/`, for example `https://<account>.r2.cloudflarestorage.com/<bucket>/`), `BACKUP_S3_ACCESS_KEY_ID` and `BACKUP_S3_SECRET_ACCESS_KEY` in `.env`, then run `docker compose up -d clickhouse` to recreate ClickHouse with them.
- `scripts/backup.sh` writes a full backup of the `scrapelog` database to `backups/scrapelog-<UTC timestamp>` in the bucket and exits non-zero on failure. Run it once by hand to check the credentials.
- The S3 credentials live in a ClickHouse named collection (`clickhouse/config.d/backup-s3.xml`), so they never appear in the SQL statement, the query log or the command line.
- The backup includes the `api_keys` hashes, so keep the bucket private. It skips `scrapelog.requests_restored`, which holds only temporary restored rows.
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

1. Deploy the stack on the target with the same `.env` (all passwords and `BACKUP_S3_*`) and run `docker compose up -d clickhouse`. The init script creates an empty `scrapelog` database. Stop `ingest-api` if it is running.
2. Drop the empty database:

```
docker compose exec -T clickhouse clickhouse-client --query "DROP DATABASE scrapelog SYNC" </dev/null
```

3. Restore the backup:

```
docker compose exec -T clickhouse clickhouse-client --query "RESTORE DATABASE scrapelog FROM S3(backup_s3, filename = '<name>')" </dev/null
```

4. Re-create the restored-rows table, which backups skip:

```
sed -n '1,/;$/p' clickhouse/initdb/004_restore.sql | docker compose exec -T clickhouse clickhouse-client --multiquery
```

5. Start everything: `docker compose up -d`.

Restored API keys take effect once the API key cache expires (35 seconds). Users and grants come from `clickhouse/users.d/users.xml`, not from the backup. Restores that were `ready` before the backup show no rows; delete them on the Archive page.

## Archive

- Raw `requests` rows are copied to S3 before the TTL deletes them, one Parquet file per UTC day at `<S3_BUCKET>/archive/requests/<YYYY-MM-DD>.parquet`.
- It requires `S3_ENDPOINT` (no trailing slash), `S3_BUCKET` and the keys. The keys are also passed to the `clickhouse` container, which writes the files itself through the `archive_s3` named collection (`clickhouse/config.d/archive-s3.xml`). Without S3 the job does nothing and the Archive page says archiving is off.
- Schedule: an hourly job inside `ingest-api`. Each day is archived once it is 2 days old, and again at 12 days to pick up late rows. The first run backfills to 58 days back: complete days first, then days already past the 14-day success TTL, which are archived as partial ("failures only") because their successful rows are gone.
- Gap: rows that arrive more than 12 days late (ingest accepts up to 60), or after a day's second pass, are not archived.
- Bodies are not archived. Archived rows keep `body_key`, which stops resolving once the `bodies/` lifecycle rule deletes the object.
- Retention: archives are kept forever. To expire them, add a bucket lifecycle rule on the `archive/` prefix.
- The Archive page lists archived days with row counts and partial flags, and downloads a day's Parquet file through the server.

### Restoring an archived range

- Pick a range, optionally a project and a domain. The page shows an estimate of rows and bytes first.
- A range covers at most 31 days. A restore is refused if used bytes plus the estimate would exceed 80 percent of `CLICKHOUSE_DISK_BUDGET_BYTES`. ClickHouse stops each restore query after 900 s; restore a smaller range if it times out.
- Restores run one at a time. Restored rows land in `scrapelog.requests_restored`, can be browsed in Requests by picking the restore, can be deleted on the Archive page, and expire after 7 days regardless.
- They never feed `requests_hourly`, Bandwidth or alerts.
- A failed or deleted restore leaves no rows.

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

Flags: `-url`, `-keys` (6), `-rate` total records per second (50000), `-duration` (2m), `-batch` records per request (5000, the default `MAX_BATCH_RECORDS`), `-body-bytes` size of a text response body added to every record (0, no bodies). With bodies and S3 configured the rows carry a `body_key`; without S3 each body is dropped with a warning. Keep `-batch` times `-body-bytes` under `MAX_REQUEST_BYTES`, or batches are rejected. Each key sends about 8300 records per second, under the 10000 per second per-key limit; keep `-rate` divided by `-keys` below that or expect 429 responses.

It prints one JSON line with `target_rate`, `achieved_rate` (accepted records divided by elapsed seconds), `duration_seconds`, `accepted`, `rejected`, `status_counts`, `transport_errors` and `p95_ms` (95th percentile request latency per batch in milliseconds, measured to the response headers over all requests that got a response), and exits 0 only if the achieved rate is at least 95 percent of the target and there were no non-200 responses, transport errors or rejected records. A shortfall usually means the host is the limit (CPU, disk or a busy machine), not the API: check the load average and `docker stats` while it runs.

Revoked keys keep working for up to 35 seconds because of the key cache. The test leaves its rows in the `loadtest` project; remove them with `ALTER TABLE scrapelog.requests DELETE WHERE project = 'loadtest'` as the admin user, or run it against a scratch stack and `docker compose down -v`.

## Dependencies

- The ingest API's direct dependencies are `clickhouse-go/v2`, `klauspost/compress` and `aws-sdk-go-v2` (`service/s3` and `credentials`).
- `klauspost/compress` is used only to decode `Content-Encoding: zstd` request bodies, because Go's standard library has no zstd. It is already an indirect dependency of `clickhouse-go`.
- The UI has no build step and no npm dependencies. It vendors uPlot (MIT, about 50 KB, no dependencies) for the Bandwidth chart, because hand-drawing a stacked, zoomable time-series chart is more code than the library. It also vendors the Atkinson Hyperlegible Next and Mono fonts (OFL, about 150 KB). Both are embedded in the binary under `ingest-api/internal/ui/assets/`.
- `aws-sdk-go-v2` uploads response bodies to S3-compatible storage (Cloudflare R2, MinIO), which needs AWS Signature V4 request signing that the standard library does not provide.

## Integration test

- Requires Docker with Compose v5 and network access to pull and build images. It needs no `.env`.
- Run: `cd ingest-api && go test -tags integration -count=1 -timeout 15m ./integration/`
- It starts its own isolated stack, ingests 1000 rows, checks project isolation and key revocation, signs in to the UI, searches, fires an alert rule at a local fake webhook and reads the event back, greps the logs for secrets, then removes the stack and its volumes.
- Set `SCRAPELOG_IT_PORT` to choose the host port for the API; otherwise a free one is used. A plain `go test ./...` does not run it.
