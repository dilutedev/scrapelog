CREATE DATABASE IF NOT EXISTS scrapelog;

CREATE TABLE scrapelog.requests
(
    received_at DateTime64(3) CODEC(Delta, ZSTD),
    ts DateTime64(3) CODEC(Delta, ZSTD),
    project LowCardinality(String),
    scraper LowCardinality(String),
    request_id String,
    method LowCardinality(String),
    url String CODEC(ZSTD(3)),
    domain LowCardinality(String),
    status UInt16,
    latency_ms UInt32,
    proxy LowCardinality(String),
    http_version LowCardinality(String),
    tls_ja3 LowCardinality(String),
    tls_ja4 LowCardinality(String),
    tls_profile LowCardinality(String),
    request_headers Map(LowCardinality(String), String) CODEC(ZSTD(3)),
    response_headers Map(LowCardinality(String), String) CODEC(ZSTD(3)),
    response_size UInt32,
    body_key String,
    request_body String CODEC(ZSTD(3)),
    request_body_encoding LowCardinality(String),
    error String CODEC(ZSTD(3)),
    tags Map(LowCardinality(String), String)
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (project, domain, ts)
TTL toDateTime(ts) + INTERVAL 14 DAY DELETE WHERE status BETWEEN 200 AND 399,
    toDateTime(ts) + INTERVAL 60 DAY DELETE
SETTINGS ttl_only_drop_parts = 0;

CREATE TABLE scrapelog.requests_hourly
(
    hour DateTime,
    project LowCardinality(String),
    scraper LowCardinality(String),
    domain LowCardinality(String),
    status UInt16,
    tls_profile LowCardinality(String),
    proxy LowCardinality(String),
    request_count AggregateFunction(sum, UInt64),
    error_count AggregateFunction(sum, UInt64),
    total_latency_ms AggregateFunction(sum, UInt64),
    total_response_size AggregateFunction(sum, UInt64),
    latency_quantiles AggregateFunction(quantiles(0.5, 0.95, 0.99), UInt32)
)
ENGINE = AggregatingMergeTree
ORDER BY (project, hour, scraper, domain, status, tls_profile, proxy);

CREATE MATERIALIZED VIEW scrapelog.requests_hourly_mv TO scrapelog.requests_hourly AS
SELECT
    toStartOfHour(toDateTime(ts)) AS hour,
    project,
    scraper,
    domain,
    status,
    tls_profile,
    proxy,
    sumState(toUInt64(1)) AS request_count,
    sumState(toUInt64(status = 0 OR error != '')) AS error_count,
    sumState(toUInt64(latency_ms)) AS total_latency_ms,
    sumState(toUInt64(response_size)) AS total_response_size,
    quantilesState(0.5, 0.95, 0.99)(latency_ms) AS latency_quantiles
FROM scrapelog.requests
GROUP BY hour, project, scraper, domain, status, tls_profile, proxy;

CREATE TABLE scrapelog.api_keys
(
    key_id String,
    key_hash String,
    project LowCardinality(String),
    name String,
    created_at DateTime64(3),
    updated_at DateTime64(3),
    revoked UInt8
)
ENGINE = ReplacingMergeTree(updated_at)
ORDER BY key_id;
