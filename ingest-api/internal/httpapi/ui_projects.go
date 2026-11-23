package httpapi

import (
	"log/slog"
	"net/http"
	"slices"
)

func listProjectsHandler(logger *slog.Logger, store KeyStore) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		records, err := store.List(request.Context())
		if err != nil {
			logger.Error("listing projects failed", "error", err)
			writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
			return
		}
		projectSet := make(map[string]struct{}, len(records))
		for _, record := range records {
			projectSet[record.Project] = struct{}{}
		}
		projectNames := make([]string, 0, len(projectSet))
		for projectName := range projectSet {
			projectNames = append(projectNames, projectName)
		}
		slices.Sort(projectNames)
		writeJSON(responseWriter, http.StatusOK, projectNames)
	}
}
