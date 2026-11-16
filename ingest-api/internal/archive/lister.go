package archive

import (
	"context"
	"time"
)

const listQuery = "SELECT day, argMax(object_key, archived_at), argMax(row_count, archived_at), argMax(partial, archived_at), max(archived_at) FROM archive_days GROUP BY day ORDER BY day DESC"

type ArchivedDay struct {
	Day        time.Time
	ObjectKey  string
	RowCount   uint64
	Partial    bool
	ArchivedAt time.Time
}

func (archiver *Archiver) List(ctx context.Context) ([]ArchivedDay, error) {
	rows, err := archiver.connection.Query(ctx, listQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	archivedDays := []ArchivedDay{}
	for rows.Next() {
		var archivedDay ArchivedDay
		var partialFlag uint8
		if err := rows.Scan(&archivedDay.Day, &archivedDay.ObjectKey, &archivedDay.RowCount, &partialFlag, &archivedDay.ArchivedAt); err != nil {
			return nil, err
		}
		archivedDay.Day = archivedDay.Day.UTC()
		archivedDay.Partial = partialFlag != 0
		archivedDays = append(archivedDays, archivedDay)
	}
	return archivedDays, rows.Err()
}
