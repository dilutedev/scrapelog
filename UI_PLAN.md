# scrapelog — New UI Plan

Grafana is being replaced by a UI built into scrapelog itself. The UI has three jobs:

1. **Find requests easily**: search, filter, and read a single request's headers and bodies.
2. **Alert on requests**: send a Discord or Telegram message when a request matches a rule, e.g. "status is 403 and the body contains `captcha`".
3. **Show bandwidth over time**: response bytes, split by project, scraper, domain or proxy.

This plan reverses two items in `PLAN.md`: the "UI: Grafana" decision in §1 and "custom web UI" in §13 (Out of scope). Update both sections when this ships.

> Instructions for the building agent: read this whole file and `PLAN.md` before writing code. Follow the decisions below. If something is ambiguous, or a decision looks wrong once you're in the code, stop and ask instead of improvising. Use descriptive variable names and no code comments (project convention). Planning and review run on Opus; implementation runs on Sonnet.

---

## 1. Decisions (made with the author on 2026-11-15/16)

| Topic | Decision |
|---|---|
| Where the UI lives | Served by the existing Go `ingest-api` binary, the same way `/docs` is. HTML, CSS, JS and fonts are embedded with `go:embed`. |
| Build step | None. Plain HTML + vanilla JS (ES modules) + CSS. No Node, no bundler, no framework. |
| Charts | Vendor **uPlot** (MIT, ~50 KB, no deps) into the embedded assets. This is the only new third-party code. Record the reason in the README. |
| Grafana | Removed entirely: compose service, volume, provisioning, `grafana_reader` user, env vars, Caddyfile route. |
| Alert rule type | **Per-request match only.** Every condition on a rule must match the same single request. Checked inside the ingest path while the record is still in memory, which is the only place response bodies can be inspected. Windowed thresholds ("block rate > 20% over 5 min") are out of scope. |
| Alert channels | Discord webhook, Telegram bot (`sendMessage`). |
| Alert delivery | Batched: at most one message per rule per window (default 60 s). Each message has the match count plus up to 3 sample requests. Never blocks ingest. |
| Bandwidth | Response bytes only (`response_size`). Read from `requests_hourly`, so history is kept after raw rows expire. No new client field. |
| Login | Single user. The login form takes `ADMIN_TOKEN` and the server issues a signed HttpOnly session cookie. |
| Query input | A filter query bar (`field:value` syntax) that the server compiles to **parameterized** SQL. No raw SQL from the browser. |
| ClickHouse access | A new `ui_reader` user, replacing `grafana_reader` and reusing its limits, for searching and charts. Alert rules and events are read and written through `ingest_writer`, so `ui_reader` never sees webhook URLs or bot tokens. |

---

## 2. Design plan

### 2.1 Subject, audience, primary job

- **Subject:** a log of the HTTP requests your own scrapers made. Status codes, blocks, proxies, TLS fingerprints and bytes are the vocabulary.
- **Audience:** the person running the scrapers, usually at a desk, debugging why a target started blocking. They sometimes open an alert link on their phone.
- **Primary job:** answer "what happened to my requests to X, and why?" in a few seconds, then read one request in full without squinting.

The defining signal in this world is **the status code mix over time**. When a site starts blocking, 2xx turns into 403/429/503 or a captcha page. The design is built around making that shift visible and actionable.

### 2.2 Color

Light, cool, mineral. It should read like a lab notebook, not a cream editorial page or a dark terminal. The palette is a neutral base plus a five-color **status scale**. The status colors are the only saturated colors in the UI, so color always means something.

| Name | Hex | Role |
|---|---|---|
| Paper | `#EEF0EC` | page background (cool grey-green) |
| Sheet | `#F8F9F6` | raised surfaces: the results list, the detail pane, inputs |
| Ink | `#1D2A33` | text, primary buttons |
| Graphite | `#5B6770` | secondary text, axis labels |
| Rule | `#C9CFC9` | borders and dividers |
| Focus | `#2457C5` | keyboard focus ring, links, selected row edge |

Status scale (used in the tape, chips, chart series and status cells):

| Class | Hex | Name |
|---|---|---|
| 2xx | `#3F7D5C` | moss |
| 3xx | `#5A6FA8` | slate blue |
| 4xx (incl. blocks 403/429) | `#C9821A` | amber |
| 5xx | `#B8432F` | brick |
| 0 (no response / network error) | `#7A4E9C` | violet |

Contrast rule: status colors are used as fills and as text only on Sheet/Paper. Check that each color reaches 4.5:1 for text use, and darken that text variant if it doesn't. Color is never the only signal: status cells always show the number too.

### 2.3 Type

One family in two widths, chosen for legibility, because readability is the whole reason Grafana got rejected:

- **Atkinson Hyperlegible Next** for interface text, headings and prose. Self-hosted woff2 (SIL OFL), embedded.
- **Atkinson Hyperlegible Mono** for *data only*: URLs, header names and values, bodies, request IDs, JA3/JA4, and numeric table columns (latency, bytes) so they line up. Never for labels or decoration.

Scale (base 15 px, ratio about 1.2): 12.5 / 15 / 18 / 21.5 / 26 px. Body line-height 1.5, data rows 1.35. Weights are 400 and 600 only. All headings, labels and buttons use sentence case. No all-caps labels, no tracked-out eyebrows.

### 2.4 Layout

Three sections in a slim top bar: **Requests · Bandwidth · Alerts**, with sign out on the right. There's no sidebar, so the screen width goes to data. Text is left-aligned throughout and numbers are right-aligned in tables.

**Requests (the explorer), desktop:**

```
┌──────────────────────────────────────────────────────────────────────────┐
│ scrapelog   Requests  Bandwidth  Alerts                        Sign out  │
├──────────────────────────────────────────────────────────────────────────┤
│ [ status:403..429 domain:shop.example.com  captcha            ] [Last 1h▾]│
│ ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▒▒▒▒░░░░░░░░░░░░░░░░░░▒▒▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓  │ ← status tape
│ 14:00            14:15            14:30             14:45           15:00 │
├───────────────────────────────────────────┬──────────────────────────────┤
│ 14:52:07  403  GET  shop.example.com /p/12 │ GET https://shop.example.com │
│ 14:52:07  200  GET  shop.example.com /p/13 │ /p/12?ref=x                  │
│ 14:52:06  429  GET  shop.example.com /cart │ 403 · 812 ms · 14.2 KB       │
│ 14:52:05    0  GET  api.example.com  /v2/… │                              │
│ …                                          │ Response headers             │
│                                            │ cf-ray        8c1f…-AMS      │
│                                            │ server        cloudflare     │
│                                            │ Request headers   Tags  Body │
│ [Load older]                               │ [Copy as curl] [Find similar]│
└───────────────────────────────────────────┴──────────────────────────────┘
```

Note: `403 · 812 ms · 14.2 KB` in the wireframe is shorthand. Build it as separate labelled values, not a middle-dot string.

- **The status tape is the one bold element.** It's a full-width band where each time bucket is a column stacked to 100% by status class. It shows the *share* of blocks over time rather than volume, so the moment a site starts blocking is easy to see. A thin Ink line on top shows request volume. Dragging across the tape sets the time range. Clicking a color band adds that status class to the query. Everything else on the page stays quiet.
- **Results list:** dense single-line rows. Time and status are mono, the status shows a small color swatch, and the path is truncated in the middle so both the host and the end of the path stay visible. Clicking any value in a row (status, domain, proxy, scraper) adds it to the query. Shift-click excludes it.
- **Detail pane:** opens on the right, and pushes the list narrower instead of overlaying it.
  - Shows the full URL (wrapped, mono), the outcome values, then response headers, request headers, tags, request body and response body as two-column key/value tables. Long values wrap instead of truncating.
  - The body viewer pretty-prints JSON and shows HTML as escaped text with line numbers. It never renders HTML.
  - Actions: **Copy as curl** and **Find similar**, which adds the same domain + status + tls_profile to the query.
- **Mobile (< 760 px):** the tape shrinks to a 24 px strip, rows wrap to two lines, and the detail pane becomes a full-screen view with a back button.

**Bandwidth:**

```
┌──────────────────────────────────────────────────────────────────────────┐
│ 41.8 GB of responses in the last 7 days                                  │
│ Split by [Proxy ▾]   Project [All ▾]   [Last 7 days ▾]   Step [Hour ▾]   │
│ ┌──────────────────────────────────────────────────────────────────────┐ │
│ │ stacked area chart, bytes on Y (binary units), time on X             │ │
│ └──────────────────────────────────────────────────────────────────────┘ │
│ Proxy              Bytes        Share                       Responses    │
│ resi-pool-eu      18.2 GB   ███████████████░░░░░░  43.5%     612,004     │
│ dc-fra-1           9.7 GB   ████████░░░░░░░░░░░░░  23.2%     901,377     │
└──────────────────────────────────────────────────────────────────────────┘
```

- The headline is a plain sentence answering the page's question. It is not a big-number stat tile.
- Series colors are a separate categorical set of 8 muted hues, so they don't clash with the status scale. Anything past the top 8 groups is merged into "Other".
- Clicking a row in the table filters the chart to that group.

**Alerts:**

```
┌──────────────────────────────────────────────────────────────────────────┐
│ Alerts                                                       [New rule]  │
│ ● Blocked by captcha on shop                                    Discord │
│   When status is 403–429 and body contains "captcha"                     │
│   in project acme-scraper. Fired 4 times today, last at 14:52.          │
│ ○ Proxy timeouts (paused)                                       Telegram │
│   When status is 0 and proxy is resi-pool-eu.                            │
├──────────────────────────────────────────────────────────────────────────┤
│ Recent alerts                                                            │
│ 14:52  Blocked by captcha on shop   312 matches   sent                   │
│ 14:31  Blocked by captcha on shop    44 matches   failed: 401 from Discord│
└──────────────────────────────────────────────────────────────────────────┘
```

- Each rule is written out as a sentence generated from its conditions, so the list reads like what the rule actually does.
- **Rule editor** (a page, not a modal):
  - Fields: name, project (or all projects), and condition rows of [field ▾] [operator ▾] [value], with an add-condition control.
  - "Send to" picks Discord (webhook URL) or Telegram (bot token + chat ID). Secrets show as "saved" once stored and are never sent back to the browser.
  - "At most one message every [60] seconds".
  - Buttons: **Send test message**, **Save rule**, **Delete rule**.
  - A live preview sentence above the buttons repeats the rule in words.

**Sign in:** a single centered field, "Admin token", and a **Sign in** button. A wrong token shows "That token doesn't match ADMIN_TOKEN on the server."

### 2.5 Principles

1. **Status color means status, and nothing else.** Buttons, navigation and decoration never borrow a status color.
2. **Every value you can see, you can filter by.** Clicking is the main way to build queries, and typing is the shortcut.
3. **Read, don't squint.** Data text is never smaller than 12.5 px. Header values wrap rather than truncate in the detail pane. Tables have no zebra stripes; there are row hover and selected states only.
4. **One moment of motion:** the tape fills left to right when a new query resolves (200 ms; disabled under `prefers-reduced-motion`). The detail pane slides in on click because the user triggered it. Nothing else animates.
5. **Plain words:** "Requests", not "Events". "Send to Discord", not "Webhook config". Errors say what happened and what to do next, e.g. "ClickHouse took longer than 60 s. Narrow the time range or add a domain filter."

### 2.6 Check of the plan against the brief (revisions made)

- **The tape started as a volume histogram above the log list.** That's the default for every log viewer (Kibana, Datadog). It was changed to a **100%-stacked status share band** with a volume line, because for scrapers the question is "when did blocking start", not "how many requests". This is the deliberate bold element.
- **The first palette was dark with one bright green accent.** That's generated-default #2 from the skill's list. It was changed to a light mineral base where the five status colors carry all the saturation.
- **The first type choice was IBM Plex Sans + Plex Mono.** That's a reflexive "technical tool" pair. It was changed to the Atkinson Hyperlegible pair, because legibility is the brief's stated complaint and both widths come from one family.
- **The Bandwidth page started as three stat tiles.** It was replaced with one plain-sentence headline. One number matters (total bytes), and the table already gives the breakdown.
- **Monospace is limited to real data values.** Using mono for small UI labels is another generated tell.

---

## 3. HTTP surface

All UI routes are on the existing `ingest-api` server (port 8080). The reverse proxy now sends everything to it: `/v1/*` for scrapers, everything else for the UI.

### 3.1 Pages and assets

| Route | Auth | Serves |
|---|---|---|
| `GET /` | session | redirect to `/requests` |
| `GET /login` | none | sign-in page |
| `GET /requests`, `GET /bandwidth`, `GET /alerts`, `GET /alerts/new`, `GET /alerts/{rule_id}` | session (redirect to `/login?next=…` if missing) | embedded HTML pages |
| `GET /assets/*` | none | embedded CSS, JS, fonts, uPlot. `Cache-Control: public, max-age=31536000, immutable` with content-hashed or versioned names. |

Every UI response sets security headers:
- a strict CSP: `default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`
- `X-Content-Type-Options: nosniff`
- `Referrer-Policy: same-origin`

No inline scripts or styles, so the CSP holds.

### 3.2 Session

- `POST /ui/api/login` `{ "token": "…" }`:
  - Compares the token to the SHA-256 of `ADMIN_TOKEN` in constant time, the same way `requireAdmin` does.
  - Rate limit: 10 attempts per minute per client IP (`X-Real-IP` when set by the proxy, otherwise the remote address), using the existing token-bucket package.
- On success, sets `scrapelog_session`:
  - The value is `expiry.hmac`, where the HMAC is HMAC-SHA256 with a key derived from `ADMIN_TOKEN`. So rotating the token logs everyone out.
  - Expires after 7 days. `HttpOnly`, `SameSite=Strict`, `Path=/`, `Secure` when the request came in over HTTPS (`X-Forwarded-Proto: https` or TLS).
- `POST /ui/api/logout` clears the cookie.
- CSRF:
  - Every mutating `/ui/api/*` call requires `Content-Type: application/json`.
  - If an `Origin` header is present, it must match the `Host`.
  - The cookie is `SameSite=Strict`.

### 3.3 Read API (session auth, `ui_reader` connection)

**`GET /ui/api/requests?q=&from=&to=&limit=100&cursor=`** returns
`{ "rows": [ { ts, project, scraper, request_id, method, url, domain, status, latency_ms, response_size, proxy, tls_profile, error } ], "next_cursor": "…" | null }`
- Newest first. `limit` max 500.
- The cursor is opaque base64 of `(ts, request_id)` of the last row. Use keyset pagination, not OFFSET.
- `from`/`to` are RFC 3339. Default is the last hour.

**`GET /ui/api/request?project=&domain=&ts=&request_id=`** returns the full record: every column, header maps, tags, request body + encoding, `body_key`, and `has_body`.
- Passing project + domain + ts lets ClickHouse use the `(project, domain, ts)` sort key, instead of scanning for a request_id.

**`GET /ui/api/request/body?project=&domain=&ts=&request_id=`**
- Reads the record's `body_key`, fetches the object from S3, gunzips it, and returns it with `Content-Type: text/plain; charset=utf-8`.
- Returns 404 with a clear message when there is no body or S3 isn't configured.
- Needs a `Get` method next to the existing `Put` in `internal/bodies/s3.go`.
- Never served as `text/html`.

**`GET /ui/api/tape?q=&from=&to=`** returns
`{ "step_seconds": 60, "buckets": [ { "t": "…", "s2xx": 0, "s3xx": 0, "s4xx": 0, "s5xx": 0, "s0": 0 } ] }`
- The step is picked so there are 60–240 buckets.
- Reads raw `requests` when the range is inside the raw retention, otherwise `requests_hourly`. With `requests_hourly`, only filters on its key columns can apply. When the query has filters it can't apply, the response includes `"approximate": true`.

**`GET /ui/api/facets?field=domain|scraper|project|proxy|tls_profile|method&prefix=&from=&to=`** returns up to 20 values by count, for query-bar autocomplete.

**`GET /ui/api/bandwidth?from=&to=&step=hour|day&split=project|scraper|domain|proxy&project=`** returns
`{ "total_bytes", "total_responses", "series": [ { "key", "points": [[unix_seconds, bytes], …] } ], "groups": [ { "key", "bytes", "responses" } ] }`
- Always reads `requests_hourly` (`sumMerge(total_response_size)`, `sumMerge(request_count)`).
- The top 8 groups are kept and the rest are summed into `"Other"`.

### 3.4 Query bar syntax (compiled server-side)

| Syntax | Meaning |
|---|---|
| `status:403` / `status:400..499` / `status:4xx` | exact / range / class |
| `domain:shop.example.com` | exact match on a LowCardinality field: `project`, `scraper`, `domain`, `method`, `proxy`, `tls`, `ja3`, `ja4`, `http` |
| `latency:>2000`, `size:<1024`, `latency:500..2000` | numeric comparisons (ms, bytes) |
| `url~/api/v2` , `error~timeout` | substring (`positionCaseInsensitive`) |
| `res.server:cloudflare`, `req.user-agent~Chrome` | response / request header value (exact or contains), header name case-insensitive |
| `tag.region:eu` | tag value |
| `id:01J…` | request_id |
| `-field:value` | negate any of the above |
| bare word | `url` contains |
| `"quoted words"` | value containing spaces |

- Terms are joined with AND.
- Unknown fields return 400 with a message naming the field and listing the valid ones. The browser shows that message under the query bar.
- Every value is passed as a ClickHouse query parameter (`{name:Type}`). Field names come only from a fixed allowlist mapping to column expressions. User text is never concatenated into SQL.

### 3.5 Alert API (session auth, `ingest_writer` connection)

| Route | Behavior |
|---|---|
| `GET /ui/api/alert-rules` | list rules. Secrets are replaced with `"discord_webhook_set": true` / `"telegram_token_set": true` |
| `POST /ui/api/alert-rules` | create; validate; return the rule without secrets |
| `PUT /ui/api/alert-rules/{rule_id}` | update. An empty secret field means "keep the stored one" |
| `DELETE /ui/api/alert-rules/{rule_id}` | soft delete (`deleted = 1`) |
| `POST /ui/api/alert-rules/{rule_id}/test` | send a test message to the rule's channel now and return the delivery result (status code + error text) |
| `GET /ui/api/alert-events?rule_id=&limit=50` | recent firings, newest first |

---

## 4. Alerts: how they work

### 4.1 Rule model

```
rule_id          String          UUIDv7
name             String
project          LowCardinality(String)   empty = all projects
conditions       String          JSON array of { field, operator, value }
channel          LowCardinality(String)   'discord' | 'telegram'
discord_webhook_url  String
telegram_bot_token   String
telegram_chat_id     String
window_seconds   UInt32          default 60, min 10, max 3600
enabled          UInt8
deleted          UInt8
created_at, updated_at  DateTime64(3)
```
Engine: `ReplacingMergeTree(updated_at) ORDER BY rule_id`, the same pattern as `api_keys`.

Fields and operators:

| Field | Type | Operators |
|---|---|---|
| `status` | number | equals, not equals, between, `>`, `<`, class (2xx…/0) |
| `latency_ms`, `response_size` | number | equals, between, `>`, `<` |
| `method`, `domain`, `scraper`, `proxy`, `tls_profile` | text | equals, not equals |
| `url`, `error` | text | contains, not contains, regex |
| `response_header[name]`, `request_header[name]`, `tag[name]` | text | equals, contains, not contains, regex, exists, missing |
| `body` | bytes | contains, not contains, regex |

- Every condition must match (AND). Text matching is case-insensitive except regex. Use the `(?i)` flag for case-insensitive regex.
- Regex uses Go `regexp` (RE2, linear time). Compile when the rule is saved; an invalid pattern returns 400 with the parse error.
- Body conditions are evaluated **last**, only after every cheaper condition on the rule has passed.
- Body regex and contains scan at most the first 1 MiB of the decoded body.
- A rule with only a `body` condition is allowed. A rule with zero conditions is rejected.

### 4.2 Where matching runs

- An in-memory rule cache that reloads enabled, non-deleted rules every 30 s, like `apikeys.Cache`. Rules are also reloaded right after any create, update or delete through the UI.
- **Batch path** (`httpapi/requests.go`) and **stream path** (`httpapi/stream.go`):
  1. After validation and **before** `bodyStore.Store` (which drops `record.Body` when S3 isn't configured), evaluate rules for each valid record. Keep the matches in a local slice.
  2. Only after the ClickHouse insert succeeds, enqueue those matches with the dispatcher. Records that failed to insert never alert.
- A match carries: rule_id, project, ts, request_id, method, url, domain, status, latency_ms, plus the matched body snippet (≤ 200 chars around the first hit) when a body condition matched.

### 4.3 Dispatcher

- One goroutine started in `serve`. It has a bounded queue of 10,000 matches. When the queue is full, matches are dropped and counted, and the next message for that rule says "N further matches were dropped".
- Each rule has a window. The first match opens it, and the message goes out when the window closes:
  - **Discord:** `POST {webhook_url}` with `{"content": "…"}`, at most 2,000 chars, no mentions (`"allowed_mentions": {"parse": []}`).
  - **Telegram:** `POST https://api.telegram.org/bot{token}/sendMessage` with `chat_id` and `text`, at most 4,096 chars, plain text (no parse_mode, so URLs can't break the formatting).
- Message body:

  ```
  Blocked by captcha on shop: 312 matching requests in the last 60 s (acme-scraper).
  403 GET https://shop.example.com/p/12 (812 ms)
  429 GET https://shop.example.com/cart (95 ms)
  …
  Open in scrapelog: {SCRAPELOG_PUBLIC_URL}/requests?q=…
  ```
  The link only appears when `SCRAPELOG_PUBLIC_URL` is set.
- HTTP client timeout is 10 s.
  - On 429, honor `retry_after` (Discord) or `parameters.retry_after` (Telegram) once.
  - On other failures, record the error and move on. There's no retry storm.
- Every send writes a row to `alert_events`.
- On graceful shutdown, flush any open windows with a 5 s budget.
- Never log webhook URLs or bot tokens. Log the rule_id and the HTTP status only.

### 4.4 `alert_events`

```
fired_at       DateTime64(3)
rule_id        String
rule_name      String
project        LowCardinality(String)
match_count    UInt32
dropped_count  UInt32
samples        String     JSON array of up to 3 sample summaries
delivery       LowCardinality(String)   'sent' | 'failed'
delivery_error String
```
`MergeTree ORDER BY (rule_id, fired_at)`, `TTL toDateTime(fired_at) + INTERVAL 30 DAY`.

---

## 5. ClickHouse changes

- New file `clickhouse/initdb/002_alerts.sql` with `alert_rules` and `alert_events`. initdb only runs on an empty volume, so the README gets an "Upgrading an existing install" section. That section gives the one command to apply the file to an existing install with `docker compose exec clickhouse clickhouse-client --user scrapelog_admin …`.
- `users.d/users.xml`:
  - Rename the `grafana_reader` profile and user to `ui_reader`. Keep the same limits and keep `CLICKHOUSE_READER_PASSWORD`.
  - Add `ingest_writer` grants: `INSERT, SELECT ON scrapelog.alert_rules`, `INSERT, SELECT ON scrapelog.alert_events`.
  - `ui_reader` gets **no** grant on `alert_rules`.
- New config for the ingest-api:
  - `CLICKHOUSE_READER_USER` (default `ui_reader`)
  - `CLICKHOUSE_READER_PASSWORD`, which is now also passed to the `ingest-api` container
  - `SCRAPELOG_PUBLIC_URL` (optional, used in alert links)

  Add all three to `.env.example` and `config.go`.

---

## 6. Grafana removal

- `docker-compose.yml`: delete the `grafana` service and the `grafana_data` volume. Pass `CLICKHOUSE_READER_PASSWORD` and `SCRAPELOG_PUBLIC_URL` to `ingest-api`.
- Delete the `grafana/` directory.
- `.env.example`: remove the `GRAFANA_*` variables.
- `deploy/examples/Caddyfile`: a single `reverse_proxy 127.0.0.1:8080` for the whole host.
- README:
  - Replace the Grafana sections with a UI section: sign in, query syntax, alerts, bandwidth.
  - Update the architecture diagram.
  - Change the Cloudflare Access note to "protect everything except `/v1/*`".
- Integration test: remove the Grafana checks and add the checks in §8.
- `PLAN.md` §1 and §13: record the reversal and point to this file.

---

## 7. Code layout

```
ingest-api/internal/
├── ui/                      embedded assets + page handlers
│   ├── assets/              app.css, fonts/, vendor/uplot.*, *.js (ES modules per page)
│   ├── pages/               login.html, requests.html, bandwidth.html, alerts.html, alert-edit.html
│   └── ui.go                go:embed, page + asset handlers, security headers
├── session/                 cookie sign/verify, login rate limit
├── query/                   query-bar parser → SQL fragment + params; tape/bandwidth/search SQL
├── alerts/                  rule model + validation, store (ClickHouse), cache, matcher, dispatcher, discord.go, telegram.go
└── httpapi/                 new /ui/api/* handlers next to the existing ones; hooks into requests.go and stream.go
```

Match the existing code: `net/http` ServeMux patterns, `slog` JSON logs, small interfaces at the point of use (as `httpapi/handler.go` does), table-driven tests (as `ingest/record_test.go` does). No new Go dependencies.

---

## 8. Tests and acceptance criteria

Unit tests:
- **Query parser:** every syntax row in §3.4. Unknown fields. Injection-shaped input (`'`, `\`, `)`, `--`, `{x:String}`) ends up only in parameters.
- **Session:** valid cookie, expired, tampered, wrong-token-derived key, login rate limit.
- **Matcher:** each field × operator. Case-insensitivity. Base64 bodies decoded before matching. The body condition is skipped when cheaper conditions fail. Invalid regex is rejected at save.
- **Dispatcher:** windowing (N matches → 1 message), sample cap, queue-full drop count, Discord/Telegram payload shape and length limits, 429 retry honored once (with an `httptest` server standing in for Discord and Telegram).
- **Rule API:** secrets are never present in GET responses, and an empty secret on PUT keeps the stored value.

Acceptance (verified with output shown):
1. `docker compose up -d` on a fresh clone starts two healthy containers (clickhouse, ingest-api), and Grafana is gone.
2. `/requests` redirects to `/login` without a session. A wrong token is refused with the message from §2.4. The right token lands on Requests.
3. After a 1,000-record batch, the query `status:403 domain:…` returns exactly the matching rows. The tape shows the block share. Clicking a row shows wrapped, readable headers, and the S3 response body when one is configured.
4. A rule "status is 403 and body contains `captcha`" pointed at a local fake webhook receives **one** message for a batch containing 50 matching records, with count 50 and 3 samples. Records not matching any condition produce nothing.
5. Bandwidth for the last 7 days matches `SELECT sum(response_size)` from raw rows within that range, and the per-proxy split sums to the total.
6. Under the PLAN.md load test (~50k records/s, 2 min) with 5 enabled rules (including one body regex), ingest p95 latency rises by no more than 10% versus no rules, and memory stays within the existing budgets.
7. CSP is present on every page, no inline script or style violations show in the browser console, keyboard focus is visible, and the pages work at 375 px wide.
8. Screenshots of Requests (with a detail pane open), Bandwidth and the alert editor are reviewed against §2 before sign-off.

---

## 9. Build order

Each phase is implemented by a Sonnet agent and reviewed by Opus before the next one starts.

1. **Foundation:** config additions, `ui_reader`, session/login, embedded asset serving, security headers, login page, top-bar shell.
2. **Requests:** query parser, search/detail/body/tape/facets APIs, explorer page with status tape and detail pane.
3. **Bandwidth:** API and page with uPlot.
4. **Alerts:** schema, store, cache, matcher, ingest hooks, dispatcher, Discord/Telegram, rule API, alerts list and editor pages.
5. **Grafana removal and docs:** compose, env, Caddyfile, README, integration test, PLAN.md update, then a full acceptance run.

---

## 10. Open questions for the author

- **Existing install migration:** the plan has you apply `002_alerts.sql` manually once (documented). The alternative is having the API create the tables at startup, which needs CREATE grants on `ingest_writer`. Manual is assumed.
- **Fonts:** the Atkinson Hyperlegible Next and Mono woff2 files get vendored, about 150 KB total. Say so if you'd rather use system fonts.
- **Dark mode:** not planned. Light theme only.
