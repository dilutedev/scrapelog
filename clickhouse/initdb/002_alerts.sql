CREATE TABLE scrapelog.alert_rules
(
    rule_id String,
    name String,
    project LowCardinality(String),
    conditions String,
    channel LowCardinality(String),
    discord_webhook_url String,
    telegram_bot_token String,
    telegram_chat_id String,
    window_seconds UInt32,
    enabled UInt8,
    deleted UInt8,
    created_at DateTime64(3),
    updated_at DateTime64(3)
)
ENGINE = ReplacingMergeTree(updated_at)
ORDER BY rule_id;

CREATE TABLE scrapelog.alert_events
(
    fired_at DateTime64(3),
    rule_id String,
    rule_name String,
    project LowCardinality(String),
    match_count UInt32,
    dropped_count UInt32,
    samples String,
    delivery LowCardinality(String),
    delivery_error String
)
ENGINE = MergeTree
ORDER BY (rule_id, fired_at)
TTL toDateTime(fired_at) + INTERVAL 30 DAY;
