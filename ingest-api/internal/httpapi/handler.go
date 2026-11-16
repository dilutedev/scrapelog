package httpapi

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"net/http"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/ratelimit"
	"github.com/dilutedev/scrapelog/ingest-api/internal/session"
)

type ClickHousePinger interface {
	Ping(ctx context.Context) error
}

type KeyCache interface {
	KeyAuthenticator
	KeyRefresher
}

func NewHandler(shutdownContext context.Context, logger *slog.Logger, clickHouse ClickHousePinger, keyStore KeyStore, keyCache KeyCache, recordWriter RecordWriter, bodyStore BodyStore, storageReporter StorageReporter, diskGuard DiskGuard, loadedConfig config.Config) http.Handler {
	adminTokenHash := sha256.Sum256([]byte(loadedConfig.AdminToken))
	recordLimiter := ratelimit.New(loadedConfig.RateLimitRecordsPerMinute, time.Now)
	sessionSigner := session.New(loadedConfig.AdminToken, time.Now)
	loginLimiter := ratelimit.New(10, time.Now)
	router := http.NewServeMux()
	router.HandleFunc("GET /v1/health", healthHandler(logger, clickHouse))
	router.HandleFunc("GET /docs", docsHandler(logger, loadedConfig))
	router.HandleFunc("POST /v1/admin/keys", requireAdmin(adminTokenHash, createKeyHandler(logger, keyStore, keyCache)))
	router.HandleFunc("GET /v1/admin/keys", requireAdmin(adminTokenHash, listKeysHandler(logger, keyStore)))
	router.HandleFunc("DELETE /v1/admin/keys/{key_id}", requireAdmin(adminTokenHash, revokeKeyHandler(logger, keyStore, keyCache)))
	router.HandleFunc("POST /v1/requests", requireAPIKey(keyCache, ingestRequestsHandler(logger, recordWriter, bodyStore, recordLimiter, diskGuard, loadedConfig)))
	router.HandleFunc("POST /v1/requests/stream", requireAPIKey(keyCache, streamRequestsHandler(logger, recordWriter, bodyStore, recordLimiter, diskGuard, shutdownContext, loadedConfig, streamFlushInterval)))
	router.HandleFunc("GET /v1/stats", requireAPIKey(keyCache, statsHandler(logger, storageReporter)))
	router.HandleFunc("GET /v1/admin/storage", requireAdmin(adminTokenHash, storageHandler(logger, storageReporter, uint64(loadedConfig.ClickHouseDiskBudgetBytes))))
	router.HandleFunc("POST /ui/api/login", withSecurityHeaders(requireSameOriginJSON(loginHandler(sessionSigner, adminTokenHash, loginLimiter))))
	router.HandleFunc("POST /ui/api/logout", withSecurityHeaders(requireSameOriginJSON(logoutHandler())))
	router.HandleFunc("GET /ui/api/session", withSecurityHeaders(sessionStatusHandler(sessionSigner)))
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
