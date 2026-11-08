package storage

import (
	"context"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const (
	diskUsedQuery          = "SELECT sum(bytes_on_disk) AS used_bytes FROM system.parts WHERE active AND database IN ('scrapelog', 'system')"
	requestsTableSizeQuery = "SELECT sum(rows), sum(bytes_on_disk) FROM system.parts WHERE active AND database = 'scrapelog' AND table = 'requests'"
	projectStatsQuery      = "SELECT count(), min(ts), max(ts) FROM requests WHERE project = ?"
	projectRowCountsQuery  = "SELECT project, count() FROM requests GROUP BY project ORDER BY project"
)

type Store struct {
	connection driver.Conn
}

func NewStore(connection driver.Conn) *Store {
	return &Store{connection: connection}
}

type ProjectStats struct {
	Project              string     `json:"project"`
	RowCount             uint64     `json:"row_count"`
	OldestTimestamp      *time.Time `json:"oldest_ts"`
	NewestTimestamp      *time.Time `json:"newest_ts"`
	EstimatedBytesOnDisk uint64     `json:"estimated_bytes_on_disk"`
}

type ProjectUsage struct {
	Project              string `json:"project"`
	RowCount             uint64 `json:"row_count"`
	EstimatedBytesOnDisk uint64 `json:"estimated_bytes_on_disk"`
}

func (store *Store) DiskUsedBytes(ctx context.Context) (uint64, error) {
	var usedBytes uint64
	if err := store.connection.QueryRow(ctx, diskUsedQuery).Scan(&usedBytes); err != nil {
		return 0, err
	}
	return usedBytes, nil
}

func (store *Store) requestsTableSize(ctx context.Context) (uint64, uint64, error) {
	var tableRows, tableBytes uint64
	if err := store.connection.QueryRow(ctx, requestsTableSizeQuery).Scan(&tableRows, &tableBytes); err != nil {
		return 0, 0, err
	}
	return tableRows, tableBytes, nil
}

func (store *Store) ProjectStats(ctx context.Context, project string) (ProjectStats, error) {
	tableRows, tableBytes, err := store.requestsTableSize(ctx)
	if err != nil {
		return ProjectStats{}, err
	}
	stats := ProjectStats{Project: project}
	var oldestTimestamp, newestTimestamp time.Time
	if err := store.connection.QueryRow(ctx, projectStatsQuery, project).Scan(&stats.RowCount, &oldestTimestamp, &newestTimestamp); err != nil {
		return ProjectStats{}, err
	}
	if stats.RowCount > 0 {
		oldestUTC := oldestTimestamp.UTC()
		newestUTC := newestTimestamp.UTC()
		stats.OldestTimestamp = &oldestUTC
		stats.NewestTimestamp = &newestUTC
	}
	stats.EstimatedBytesOnDisk = estimatedBytes(stats.RowCount, tableRows, tableBytes)
	return stats, nil
}

func (store *Store) ProjectUsage(ctx context.Context) ([]ProjectUsage, error) {
	tableRows, tableBytes, err := store.requestsTableSize(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := store.connection.Query(ctx, projectRowCountsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	usages := []ProjectUsage{}
	for rows.Next() {
		var usage ProjectUsage
		if err := rows.Scan(&usage.Project, &usage.RowCount); err != nil {
			return nil, err
		}
		usage.EstimatedBytesOnDisk = estimatedBytes(usage.RowCount, tableRows, tableBytes)
		usages = append(usages, usage)
	}
	return usages, rows.Err()
}

func estimatedBytes(projectRows, tableRows, tableBytes uint64) uint64 {
	if tableRows == 0 {
		return 0
	}
	return uint64(float64(projectRows) / float64(tableRows) * float64(tableBytes))
}
