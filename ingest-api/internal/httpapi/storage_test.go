package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/storage"
)

type fakeStorageReporter struct {
	usedBytes        uint64
	stats            storage.ProjectStats
	usage            []storage.ProjectUsage
	err              error
	requestedProject string
}

func (reporter *fakeStorageReporter) DiskUsedBytes(ctx context.Context) (uint64, error) {
	return reporter.usedBytes, reporter.err
}

func (reporter *fakeStorageReporter) ProjectStats(ctx context.Context, project string) (storage.ProjectStats, error) {
	reporter.requestedProject = project
	return reporter.stats, reporter.err
}

func (reporter *fakeStorageReporter) ProjectUsage(ctx context.Context) ([]storage.ProjectUsage, error) {
	return reporter.usage, reporter.err
}

type fakeDiskGuard struct {
	usage storage.Usage
}

func (guard *fakeDiskGuard) Usage() storage.Usage {
	return guard.usage
}

func serveStorageRoute(reporter *fakeStorageReporter, path string, authorization string) *httptest.ResponseRecorder {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	loadedConfig := config.Config{AdminToken: "admin-token", ClickHouseDiskBudgetBytes: 5000}
	NewHandler(logger, &fakePinger{}, &fakeKeyStore{}, &fakeRefresher{}, &fakeRecordWriter{}, reporter, &fakeDiskGuard{}, loadedConfig).ServeHTTP(recorder, request)
	return recorder
}

func TestStatsReturnsStatsForKeyProject(t *testing.T) {
	oldest := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	newest := time.Date(2026, 11, 8, 11, 0, 0, 0, time.UTC)
	reporter := &fakeStorageReporter{stats: storage.ProjectStats{Project: "acme", RowCount: 1000, OldestTimestamp: &oldest, NewestTimestamp: &newest, EstimatedBytesOnDisk: 123456}}

	recorder := serveStorageRoute(reporter, "/v1/stats?project=other", "Bearer good-key")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
	}
	if reporter.requestedProject != "acme" {
		t.Fatalf("reporter asked for project %q, want acme", reporter.requestedProject)
	}
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	expected := map[string]any{"project": "acme", "row_count": float64(1000), "oldest_ts": "2026-11-08T10:00:00Z", "newest_ts": "2026-11-08T11:00:00Z", "estimated_bytes_on_disk": float64(123456)}
	if !reflect.DeepEqual(decoded, expected) {
		t.Fatalf("got %v, want %v", decoded, expected)
	}
}

func TestStatsReturnsNullTimestampsForEmptyProject(t *testing.T) {
	reporter := &fakeStorageReporter{stats: storage.ProjectStats{Project: "acme"}}

	recorder := serveStorageRoute(reporter, "/v1/stats", "Bearer good-key")

	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["oldest_ts"] != nil || decoded["newest_ts"] != nil || decoded["row_count"] != float64(0) {
		t.Fatalf("got %v", decoded)
	}
}

func TestStatsRequiresKeyAndReportsUnavailable(t *testing.T) {
	if recorder := serveStorageRoute(&fakeStorageReporter{}, "/v1/stats", ""); recorder.Code != http.StatusUnauthorized {
		t.Errorf("no key: status %d, want 401", recorder.Code)
	}
	if recorder := serveStorageRoute(&fakeStorageReporter{err: errors.New("down")}, "/v1/stats", "Bearer good-key"); recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("reporter error: status %d, want 503", recorder.Code)
	}
}

func TestAdminStorageReturnsUsageAndBudget(t *testing.T) {
	reporter := &fakeStorageReporter{usedBytes: 4200, usage: []storage.ProjectUsage{{Project: "acme", RowCount: 10, EstimatedBytesOnDisk: 99}}}

	recorder := serveStorageRoute(reporter, "/v1/admin/storage", "Bearer admin-token")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	expected := map[string]any{
		"used_bytes":   float64(4200),
		"budget_bytes": float64(5000),
		"projects":     []any{map[string]any{"project": "acme", "row_count": float64(10), "estimated_bytes_on_disk": float64(99)}},
	}
	if !reflect.DeepEqual(decoded, expected) {
		t.Fatalf("got %v, want %v", decoded, expected)
	}
}

func TestAdminStorageRejectsAPIKeyAndReportsUnavailable(t *testing.T) {
	if recorder := serveStorageRoute(&fakeStorageReporter{}, "/v1/admin/storage", "Bearer good-key"); recorder.Code != http.StatusUnauthorized {
		t.Errorf("api key: status %d, want 401", recorder.Code)
	}
	if recorder := serveStorageRoute(&fakeStorageReporter{err: errors.New("down")}, "/v1/admin/storage", "Bearer admin-token"); recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("reporter error: status %d, want 503", recorder.Code)
	}
}
