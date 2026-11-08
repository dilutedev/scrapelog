package httpapi

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"net/http"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/ratelimit"
)

type ClickHousePinger interface {
	Ping(ctx context.Context) error
}

type KeyCache interface {
	KeyAuthenticator
	KeyRefresher
}

func NewHandler(logger *slog.Logger, clickHouse ClickHousePinger, keyStore KeyStore, keyCache KeyCache, recordWriter RecordWriter, loadedConfig config.Config) http.Handler {
	adminTokenHash := sha256.Sum256([]byte(loadedConfig.AdminToken))
	recordLimiter := ratelimit.New(loadedConfig.RateLimitRecordsPerMinute, time.Now)
	router := http.NewServeMux()
	router.HandleFunc("GET /v1/health", healthHandler(logger, clickHouse))
	router.HandleFunc("POST /v1/admin/keys", requireAdmin(adminTokenHash, createKeyHandler(logger, keyStore, keyCache)))
	router.HandleFunc("GET /v1/admin/keys", requireAdmin(adminTokenHash, listKeysHandler(logger, keyStore)))
	router.HandleFunc("DELETE /v1/admin/keys/{key_id}", requireAdmin(adminTokenHash, revokeKeyHandler(logger, keyStore, keyCache)))
	router.HandleFunc("POST /v1/requests", requireAPIKey(keyCache, ingestRequestsHandler(logger, recordWriter, recordLimiter, loadedConfig)))
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
