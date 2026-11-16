package archive

import (
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func archivedDaysWithRows(rowCounts map[time.Time]uint64) []ArchivedDay {
	archivedDays := []ArchivedDay{}
	for day, rowCount := range rowCounts {
		archivedDays = append(archivedDays, ArchivedDay{Day: day, RowCount: rowCount})
	}
	return archivedDays
}

func TestPlanRestoreEstimatesRowsAndBytes(t *testing.T) {
	archivedDays := archivedDaysWithRows(map[time.Time]uint64{utcDay(2026, 9, 1): 1000, utcDay(2026, 9, 2): 3000})
	request := RestoreRequest{From: utcDay(2026, 9, 1), To: utcDay(2026, 9, 3)}

	plan := PlanRestore(request, archivedDays, 0, 100_000_000, 100, 70000)

	if !plan.Allowed {
		t.Fatalf("plan refused: %s", plan.Reason)
	}
	if plan.EstimatedRows != 4000 || plan.EstimatedBytes != 2_800_000 {
		t.Fatalf("estimated %d rows %d bytes, want 4000 and 2800000", plan.EstimatedRows, plan.EstimatedBytes)
	}
}

func TestPlanRestoreListsExcludedDays(t *testing.T) {
	archivedDays := archivedDaysWithRows(map[time.Time]uint64{utcDay(2026, 9, 1): 10, utcDay(2026, 9, 3): 10})
	request := RestoreRequest{From: utcDay(2026, 9, 1), To: utcDay(2026, 9, 4)}

	plan := PlanRestore(request, archivedDays, 0, 100_000_000, 100, 70000)

	if len(plan.ArchivedDays) != 2 || !plan.ArchivedDays[0].Equal(utcDay(2026, 9, 1)) || !plan.ArchivedDays[1].Equal(utcDay(2026, 9, 3)) {
		t.Fatalf("unexpected archived days: %v", plan.ArchivedDays)
	}
	if len(plan.ExcludedDays) != 1 || !plan.ExcludedDays[0].Equal(utcDay(2026, 9, 2)) {
		t.Fatalf("unexpected excluded days: %v", plan.ExcludedDays)
	}
}

func TestPlanRestoreDaySpan(t *testing.T) {
	tests := map[string]struct {
		from        time.Time
		to          time.Time
		wantAllowed bool
		wantReason  string
		wantDays    int
	}{
		"to at midnight excludes that day": {from: utcDay(2026, 9, 1), to: utcDay(2026, 9, 2), wantAllowed: true, wantDays: 1},
		"31 days allowed":                  {from: utcDay(2026, 9, 1), to: utcDay(2026, 10, 2), wantAllowed: true, wantDays: 31},
		"32 days refused":                  {from: utcDay(2026, 9, 1), to: utcDay(2026, 10, 3), wantReason: "the range can span at most 31 days"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			rowCounts := map[time.Time]uint64{}
			for day := test.from; day.Before(test.to); day = day.AddDate(0, 0, 1) {
				rowCounts[day] = 10
			}
			plan := PlanRestore(RestoreRequest{From: test.from, To: test.to}, archivedDaysWithRows(rowCounts), 0, 100_000_000, 100, 70000)
			if plan.Allowed != test.wantAllowed {
				t.Fatalf("allowed %v, want %v (%s)", plan.Allowed, test.wantAllowed, plan.Reason)
			}
			if test.wantReason != "" && plan.Reason != test.wantReason {
				t.Fatalf("reason %q, want %q", plan.Reason, test.wantReason)
			}
			if len(plan.ArchivedDays) != test.wantDays {
				t.Fatalf("%d archived days, want %d", len(plan.ArchivedDays), test.wantDays)
			}
		})
	}
}

func TestPlanRestoreCap(t *testing.T) {
	archivedDays := archivedDaysWithRows(map[time.Time]uint64{utcDay(2026, 9, 1): 100})
	request := RestoreRequest{From: utcDay(2026, 9, 1), To: utcDay(2026, 9, 2)}
	tests := map[string]struct {
		usedBytes   uint64
		wantAllowed bool
		wantReason  string
	}{
		"exactly at the cap": {usedBytes: 799_930, wantAllowed: true},
		"one byte over":      {usedBytes: 799_931, wantReason: "80%"},
		"already above":      {usedBytes: 900_000, wantReason: "only 0 MiB is free"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plan := PlanRestore(request, archivedDays, test.usedBytes, 1_000_000, 100, 70)
			if plan.EstimatedBytes != 70 {
				t.Fatalf("estimated %d bytes, want 70", plan.EstimatedBytes)
			}
			if plan.Allowed != test.wantAllowed {
				t.Fatalf("allowed %v, want %v (%s)", plan.Allowed, test.wantAllowed, plan.Reason)
			}
			if !strings.Contains(plan.Reason, test.wantReason) {
				t.Fatalf("reason %q does not contain %q", plan.Reason, test.wantReason)
			}
		})
	}
}

func TestPlanRestoreRefusals(t *testing.T) {
	request := RestoreRequest{From: utcDay(2026, 9, 1), To: utcDay(2026, 9, 2)}
	archivedDays := archivedDaysWithRows(map[time.Time]uint64{utcDay(2026, 9, 1): 100})

	noDays := PlanRestore(request, nil, 0, 1_000_000, 100, 70)
	if noDays.Allowed || noDays.Reason != "no archived days in this range" {
		t.Fatalf("unexpected plan: %+v", noDays)
	}
	emptyTable := PlanRestore(request, archivedDays, 0, 1_000_000, 0, 0)
	if emptyTable.Allowed || emptyTable.Reason != "cannot estimate the size while the requests table is empty" {
		t.Fatalf("unexpected plan: %+v", emptyTable)
	}
}

func TestValidRestoreID(t *testing.T) {
	tests := map[string]struct {
		restoreID string
		want      bool
	}{
		"v7":           {restoreID: uuid.NewV7().String(), want: true},
		"v4":           {restoreID: uuid.NewV4().String()},
		"uppercase":    {restoreID: strings.ToUpper(uuid.NewV7().String())},
		"braces":       {restoreID: "{" + uuid.NewV7().String() + "}"},
		"trailing":     {restoreID: uuid.NewV7().String() + "'"},
		"injection":    {restoreID: "' OR 1=1"},
		"empty string": {restoreID: ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := ValidRestoreID(test.restoreID); got != test.want {
				t.Fatalf("ValidRestoreID(%q) = %v, want %v", test.restoreID, got, test.want)
			}
		})
	}
}

func TestRestoreFileGlob(t *testing.T) {
	if got := restoreFileGlob([]time.Time{utcDay(2026, 1, 1)}); got != "{2026-01-01}.parquet" {
		t.Fatalf("unexpected glob %q", got)
	}
	if got := restoreFileGlob([]time.Time{utcDay(2026, 1, 1), utcDay(2026, 1, 2), utcDay(2026, 1, 5)}); got != "{2026-01-01,2026-01-02,2026-01-05}.parquet" {
		t.Fatalf("unexpected glob %q", got)
	}
}

func TestFailureMessage(t *testing.T) {
	tests := map[string]struct {
		err  error
		want string
	}{
		"timeout": {err: &clickhouse.Exception{Code: 159, Message: "too slow"}, want: "the restore took longer than 15 minutes; restore a smaller range"},
		"memory":  {err: &clickhouse.Exception{Code: 241, Message: "too big"}, want: "the restore ran out of memory; restore a smaller range"},
		"other":   {err: &clickhouse.Exception{Code: 1, Message: "secret-key-value"}, want: "the restore failed; see the server logs"},
		"plain":   {err: errors.New("secret-key-value"), want: "the restore failed; see the server logs"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := failureMessage(test.err)
			if got != test.want {
				t.Fatalf("message %q, want %q", got, test.want)
			}
			if strings.Contains(got, "secret-key-value") {
				t.Fatalf("message leaks the error text: %q", got)
			}
		})
	}
}

func TestDisplayStatus(t *testing.T) {
	createdAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		status string
		now    time.Time
		want   string
	}{
		"ready within a week":   {status: "ready", now: createdAt.Add(7*24*time.Hour - time.Second), want: "ready"},
		"ready after a week":    {status: "ready", now: createdAt.Add(7 * 24 * time.Hour), want: "expired"},
		"failed stays failed":   {status: "failed", now: createdAt.Add(30 * 24 * time.Hour), want: "failed"},
		"running stays running": {status: "running", now: createdAt.Add(30 * 24 * time.Hour), want: "running"},
		"deleted stays deleted": {status: "deleted", now: createdAt.Add(30 * 24 * time.Hour), want: "deleted"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := displayStatus(test.status, createdAt, test.now); got != test.want {
				t.Fatalf("status %q, want %q", got, test.want)
			}
		})
	}
}
