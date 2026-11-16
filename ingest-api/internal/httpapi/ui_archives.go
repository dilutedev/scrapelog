package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/archive"
	"github.com/dilutedev/scrapelog/ingest-api/internal/bodies"
)

const archiveDayLayout = "2006-01-02"

type ArchiveLister interface {
	List(ctx context.Context) ([]archive.ArchivedDay, error)
}

type ArchiveDownloader interface {
	Open(ctx context.Context, key string) (io.ReadCloser, int64, error)
}

type archiveSummary struct {
	Day        string    `json:"day"`
	RowCount   uint64    `json:"row_count"`
	Partial    bool      `json:"partial"`
	ArchivedAt time.Time `json:"archived_at"`
}

type archivesResponse struct {
	ArchivingEnabled bool             `json:"archiving_enabled"`
	Archives         []archiveSummary `json:"archives"`
}

func listArchivesHandler(logger *slog.Logger, archivingEnabled bool, archiveLister ArchiveLister) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Cache-Control", "no-store")
		summaries := []archiveSummary{}
		if archivingEnabled {
			archivedDays, err := archiveLister.List(request.Context())
			if err != nil {
				logger.Error("listing archives failed", "error", err)
				writeError(responseWriter, http.StatusInternalServerError, "could not list archives")
				return
			}
			for _, archivedDay := range archivedDays {
				summaries = append(summaries, archiveSummary{Day: archivedDay.Day.Format(archiveDayLayout), RowCount: archivedDay.RowCount, Partial: archivedDay.Partial, ArchivedAt: archivedDay.ArchivedAt.UTC()})
			}
		}
		writeJSON(responseWriter, http.StatusOK, archivesResponse{ArchivingEnabled: archivingEnabled, Archives: summaries})
	}
}

func downloadArchiveHandler(logger *slog.Logger, archivingEnabled bool, archiveLister ArchiveLister, archiveDownloader ArchiveDownloader) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if !archivingEnabled {
			writeError(responseWriter, http.StatusNotFound, "archiving is disabled")
			return
		}
		rawDay := request.PathValue("day")
		parsedDay, err := time.Parse(archiveDayLayout, rawDay)
		if err != nil || parsedDay.Format(archiveDayLayout) != rawDay {
			writeError(responseWriter, http.StatusBadRequest, "day must be YYYY-MM-DD")
			return
		}
		archivedDays, err := archiveLister.List(request.Context())
		if err != nil {
			logger.Error("listing archives failed", "error", err)
			writeError(responseWriter, http.StatusInternalServerError, "could not list archives")
			return
		}
		var entry archive.ArchivedDay
		found := false
		for _, archivedDay := range archivedDays {
			if archivedDay.Day.Format(archiveDayLayout) == rawDay {
				entry = archivedDay
				found = true
				break
			}
		}
		if !found {
			writeError(responseWriter, http.StatusNotFound, "archive not found")
			return
		}
		archiveFile, size, err := archiveDownloader.Open(request.Context(), entry.ObjectKey)
		if errors.Is(err, bodies.ErrBodyNotFound) {
			writeError(responseWriter, http.StatusNotFound, "archive file not found")
			return
		}
		if err != nil {
			logger.Error("opening archive failed", "day", rawDay, "error", err)
			writeError(responseWriter, http.StatusBadGateway, "could not read archive")
			return
		}
		defer archiveFile.Close()
		responseWriter.Header().Set("Content-Type", "application/vnd.apache.parquet")
		responseWriter.Header().Set("Content-Disposition", `attachment; filename="scrapelog-requests-`+rawDay+`.parquet"`)
		responseWriter.Header().Set("Cache-Control", "no-store")
		if size >= 0 {
			responseWriter.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		}
		responseWriter.WriteHeader(http.StatusOK)
		if _, err := io.Copy(responseWriter, archiveFile); err != nil {
			if request.Context().Err() == nil {
				logger.Error("streaming archive failed", "day", rawDay, "error", err)
			}
			panic(http.ErrAbortHandler)
		}
	}
}
