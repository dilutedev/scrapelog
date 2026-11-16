package archive

import (
	"context"
	"log/slog"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const (
	runInterval    = time.Hour
	objectPrefix   = "archive/requests/"
	partitionQuery = "SELECT partition FROM system.parts WHERE active AND database = 'scrapelog' AND table = 'requests' GROUP BY partition HAVING sum(rows) > 0"
	manifestQuery  = "SELECT toString(day), max(archived_at) FROM archive_days GROUP BY day"
	exportQuery    = `INSERT INTO FUNCTION s3(archive_s3, filename = {object_name:String}, format = 'Parquet')
SELECT received_at, ts, project, scraper, request_id, method, url, domain, status, latency_ms, proxy, http_version, tls_ja3, tls_ja4, tls_profile, request_headers, response_headers, response_size, body_key, request_body, request_body_encoding, error, tags
FROM requests
WHERE ts >= {day_start:DateTime64(3)} AND ts < {day_end:DateTime64(3)}
SETTINGS s3_truncate_on_insert = 1`
	countQuery     = "SELECT count() FROM s3(archive_s3, filename = {object_name:String}, format = 'Parquet')"
	manifestInsert = "INSERT INTO archive_days (day, object_key, row_count, partial, archived_at)"
)

type Archiver struct {
	connection driver.Conn
}

func NewArchiver(connection driver.Conn) *Archiver {
	return &Archiver{connection: connection}
}

func (archiver *Archiver) Run(ctx context.Context, logger *slog.Logger) {
	archiver.archiveDueDays(ctx, logger)
	ticker := time.NewTicker(runInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			archiver.archiveDueDays(ctx, logger)
		}
	}
}

func (archiver *Archiver) archiveDueDays(ctx context.Context, logger *slog.Logger) {
	runStartedAt := time.Now().UTC()
	daysWithRows, err := archiver.daysWithRows(ctx)
	if err != nil {
		logger.Warn("archive run failed", "error", err)
		return
	}
	lastArchivedAt, err := archiver.lastArchivedAt(ctx)
	if err != nil {
		logger.Warn("archive run failed", "error", err)
		return
	}
	for _, due := range DaysToArchive(runStartedAt, daysWithRows, lastArchivedAt) {
		if ctx.Err() != nil {
			return
		}
		rowCount, err := archiver.archiveDay(ctx, due)
		if err != nil {
			logger.Warn("archiving day failed", "day", dayKey(due.Day), "error", err)
			continue
		}
		logger.Info("archived day", "day", dayKey(due.Day), "row_count", rowCount, "partial", due.Partial)
	}
}

func (archiver *Archiver) daysWithRows(ctx context.Context) (map[string]bool, error) {
	rows, err := archiver.connection.Query(ctx, partitionQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	daysWithRows := map[string]bool{}
	for rows.Next() {
		var partition string
		if err := rows.Scan(&partition); err != nil {
			return nil, err
		}
		day, err := time.Parse("20060102", partition)
		if err != nil {
			return nil, err
		}
		daysWithRows[dayKey(day)] = true
	}
	return daysWithRows, rows.Err()
}

func (archiver *Archiver) lastArchivedAt(ctx context.Context) (map[string]time.Time, error) {
	rows, err := archiver.connection.Query(ctx, manifestQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lastArchivedAt := map[string]time.Time{}
	for rows.Next() {
		var key string
		var archivedAt time.Time
		if err := rows.Scan(&key, &archivedAt); err != nil {
			return nil, err
		}
		lastArchivedAt[key] = archivedAt
	}
	return lastArchivedAt, rows.Err()
}

func (archiver *Archiver) archiveDay(ctx context.Context, due DueDay) (uint64, error) {
	key := dayKey(due.Day)
	objectName := key + ".parquet"
	archivedAt := time.Now().UTC()
	err := archiver.connection.Exec(ctx, exportQuery,
		clickhouse.Named("object_name", objectName),
		clickhouse.Named("day_start", due.Day),
		clickhouse.Named("day_end", due.Day.AddDate(0, 0, 1)),
	)
	if err != nil {
		return 0, err
	}
	var rowCount uint64
	err = archiver.connection.QueryRow(ctx, countQuery, clickhouse.Named("object_name", objectName)).Scan(&rowCount)
	if err != nil {
		return 0, err
	}
	batch, err := archiver.connection.PrepareBatch(ctx, manifestInsert)
	if err != nil {
		return 0, err
	}
	var partialFlag uint8
	if due.Partial {
		partialFlag = 1
	}
	if err := batch.Append(due.Day, objectPrefix+objectName, rowCount, partialFlag, archivedAt); err != nil {
		return 0, err
	}
	if err := batch.Send(); err != nil {
		return 0, err
	}
	return rowCount, nil
}
