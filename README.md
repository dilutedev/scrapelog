# scrapelog

## Grafana

- Log in at the Grafana URL as `admin` with the password in `GRAFANA_ADMIN_PASSWORD`.
- Set `GRAFANA_ROOT_URL` to the public URL Grafana is served from.
- Dashboards and the ClickHouse datasource are provisioned from `grafana/provisioning/` and are edited there, not in the UI.
- The 25 GB budget on the storage gauge is set in `grafana/provisioning/dashboards/overview.json`. Edit it there if the disk budget changes.
- Put Cloudflare Access in front of the Grafana hostname.

## Dependencies

- The ingest API's direct dependencies are `clickhouse-go/v2` and `klauspost/compress`.
- `klauspost/compress` is used only to decode `Content-Encoding: zstd` request bodies, because Go's standard library has no zstd. It is already an indirect dependency of `clickhouse-go`.
