package httpapi

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"net/http"
	"time"
)

type ClickHousePinger interface {
	Ping(ctx context.Context) error
}

func NewHandler(logger *slog.Logger, clickHouse ClickHousePinger, keyStore KeyStore, keyRefresher KeyRefresher, adminToken string) http.Handler {
	adminTokenHash := sha256.Sum256([]byte(adminToken))
	router := http.NewServeMux()
	router.HandleFunc("GET /v1/health", healthHandler(logger, clickHouse))
	router.HandleFunc("POST /v1/admin/keys", requireAdmin(adminTokenHash, createKeyHandler(logger, keyStore, keyRefresher)))
	router.HandleFunc("GET /v1/admin/keys", requireAdmin(adminTokenHash, listKeysHandler(logger, keyStore)))
	router.HandleFunc("DELETE /v1/admin/keys/{key_id}", requireAdmin(adminTokenHash, revokeKeyHandler(logger, keyStore, keyRefresher)))
	return router
}

func healthHandler(logger *slog.Logger, clickHouse ClickHousePinger) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		pingContext, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()

		responseWriter.Header().Set("Content-Type", "application/json")
		if err := clickHouse.Ping(pingContext); err != nil {
			logger.Warn("clickhouse ping failed", "error", err)
			responseWriter.WriteHeader(http.StatusServiceUnavailable)
			responseWriter.Write([]byte(`{"status":"clickhouse unavailable"}`))
			return
		}
		responseWriter.Write([]byte(`{"status":"ok"}`))
	}
}
