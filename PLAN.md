# scrapelog — Build Plan

> History: this is the v1 build plan. The UI moved from Grafana to a built-in UI; see `UI_PLAN.md`. Sections describing Grafana record the original build.

A self-hostable, open-source service for logging every HTTP request your scrapers make: URL, status, headers, TLS fingerprint, proxy, latency, errors, and optionally response bodies. Scrapers on any server push batches over a public HTTPS API guarded by per-project API keys. Data lands in ClickHouse; Grafana provides the UI.

The repo will be **public on GitHub**. Anyone should be able to clone it, fill in a `.env`, run `docker compose up -d`, and have it working. Nothing specific to the author's machine goes into the core repo; host-specific deployment notes live in `deploy/examples/`.

> Instructions for the building agent: read this whole file before writing code. Follow the decisions below; if something is ambiguous or a decision looks wrong once you're in the code, stop and ask instead of improvising. Use descriptive variable names and do not add code comments (project convention). Verify ClickHouse/Grafana setting names against current official docs before relying on them — names drift between versions.

---

## 1. Decisions (already made — don't relitigate)

| Topic | Decision |
|---|---|
| Storage engine | ClickHouse (single node, Docker) |
| Ingest API language | Go (single static binary, small memory footprint, good at batching/streaming) |
| UI | Built-in web UI served by `ingest-api` (replaced Grafana; see `UI_PLAN.md`) |
| ClickHouse disk budget | **25 GB** for ClickHouse data |
| ClickHouse memory cap | **6 GB container limit**, server tuned to stay under ~5 GB |
| API exposure | Public over HTTPS, every request requires a project API key |
| ClickHouse exposure | **Never public.** Only reachable on the internal Docker network |
| Response bodies | Optional. Stored in S3-compatible object storage (Cloudflare R2, MinIO, AWS S3), not in ClickHouse |
| Backups | Nightly ClickHouse `BACKUP` to the same S3-compatible storage (separate prefix) |
| Volume | Unknown. Design for small-to-medium, measure after the first week, then tune TTL (section 8) |
| Reverse proxy | Out of scope for the core stack. Ship an example Caddyfile; users bring their own proxy |

---

## 2. Repository layout

```
scrapelog/
├── README.md                     quickstart, architecture diagram, API reference, client examples
├── LICENSE                       MIT
├── .env.example                  every config var, with safe placeholder values
├── docker-compose.yml            clickhouse, ingest-api, grafana
├── clickhouse/
│   ├── config.d/
│   │   └── low-memory.xml        server memory/cache/pool tuning (section 6)
│   ├── users.d/
│   │   └── users.xml             ingest_writer + grafana_reader users, profiles, quotas
│   └── initdb/
│       └── 001_schema.sql        database, tables, materialized views, TTLs
├── ingest-api/
│   ├── Dockerfile                multi-stage, distroless/static final image
│   ├── go.mod
│   ├── cmd/scrapelog/main.go     "serve" and "keys" subcommands
│   └── internal/...              config, auth, ingest handlers, clickhouse client, s3 upload
├── grafana/
│   └── provisioning/
│       ├── datasources/clickhouse.yml
│       └── dashboards/
│           ├── dashboards.yml
│           ├── overview.json
│           └── request-explorer.json
├── scripts/
│   └── backup.sh                 runs BACKUP DATABASE to S3; invoked by cron/systemd timer
├── examples/
│   ├── python/send_batch.py
│   ├── node/send_batch.ts
│   └── go/send_batch.go
└── deploy/
    └── examples/
        └── Caddyfile             example: grafana at /, api at /v1/*
```

---

## 3. Architecture

```
scrapers (any server)
   │  HTTPS  Authorization: Bearer sl_<key>
   ▼
reverse proxy (user's own: Caddy / nginx / Cloudflare)
   ├── /v1/*  ─► ingest-api :8080 ──► clickhouse :8123 (internal network only)
   │                 └──► S3-compatible bucket (bodies, optional)
   └── /      ─► grafana :3000 ─────► clickhouse (read-only user)

nightly: scripts/backup.sh ─► clickhouse BACKUP ─► S3 bucket /backups/
```

docker-compose details:
- One internal network `scrapelog`. ClickHouse publishes **no** host ports.
- `ingest-api` publishes `127.0.0.1:${INGEST_API_PORT:-8080}:8080`.
- `grafana` publishes `127.0.0.1:${GRAFANA_PORT:-3000}:3000`.
- Binding to 127.0.0.1 means a reverse proxy is required for public access. Document this; don't bind to 0.0.0.0 by default.
- Named volumes: `clickhouse_data`, `grafana_data`.
- `restart: unless-stopped` on all services. Healthchecks on all three; `ingest-api` and `grafana` `depends_on` clickhouse being healthy.
- Pin image versions (a current ClickHouse LTS, current Grafana OSS). No `latest` tags.

---

## 4. ClickHouse schema (`clickhouse/initdb/001_schema.sql`)

Database: `scrapelog`.

### 4.1 `requests` (main table)

| Column | Type | Notes |
|---|---|---|
| `received_at` | `DateTime64(3)` | set by the API |
| `ts` | `DateTime64(3)` | when the request was made, sent by the client; API defaults it to `received_at` |
| `project` | `LowCardinality(String)` | **set from the API key, never from the client** |
| `scraper` | `LowCardinality(String)` | client-supplied name of the scraper/job |
| `request_id` | `String` | client-supplied or API-generated UUIDv7 |
| `method` | `LowCardinality(String)` | |
| `url` | `String` | |
| `domain` | `LowCardinality(String)` | API derives it from `url` when not supplied |
| `status` | `UInt16` | 0 means no response (network error / timeout) |
| `latency_ms` | `UInt32` | |
| `proxy` | `LowCardinality(String)` | proxy id/label, **never proxy credentials** — API strips `user:pass@` if present |
| `http_version` | `LowCardinality(String)` | |
| `tls_ja3` | `LowCardinality(String)` | |
| `tls_ja4` | `LowCardinality(String)` | |
| `tls_profile` | `LowCardinality(String)` | free-form label, e.g. `chrome_131` |
| `request_headers` | `Map(LowCardinality(String), String)` | |
| `response_headers` | `Map(LowCardinality(String), String)` | |
| `response_size` | `UInt32` | bytes |
| `body_key` | `String` | object key in S3 when a body was stored, else empty |
| `error` | `String` | error message when the request failed |
| `tags` | `Map(LowCardinality(String), String)` | project-specific extra fields, so projects never need schema changes |

Engine and keys:
```sql
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (project, domain, ts)
TTL toDateTime(ts) + INTERVAL 14 DAY DELETE WHERE status BETWEEN 200 AND 399,
    toDateTime(ts) + INTERVAL 60 DAY DELETE
SETTINGS ttl_only_drop_parts = 0
```
- `ORDER BY (project, domain, ts)`: almost every query filters by project first, then domain and a time range.
- Daily partitions so TTL deletes are cheap and old data drops cleanly.
- Use `ZSTD(3)` codec on `url`, `request_headers`, `response_headers`, `error`; `Delta, ZSTD` on `ts`/`received_at`. Confirm codec syntax against current docs.
- Starting TTL values are defaults for a 25 GB budget with unknown volume. See section 8 for how to tune them. Make them easy to change: document the exact `ALTER TABLE ... MODIFY TTL` statement in the README.

### 4.2 `requests_hourly` (rollup, kept forever)

`SummingMergeTree` (or `AggregatingMergeTree` if quantiles are included), filled by a materialized view from `requests`:
- keys: `hour` (`toStartOfHour(ts)`), `project`, `scraper`, `domain`, `status`, `tls_profile`, `proxy`
- values: `request_count`, `error_count`, `total_latency_ms`, `total_response_size`, plus a latency quantile state (`quantilesState(0.5, 0.95, 0.99)(latency_ms)`) if `AggregatingMergeTree` is used
- no TTL. This keeps long-term trends after raw rows expire. It is tiny.

### 4.3 `api_keys`

`ReplacingMergeTree(updated_at)`:
- `key_id` (String, short public id, e.g. `sl_ab12cd`), `key_hash` (String, SHA-256 hex of the full secret), `project` (LowCardinality String), `name`, `created_at`, `updated_at`, `revoked` (UInt8)
- the plaintext key is shown **once** at creation and never stored

---

## 5. Ingest API (Go)

### 5.1 Config (env vars, all in `.env.example`)

```
CLICKHOUSE_URL=http://clickhouse:8123
CLICKHOUSE_DATABASE=scrapelog
CLICKHOUSE_INGEST_USER=ingest_writer
CLICKHOUSE_INGEST_PASSWORD=
ADMIN_TOKEN=                     required; guards /v1/admin/*
MAX_BATCH_RECORDS=5000
MAX_REQUEST_BYTES=10485760       10 MiB per POST (compressed or not)
MAX_BODY_BYTES=2097152           2 MiB per stored response body
S3_ENDPOINT=                     e.g. https://<account>.r2.cloudflarestorage.com ; empty disables bodies
S3_BUCKET=
S3_ACCESS_KEY_ID=
S3_SECRET_ACCESS_KEY=
S3_REGION=auto
S3_BODY_PREFIX=bodies/
RATE_LIMIT_RECORDS_PER_MINUTE=600000   per API key
```

Use the official `clickhouse-go` v2 client (check the current module path). For S3 use `aws-sdk-go-v2` with a custom endpoint (works with R2/MinIO). Those two plus the standard library should be enough; justify any other dependency in the README.

### 5.2 Endpoints

All `/v1/*` routes except `/v1/health` need `Authorization: Bearer <api key>`. Admin routes need `Authorization: Bearer <ADMIN_TOKEN>`.

**`POST /v1/requests`** — batch insert
- Body: a JSON **array** of records. A single object is rejected with 400 and a message telling the client to wrap it in an array.
- Accepts `Content-Encoding: gzip` and `zstd`.
- Validates each record: `url` required and must parse; `method` required; `status` 0–999; header maps are string→string. Fills `received_at`, `ts` default, `domain`, `request_id`, and `project` (from the key; a `project` field in the body is ignored).
- Behavior: **partial acceptance**. Valid records are inserted, invalid ones are reported. Response:
  ```json
  { "accepted": 998, "rejected": 2, "errors": [ { "index": 17, "error": "url is required" } ] }
  ```
  Status 200 if at least one record was accepted, 400 if none were, 413 if over size limits, 401 for a bad key, 429 when rate limited (with `Retry-After`).
- The insert goes to ClickHouse as one `INSERT` per HTTP request with `async_insert=1, wait_for_async_insert=1`. That way ClickHouse merges small batches from many scrapers, **and** the API only returns 200 once the data is durably flushed. No in-memory buffering in the API means nothing is lost if the API container restarts.

**`POST /v1/requests/stream`** — long-lived streaming insert
- Body: NDJSON (one JSON record per line), chunked transfer encoding, optionally gzip.
- The API reads lines, validates them, and flushes to ClickHouse every 2,000 records or 2 seconds, whichever comes first.
- When the client closes the body, the API flushes what's left and responds with the same summary shape as the batch endpoint (totals across the whole stream).
- Document that Cloudflare's proxy caps request time (around 100 s on free plans) and buffers request bodies, so long streams through Cloudflare should reconnect periodically. Example clients reconnect every 60 s.

**Response bodies (optional)**: a record may include `body` (string) with `body_encoding` of `utf8` or `base64`. If S3 is configured and the body is under `MAX_BODY_BYTES`, the API gzips and uploads it to `{S3_BODY_PREFIX}{project}/{yyyy}/{mm}/{dd}/{request_id}.gz` and sets `body_key`. If S3 isn't configured, the body is dropped and the record is still accepted with a warning in the response (`"warnings": [...]`). Bodies are never written to ClickHouse.

**`GET /v1/health`** — no auth. Returns 200 if ClickHouse responds to `SELECT 1`, 503 otherwise.

**`GET /v1/stats`** — API key auth. Row count, oldest/newest `ts`, and on-disk bytes for the caller's project.

**Admin (ADMIN_TOKEN):**
- `POST /v1/admin/keys` `{ "project": "acme-scraper", "name": "prod-box-1" }` → returns `{ "key_id", "key", "project" }`. The full key is only ever returned here.
- `GET /v1/admin/keys` → lists keys (never hashes or secrets)
- `DELETE /v1/admin/keys/{key_id}` → revokes the key
- `GET /v1/admin/storage` → total ClickHouse bytes on disk vs the 25 GB budget, per project

### 5.3 API keys

- Format: `sl_<key_id>_<32 random bytes, base62>`. Generated with `crypto/rand`.
- Stored as SHA-256 of the full key (fast hashing is fine for high-entropy random keys; bcrypt is unnecessary and would make every request slow).
- The API loads non-revoked keys into memory at startup and refreshes every 30 s, so a revocation takes effect within 30 s. Compare hashes with `crypto/subtle.ConstantTimeCompare`.
- CLI equivalent so you don't need curl: `docker compose exec ingest-api scrapelog keys create --project acme-scraper --name prod-box-1`, plus `keys list` and `keys revoke <key_id>`.

### 5.4 Other requirements

- Structured JSON logs to stdout. Never log API keys, `Authorization` headers, or record bodies.
- Graceful shutdown: stop accepting connections, flush open streams, then exit.
- Rate limiting per key with a token bucket in memory (single-instance only; that's fine for v1).
- Unit tests: record validation, domain extraction, proxy credential stripping, key generation and verification, partial-acceptance response building.
- Integration test (runs against the compose stack): create key → POST batch of 1,000 → assert 1,000 rows for that project → revoked key gets 401 within 35 s → a key for project A can't write rows tagged as project B.

---

## 6. ClickHouse resource limits (6 GB cap)

docker-compose:
```yaml
clickhouse:
  mem_limit: 6g
  ulimits:
    nofile: { soft: 262144, hard: 262144 }
```

`clickhouse/config.d/low-memory.xml`, starting values (verify names against the current docs):
- `max_server_memory_usage_to_ram_ratio`: `0.85` (recent ClickHouse versions detect the cgroup limit; confirm the server log at startup reports roughly 6 GB, not the host's 62 GB)
- `mark_cache_size`: 512 MiB
- `uncompressed_cache_size`: 0 (disabled)
- `index_mark_cache_size`, `primary_index_cache_size`: small (e.g. 128 MiB), if present in the pinned version
- `max_concurrent_queries`: 20
- `background_pool_size`: 4 (fewer concurrent merges means less memory)
- disable unneeded system log tables or give them a short TTL (`query_log`, `trace_log`, `part_log`, `metric_log`, `asynchronous_metric_log`). These quietly eat disk and memory on small boxes. Keep `query_log` with a 7-day TTL for debugging.

User profiles (`users.d/users.xml`):
- `grafana_reader`: `readonly=1`, `max_memory_usage` 2 GB per query, `max_execution_time` 60 s, `max_bytes_before_external_group_by` and `max_bytes_before_external_sort` 1 GB so big queries spill to disk instead of failing, `max_result_rows` 100k
- `ingest_writer`: may only `INSERT` into `scrapelog.requests` and `SELECT`/`INSERT` into `scrapelog.api_keys`; `async_insert=1`
- the `default` user: disabled or password-protected and limited to the internal network. No passwordless default user.

Expected memory: ClickHouse 2–5 GB, Grafana ~300 MB, ingest-api under 100 MB.

---

## 7. Grafana

- Provisioned automatically: ClickHouse data source using `grafana_reader`, plus both dashboards. A fresh `docker compose up` must show working dashboards with no clicking around.
- Anonymous access off. Admin password from `GRAFANA_ADMIN_PASSWORD` in `.env`. Sign-up disabled.
- `GF_SERVER_ROOT_URL` from env, so it works behind any domain.
- README note: if you're on Cloudflare, putting Cloudflare Access (Zero Trust) in front of the Grafana hostname is recommended as a second login layer. Optional.

**Dashboard: Overview** (variables at the top: `project`, `scraper`, `domain`, time range; all multi-select with "All"):
- stat tiles: total requests, success rate, block rate (403/429/503), median latency
- requests over time stacked by status class (2xx/3xx/4xx/5xx/0)
- block rate over time by `tls_profile`
- top domains by request count and by error rate (tables)
- status × tls_profile heatmap
- proxy health table: requests, error rate, p95 latency per proxy
- storage panel: bytes on disk vs the 25 GB budget, plus oldest raw row date
- long-range panels (more than 14 days) read from `requests_hourly`, not `requests`

**Dashboard: Request explorer** (same variables, plus `status` filter, free-text `url contains`, `tls_ja4`):
- table of recent requests, newest first, limit 500: time, project, scraper, method, status (color-coded), domain, path, latency, proxy, tls_profile, error
- clicking a row opens the full record: all request headers and response headers rendered as key/value rows, tags, and `body_key`
- error breakdown table: most common `error` strings grouped

Readability note for the builder: Grafana's defaults are cramped. Spend time on column widths, value mappings for status codes, hiding columns nobody reads, and sensible units (ms, bytes). The explorer should be usable to read one request's headers without squinting.

---

## 8. Retention / TTL

Starting defaults (in the schema above):
- successful requests (2xx–3xx): **14 days**
- failures, blocks, and network errors: **60 days**
- `requests_hourly` rollup: **forever**
- S3 bodies: set a bucket lifecycle rule to delete under `bodies/` after **14 days**. Document how to do this for R2 in the README.

Tuning procedure (put this in the README):
1. Run for about a week.
2. Measure:
   ```sql
   SELECT
       sum(rows) AS row_count,
       formatReadableSize(sum(bytes_on_disk)) AS disk_used,
       round(sum(bytes_on_disk) / sum(rows)) AS bytes_per_row
   FROM system.parts
   WHERE database = 'scrapelog' AND table = 'requests' AND active;

   SELECT toDate(ts) AS day, count() AS rows_per_day
   FROM scrapelog.requests GROUP BY day ORDER BY day;
   ```
3. Calculate: `retention_days = (25 GB × 0.7) / (rows_per_day × bytes_per_row)`. The 0.7 leaves headroom for merges (which temporarily need extra space), system tables, and backups staging.
4. Apply with `ALTER TABLE scrapelog.requests MODIFY TTL ...`. Change one number at a time.

Disk guard: ClickHouse has no "delete oldest when full" setting, so the API must protect the budget:
- `GET /v1/admin/storage` reports usage.
- When ClickHouse data exceeds 90% of `CLICKHOUSE_DISK_BUDGET_BYTES` (default 25 GB), the ingest API logs a warning on every flush. Above 100%, it rejects new inserts with `507 Insufficient Storage` and a clear message, rather than letting the host disk fill up.
- The Overview dashboard shows usage against the budget.

---

## 9. Backups

- `scripts/backup.sh` runs `BACKUP DATABASE scrapelog TO S3('<endpoint>/<bucket>/backups/<timestamp>/', ...)` via `clickhouse-client` inside the container.
- Documented as a nightly cron or systemd timer on the host. Keep 7 nightly backups; delete older ones with a bucket lifecycle rule on `backups/`.
- README must include a tested restore command (`RESTORE DATABASE ... FROM S3(...)`).
- Credentials come from `.env`, never written to logs or committed.

---

## 10. Security checklist

- [ ] ClickHouse has no host-published ports
- [ ] no passwordless ClickHouse user
- [ ] Grafana uses a read-only ClickHouse user
- [ ] ingest user can only write to `requests` and manage `api_keys`
- [ ] API keys stored hashed, shown once, revocable
- [ ] `project` always comes from the key
- [ ] proxy credentials stripped from `proxy` and from URLs in `url` (`user:pass@`)
- [ ] request size, batch size, and body size limits enforced
- [ ] per-key rate limiting
- [ ] no secrets in logs
- [ ] `.env` in `.gitignore`; only `.env.example` committed with placeholder values
- [ ] README warns: request headers you log may include your own cookies/tokens for target sites — redact sensitive header names via an optional `REDACT_HEADERS=authorization,cookie,proxy-authorization` env var (default to that list)

---

## 12. Acceptance criteria

The build is done when all of these are verified, with output shown:
1. Fresh clone → `cp .env.example .env` → fill values → `docker compose up -d` → all three containers healthy.
2. `curl` from outside the host to ClickHouse ports fails; `/v1/health` through the proxy returns 200.
3. Create a key via the CLI, send a 1,000-record batch from `examples/python/send_batch.py`, and see exactly 1,000 rows for that project in ClickHouse.
4. Stream 10,000 NDJSON records through `/v1/requests/stream`; all 10,000 land.
5. A batch with 2 invalid records out of 100 returns `accepted: 98, rejected: 2` with correct indexes.
6. Revoked key gets 401 within 35 s. A key for project A can't write project B data.
7. With S3 configured, a record with a body produces an object in the bucket and a non-empty `body_key`; without S3, the record is accepted with a warning.
8. Both Grafana dashboards load with data, filters work, and a single request's headers are readable in the explorer.
9. `docker stats` shows ClickHouse under 6 GB during a load test of ~50k records/s for 2 minutes, and dashboards stay responsive.
10. Backup to S3 succeeds and a restore into a scratch database returns the same row count.
11. Go unit tests and the integration test pass.

---

## 13. Out of scope for v1

- Multi-node ClickHouse, replication, or S3 as a live storage tier
- Multi-user accounts / per-project permissions (the single UI login sees all projects)
- Client SDK packages (examples only)
- Exactly-once dedup of retried batches (document that clients should retry with the same `request_id`s and that duplicates are possible; revisit with `ReplacingMergeTree` or insert dedup tokens if it becomes a problem)
