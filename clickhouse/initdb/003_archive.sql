CREATE TABLE scrapelog.archive_days
(
    day Date,
    object_key String,
    row_count UInt64,
    partial UInt8,
    archived_at DateTime64(3)
)
ENGINE = ReplacingMergeTree(archived_at)
ORDER BY day;
