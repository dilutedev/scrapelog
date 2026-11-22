CREATE TABLE scrapelog.users
(
    user_id String,
    username String,
    password_hash String,
    permissions Array(LowCardinality(String)),
    disabled UInt8,
    session_version UInt32,
    created_at DateTime64(3),
    updated_at DateTime64(3)
)
ENGINE = ReplacingMergeTree(updated_at)
ORDER BY user_id;
