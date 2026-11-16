package archive

import (
	"context"
	"log/slog"
	"time"
	"uuid"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const (
	restoreExpiry = 7 * 24 * time.Hour

	statusRunning = "running"
	statusReady   = "ready"
	statusFailed  = "failed"
	statusDeleted = "deleted"
	statusExpired = "expired"

	restoreInsert = `INSERT INTO requests_restored (received_at, ts, project, scraper, request_id, method, url, domain, status, latency_ms, proxy, http_version, tls_ja3, tls_ja4, tls_profile, request_headers, response_headers, response_size, body_key, request_body, request_body_encoding, error, tags, restore_id, restored_at)
SELECT received_at, ts, project, scraper, request_id, method, url, domain, status, latency_ms, proxy, http_version, tls_ja3, tls_ja4, tls_profile, request_headers, response_headers, response_size, body_key, request_body, request_body_encoding, error, tags, {restore_id:String}, now()
FROM s3(archive_s3, filename = {file_glob:String}, format = 'Parquet')
WHERE ts >= {range_from:DateTime64(3)} AND ts < {range_to:DateTime64(3)}
  AND ({project:String} = '' OR project = {project:String})
  AND ({domain:String} = '' OR domain = {domain:String})`
	restoreCountQuery  = "SELECT count() FROM requests_restored WHERE restore_id = {restore_id:String}"
	restoreKillQuery   = "KILL QUERY WHERE query_id = {restore_id:String} SYNC"
	dropPartitionQuery = "ALTER TABLE requests_restored DROP PARTITION "
	restoreSelectQuery = "SELECT restore_id, range_from, range_to, project, domain, status, row_count, error, created_at, updated_at FROM archive_restores FINAL "
	listRestoresQuery  = restoreSelectQuery + "ORDER BY created_at DESC"
	getRestoreQuery    = restoreSelectQuery + "WHERE restore_id = {restore_id:String}"
	runningQuery       = restoreSelectQuery + "WHERE status = 'running'"
	statusInsert       = "INSERT INTO archive_restores (restore_id, range_from, range_to, project, domain, status, row_count, error, created_at, updated_at)"
)

var restoreInsertSettings = clickhouse.Settings{
	"min_insert_block_size_bytes":    16 * 1024 * 1024,
	"max_threads":                    1,
	"max_parsing_threads":            1,
	"remote_read_min_bytes_for_seek": 65536,
}

type Restore struct {
	RestoreID string
	From, To  time.Time
	Project   string
	Domain    string
	Status    string
	RowCount  uint64
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type diskSizer interface {
	DiskUsedBytes(ctx context.Context) (uint64, error)
	RequestsTableSize(ctx context.Context) (uint64, uint64, error)
}

type Restorer struct {
	connection  driver.Conn
	archiver    *Archiver
	sizer       diskSizer
	budgetBytes uint64
	logger      *slog.Logger
	slot        restoreSlot
}

func NewRestorer(connection driver.Conn, archiver *Archiver, sizer diskSizer, budgetBytes uint64, logger *slog.Logger) *Restorer {
	return &Restorer{connection: connection, archiver: archiver, sizer: sizer, budgetBytes: budgetBytes, logger: logger}
}

func displayStatus(status string, createdAt, now time.Time) string {
	if status == statusReady && !now.Before(createdAt.Add(restoreExpiry)) {
		return statusExpired
	}
	return status
}

func nextUpdatedAt(previous time.Time) time.Time {
	updatedAt := time.Now().UTC()
	earliestWinningTime := previous.Add(time.Millisecond)
	if updatedAt.Before(earliestWinningTime) {
		updatedAt = earliestWinningTime
	}
	return updatedAt
}

func (restorer *Restorer) Estimate(ctx context.Context, request RestoreRequest) (RestorePlan, error) {
	archivedDays, err := restorer.archiver.List(ctx)
	if err != nil {
		return RestorePlan{}, err
	}
	usedBytes, err := restorer.sizer.DiskUsedBytes(ctx)
	if err != nil {
		return RestorePlan{}, err
	}
	tableRows, tableBytes, err := restorer.sizer.RequestsTableSize(ctx)
	if err != nil {
		return RestorePlan{}, err
	}
	return PlanRestore(request, archivedDays, usedBytes, restorer.budgetBytes, tableRows, tableBytes), nil
}

func (restorer *Restorer) Start(ctx context.Context, request RestoreRequest) (Restore, error) {
	restoreID := uuid.NewV7().String()
	if !restorer.slot.acquire(restoreID) {
		return Restore{}, ErrRestoreRunning
	}
	plan, err := restorer.Estimate(ctx, request)
	if err != nil {
		restorer.slot.release()
		return Restore{}, err
	}
	if !plan.Allowed {
		restorer.slot.release()
		return Restore{}, RestoreRefusedError{Reason: plan.Reason}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	restore := Restore{RestoreID: restoreID, From: request.From, To: request.To, Project: request.Project, Domain: request.Domain, Status: statusRunning, CreatedAt: now, UpdatedAt: now}
	if err := restorer.writeStatus(ctx, restore); err != nil {
		restorer.slot.release()
		return Restore{}, err
	}
	go restorer.run(restore, restoreFileGlob(plan.ArchivedDays))
	return restore, nil
}

func (restorer *Restorer) run(restore Restore, fileGlob string) {
	insertContext := clickhouse.Context(context.Background(), clickhouse.WithQueryID(restore.RestoreID), clickhouse.WithSettings(restoreInsertSettings))
	insertErr := restorer.connection.Exec(insertContext, restoreInsert,
		clickhouse.Named("restore_id", restore.RestoreID),
		clickhouse.Named("file_glob", fileGlob),
		clickhouse.Named("range_from", restore.From),
		clickhouse.Named("range_to", restore.To),
		clickhouse.Named("project", restore.Project),
		clickhouse.Named("domain", restore.Domain),
	)
	restorer.slot.finish(func(stopped bool) {
		ctx := context.Background()
		if stopped {
			if err := restorer.dropPartition(ctx, restore.RestoreID); err != nil {
				restorer.logger.Warn("dropping stopped restore failed", "restore_id", restore.RestoreID, "error", err)
			}
			return
		}
		var rowCount uint64
		if insertErr == nil {
			insertErr = restorer.connection.QueryRow(ctx, restoreCountQuery, clickhouse.Named("restore_id", restore.RestoreID)).Scan(&rowCount)
		}
		if insertErr != nil {
			restorer.logger.Warn("restore failed", "restore_id", restore.RestoreID, "error", insertErr)
			if err := restorer.dropPartition(ctx, restore.RestoreID); err != nil {
				restorer.logger.Warn("dropping failed restore failed", "restore_id", restore.RestoreID, "error", err)
			}
			restore.Status = statusFailed
			restore.Error = failureMessage(insertErr)
		} else {
			restore.Status = statusReady
			restore.RowCount = rowCount
		}
		if err := restorer.writeStatus(ctx, restore); err != nil {
			restorer.logger.Warn("writing restore status failed", "restore_id", restore.RestoreID, "error", err)
			return
		}
		if restore.Status == statusReady {
			restorer.logger.Info("restore ready", "restore_id", restore.RestoreID, "row_count", rowCount)
		}
	})
}

func (restorer *Restorer) ListRestores(ctx context.Context) ([]Restore, error) {
	restores, err := restorer.queryRestores(ctx, listRestoresQuery)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for index := range restores {
		restores[index].Status = displayStatus(restores[index].Status, restores[index].CreatedAt, now)
	}
	return restores, nil
}

func (restorer *Restorer) Delete(ctx context.Context, restoreID string) error {
	restores, err := restorer.queryRestores(ctx, getRestoreQuery, clickhouse.Named("restore_id", restoreID))
	if err != nil {
		return err
	}
	if len(restores) == 0 {
		return ErrRestoreNotFound
	}
	restore := restores[0]
	if restorer.slot.requestStop(restoreID) {
		if err := restorer.kill(ctx, restoreID); err != nil {
			return err
		}
	}
	if err := restorer.dropPartition(ctx, restoreID); err != nil {
		return err
	}
	if restore.Status == statusDeleted {
		return nil
	}
	restore.Status = statusDeleted
	return restorer.writeStatus(ctx, restore)
}

func (restorer *Restorer) StopInterrupted(ctx context.Context) {
	interrupted, err := restorer.queryRestores(ctx, runningQuery)
	if err != nil {
		restorer.logger.Warn("reading interrupted restores failed", "error", err)
		return
	}
	for _, restore := range interrupted {
		if err := restorer.stopInterrupted(ctx, restore); err != nil {
			restorer.logger.Warn("stopping interrupted restore failed", "restore_id", restore.RestoreID, "error", err)
		}
	}
}

func (restorer *Restorer) stopInterrupted(ctx context.Context, restore Restore) error {
	if err := restorer.kill(ctx, restore.RestoreID); err != nil {
		return err
	}
	if err := restorer.dropPartition(ctx, restore.RestoreID); err != nil {
		return err
	}
	restore.Status = statusFailed
	restore.Error = "interrupted by a restart"
	return restorer.writeStatus(ctx, restore)
}

func (restorer *Restorer) kill(ctx context.Context, restoreID string) error {
	return restorer.connection.Exec(ctx, restoreKillQuery, clickhouse.Named("restore_id", restoreID))
}

func (restorer *Restorer) dropPartition(ctx context.Context, restoreID string) error {
	if !ValidRestoreID(restoreID) {
		return RestoreRefusedError{Reason: "invalid restore id"}
	}
	return restorer.connection.Exec(ctx, dropPartitionQuery+"'"+restoreID+"'")
}

func (restorer *Restorer) queryRestores(ctx context.Context, query string, args ...any) ([]Restore, error) {
	rows, err := restorer.connection.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	restores := []Restore{}
	for rows.Next() {
		var restore Restore
		if err := rows.Scan(&restore.RestoreID, &restore.From, &restore.To, &restore.Project, &restore.Domain, &restore.Status, &restore.RowCount, &restore.Error, &restore.CreatedAt, &restore.UpdatedAt); err != nil {
			return nil, err
		}
		restore.From = restore.From.UTC()
		restore.To = restore.To.UTC()
		restore.CreatedAt = restore.CreatedAt.UTC()
		restore.UpdatedAt = restore.UpdatedAt.UTC()
		restores = append(restores, restore)
	}
	return restores, rows.Err()
}

func (restorer *Restorer) writeStatus(ctx context.Context, restore Restore) error {
	batch, err := restorer.connection.PrepareBatch(ctx, statusInsert)
	if err != nil {
		return err
	}
	if err := batch.Append(restore.RestoreID, restore.From, restore.To, restore.Project, restore.Domain, restore.Status, restore.RowCount, restore.Error, restore.CreatedAt, nextUpdatedAt(restore.UpdatedAt)); err != nil {
		return err
	}
	return batch.Send()
}
