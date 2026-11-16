package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/archive"
)

type RestoreService interface {
	Estimate(ctx context.Context, request archive.RestoreRequest) (archive.RestorePlan, error)
	Start(ctx context.Context, request archive.RestoreRequest) (archive.Restore, error)
	ListRestores(ctx context.Context) ([]archive.Restore, error)
	Delete(ctx context.Context, restoreID string) error
}

type restoreRequestBody struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Project string `json:"project"`
	Domain  string `json:"domain"`
}

type restoreEstimateResponse struct {
	ArchivedDays   []string `json:"archived_days"`
	ExcludedDays   []string `json:"excluded_days"`
	EstimatedRows  uint64   `json:"estimated_rows"`
	EstimatedBytes uint64   `json:"estimated_bytes"`
	UsedBytes      uint64   `json:"used_bytes"`
	BudgetBytes    uint64   `json:"budget_bytes"`
	Allowed        bool     `json:"allowed"`
	Reason         string   `json:"reason"`
}

type restoreResponse struct {
	RestoreID string    `json:"restore_id"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Project   string    `json:"project"`
	Domain    string    `json:"domain"`
	Status    string    `json:"status"`
	RowCount  uint64    `json:"row_count"`
	Error     string    `json:"error"`
	CreatedAt time.Time `json:"created_at"`
}

type restoresResponse struct {
	ArchivingEnabled bool              `json:"archiving_enabled"`
	Restores         []restoreResponse `json:"restores"`
}

func buildRestoreRequest(fromText, toText, project, domain string) (archive.RestoreRequest, string) {
	from, fromErr := parseTimestamp(fromText)
	to, toErr := parseTimestamp(toText)
	if fromErr != nil || toErr != nil {
		return archive.RestoreRequest{}, "from and to are required RFC 3339 times"
	}
	if !from.Before(to) {
		return archive.RestoreRequest{}, "from must be before to"
	}
	return archive.RestoreRequest{From: from, To: to, Project: project, Domain: domain}, ""
}

func formatDays(days []time.Time) []string {
	formatted := make([]string, len(days))
	for index, day := range days {
		formatted[index] = day.Format(archiveDayLayout)
	}
	return formatted
}

func toRestoreResponse(restore archive.Restore) restoreResponse {
	return restoreResponse{RestoreID: restore.RestoreID, From: restore.From, To: restore.To, Project: restore.Project, Domain: restore.Domain, Status: restore.Status, RowCount: restore.RowCount, Error: restore.Error, CreatedAt: restore.CreatedAt}
}

func estimateRestoreHandler(logger *slog.Logger, archivingEnabled bool, restoreService RestoreService) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Cache-Control", "no-store")
		if !archivingEnabled {
			writeError(responseWriter, http.StatusNotFound, "archiving is disabled")
			return
		}
		queryValues := request.URL.Query()
		restoreRequest, message := buildRestoreRequest(queryValues.Get("from"), queryValues.Get("to"), queryValues.Get("project"), queryValues.Get("domain"))
		if message != "" {
			writeError(responseWriter, http.StatusBadRequest, message)
			return
		}
		plan, err := restoreService.Estimate(request.Context(), restoreRequest)
		if err != nil {
			logger.Error("estimating restore failed", "error", err)
			writeError(responseWriter, http.StatusInternalServerError, "could not estimate restore")
			return
		}
		writeJSON(responseWriter, http.StatusOK, restoreEstimateResponse{ArchivedDays: formatDays(plan.ArchivedDays), ExcludedDays: formatDays(plan.ExcludedDays), EstimatedRows: plan.EstimatedRows, EstimatedBytes: plan.EstimatedBytes, UsedBytes: plan.UsedBytes, BudgetBytes: plan.BudgetBytes, Allowed: plan.Allowed, Reason: plan.Reason})
	}
}

func startRestoreHandler(logger *slog.Logger, archivingEnabled bool, restoreService RestoreService) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if !archivingEnabled {
			writeError(responseWriter, http.StatusNotFound, "archiving is disabled")
			return
		}
		var body restoreRequestBody
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeError(responseWriter, http.StatusBadRequest, "from and to are required RFC 3339 times")
			return
		}
		restoreRequest, message := buildRestoreRequest(body.From, body.To, body.Project, body.Domain)
		if message != "" {
			writeError(responseWriter, http.StatusBadRequest, message)
			return
		}
		restore, err := restoreService.Start(request.Context(), restoreRequest)
		var refused archive.RestoreRefusedError
		if errors.Is(err, archive.ErrRestoreRunning) {
			writeError(responseWriter, http.StatusConflict, err.Error())
			return
		}
		if errors.As(err, &refused) {
			writeError(responseWriter, http.StatusUnprocessableEntity, refused.Reason)
			return
		}
		if err != nil {
			logger.Error("starting restore failed", "error", err)
			writeError(responseWriter, http.StatusInternalServerError, "could not start restore")
			return
		}
		writeJSON(responseWriter, http.StatusAccepted, toRestoreResponse(restore))
	}
}

func listRestoresHandler(logger *slog.Logger, archivingEnabled bool, restoreService RestoreService) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Cache-Control", "no-store")
		responses := []restoreResponse{}
		if archivingEnabled {
			restores, err := restoreService.ListRestores(request.Context())
			if err != nil {
				logger.Error("listing restores failed", "error", err)
				writeError(responseWriter, http.StatusInternalServerError, "could not list restores")
				return
			}
			for _, restore := range restores {
				responses = append(responses, toRestoreResponse(restore))
			}
		}
		writeJSON(responseWriter, http.StatusOK, restoresResponse{ArchivingEnabled: archivingEnabled, Restores: responses})
	}
}

func deleteRestoreHandler(logger *slog.Logger, archivingEnabled bool, restoreService RestoreService) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if !archivingEnabled {
			writeError(responseWriter, http.StatusNotFound, "archiving is disabled")
			return
		}
		restoreID := request.PathValue("restore_id")
		if !archive.ValidRestoreID(restoreID) {
			writeError(responseWriter, http.StatusBadRequest, "invalid restore id")
			return
		}
		err := restoreService.Delete(request.Context(), restoreID)
		if errors.Is(err, archive.ErrRestoreNotFound) {
			writeError(responseWriter, http.StatusNotFound, "restore not found")
			return
		}
		if err != nil {
			logger.Error("deleting restore failed", "restore_id", restoreID, "error", err)
			writeError(responseWriter, http.StatusInternalServerError, "could not delete restore")
			return
		}
		responseWriter.WriteHeader(http.StatusNoContent)
	}
}
