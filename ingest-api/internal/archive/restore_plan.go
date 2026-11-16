package archive

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/ClickHouse/clickhouse-go/v2"
)

const (
	maxRestoreDays    = 31
	restoreCapPercent = 80
	bytesPerMebibyte  = 1024 * 1024

	timeoutExceededCode = 159
	memoryLimitCode     = 241
)

var (
	ErrRestoreRunning  = errors.New("a restore is already running")
	ErrRestoreNotFound = errors.New("restore not found")
)

type RestoreRequest struct {
	From    time.Time
	To      time.Time
	Project string
	Domain  string
}

type RestorePlan struct {
	ArchivedDays   []time.Time
	ExcludedDays   []time.Time
	EstimatedRows  uint64
	EstimatedBytes uint64
	UsedBytes      uint64
	BudgetBytes    uint64
	Allowed        bool
	Reason         string
}

type RestoreRefusedError struct{ Reason string }

func (refused RestoreRefusedError) Error() string { return refused.Reason }

func PlanRestore(request RestoreRequest, archivedDays []ArchivedDay, usedBytes, budgetBytes, tableRows, tableBytes uint64) RestorePlan {
	plan := RestorePlan{ArchivedDays: []time.Time{}, ExcludedDays: []time.Time{}, UsedBytes: usedBytes, BudgetBytes: budgetBytes}
	firstDay := request.From.UTC().Truncate(24 * time.Hour)
	lastDay := request.To.Add(-time.Millisecond).UTC().Truncate(24 * time.Hour)
	var spannedDays []time.Time
	for day := firstDay; !day.After(lastDay); day = day.AddDate(0, 0, 1) {
		spannedDays = append(spannedDays, day)
		if len(spannedDays) > maxRestoreDays {
			plan.Reason = "the range can span at most 31 days"
			return plan
		}
	}
	rowCountByDay := map[string]uint64{}
	for _, archivedDay := range archivedDays {
		rowCountByDay[dayKey(archivedDay.Day)] = archivedDay.RowCount
	}
	for _, day := range spannedDays {
		rowCount, archived := rowCountByDay[dayKey(day)]
		if !archived {
			plan.ExcludedDays = append(plan.ExcludedDays, day)
			continue
		}
		plan.ArchivedDays = append(plan.ArchivedDays, day)
		plan.EstimatedRows += rowCount
	}
	if len(plan.ArchivedDays) == 0 {
		plan.Reason = "no archived days in this range"
		return plan
	}
	if tableRows == 0 {
		plan.Reason = "cannot estimate the size while the requests table is empty"
		return plan
	}
	plan.EstimatedBytes = uint64(float64(plan.EstimatedRows) * float64(tableBytes) / float64(tableRows))
	cappedBytes := budgetBytes * restoreCapPercent / 100
	if usedBytes+plan.EstimatedBytes > cappedBytes {
		var freeBytes uint64
		if cappedBytes > usedBytes {
			freeBytes = cappedBytes - usedBytes
		}
		plan.Reason = fmt.Sprintf("this restore needs about %d MiB but only %d MiB is free below 80%% of the disk budget", plan.EstimatedBytes/bytesPerMebibyte, freeBytes/bytesPerMebibyte)
		return plan
	}
	plan.Allowed = true
	return plan
}

func ValidRestoreID(restoreID string) bool {
	parsed, err := uuid.Parse(restoreID)
	return err == nil && parsed.String() == restoreID && parsed[6]>>4 == 7
}

func restoreFileGlob(days []time.Time) string {
	dayNames := make([]string, len(days))
	for index, day := range days {
		dayNames[index] = dayKey(day)
	}
	return "{" + strings.Join(dayNames, ",") + "}.parquet"
}

func failureMessage(err error) string {
	var exception *clickhouse.Exception
	if errors.As(err, &exception) {
		switch exception.Code {
		case timeoutExceededCode:
			return "the restore took longer than 15 minutes; restore a smaller range"
		case memoryLimitCode:
			return "the restore ran out of memory; restore a smaller range"
		}
	}
	return "the restore failed; see the server logs"
}
