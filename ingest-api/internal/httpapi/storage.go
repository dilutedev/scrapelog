package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/apikeys"
	"github.com/dilutedev/scrapelog/ingest-api/internal/storage"
)

const storageQueryTimeout = 10 * time.Second

type StorageReporter interface {
	DiskUsedBytes(ctx context.Context) (uint64, error)
	ProjectStats(ctx context.Context, project string) (storage.ProjectStats, error)
	ProjectUsage(ctx context.Context) ([]storage.ProjectUsage, error)
}

type DiskGuard interface {
	Usage() storage.Usage
}

type storageReport struct {
	UsedBytes   uint64                 `json:"used_bytes"`
	BudgetBytes uint64                 `json:"budget_bytes"`
	Projects    []storage.ProjectUsage `json:"projects"`
}

func statsHandler(logger *slog.Logger, reporter StorageReporter) func(http.ResponseWriter, *http.Request, apikeys.Identity) {
	return func(responseWriter http.ResponseWriter, request *http.Request, identity apikeys.Identity) {
		queryContext, cancel := context.WithTimeout(request.Context(), storageQueryTimeout)
		defer cancel()
		stats, err := reporter.ProjectStats(queryContext, identity.Project)
		if err != nil {
			logger.Error("reading project stats failed", "error", err, "project", identity.Project)
			writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
			return
		}
		writeJSON(responseWriter, http.StatusOK, stats)
	}
}

func storageHandler(logger *slog.Logger, reporter StorageReporter, budgetBytes uint64) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		queryContext, cancel := context.WithTimeout(request.Context(), storageQueryTimeout)
		defer cancel()
		usedBytes, err := reporter.DiskUsedBytes(queryContext)
		if err != nil {
			logger.Error("measuring disk usage failed", "error", err)
			writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
			return
		}
		projects, err := reporter.ProjectUsage(queryContext)
		if err != nil {
			logger.Error("reading project usage failed", "error", err)
			writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
			return
		}
		writeJSON(responseWriter, http.StatusOK, storageReport{UsedBytes: usedBytes, BudgetBytes: budgetBytes, Projects: projects})
	}
}
