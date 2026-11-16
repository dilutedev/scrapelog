CREATE TABLE scrapelog.requests_restored
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
    tags Map(LowCardinality(String), String),
    restore_id String,
    restored_at DateTime
)
ENGINE = MergeTree
PARTITION BY restore_id
ORDER BY (restore_id, project, domain, ts)
TTL restored_at + INTERVAL 7 DAY DELETE;

CREATE TABLE scrapelog.archive_restores
(
    restore_id String,
    range_from DateTime64(3),
    range_to DateTime64(3),
    project String,
    domain String,
    status LowCardinality(String),
    row_count UInt64,
    error String,
    created_at DateTime64(3),
    updated_at DateTime64(3)
)
ENGINE = ReplacingMergeTree(updated_at)
ORDER BY restore_id;
