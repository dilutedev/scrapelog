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
	"github.com/dilutedev/scrapelog/ingest-api/internal/ui"
	"github.com/dilutedev/scrapelog/ingest-api/internal/users"
)

type ClickHousePinger interface {
	Ping(ctx context.Context) error
}

type KeyCache interface {
	KeyAuthenticator
	KeyRefresher
}

func NewHandler(shutdownContext context.Context, logger *slog.Logger, clickHouse ClickHousePinger, keyStore KeyStore, keyCache KeyCache, recordWriter RecordWriter, bodyStore BodyStore, storageReporter StorageReporter, diskGuard DiskGuard, requestReader RequestReader, bodyGetter BodyGetter, alertStore AlertStore, alertRuleRefresher AlertRuleRefresher, alertTester AlertTester, alertMatcher AlertMatcher, alertQueue AlertQueue, archivingEnabled bool, archiveLister ArchiveLister, archiveDownloader ArchiveDownloader, restoreService RestoreService, userDirectory UserDirectory, loadedConfig config.Config) http.Handler {
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
	router.HandleFunc("POST /v1/requests", requireAPIKey(keyCache, ingestRequestsHandler(logger, recordWriter, bodyStore, alertMatcher, alertQueue, recordLimiter, diskGuard, loadedConfig)))
	router.HandleFunc("POST /v1/requests/stream", requireAPIKey(keyCache, streamRequestsHandler(logger, recordWriter, bodyStore, alertMatcher, alertQueue, recordLimiter, diskGuard, shutdownContext, loadedConfig, streamFlushInterval)))
	router.HandleFunc("GET /v1/stats", requireAPIKey(keyCache, statsHandler(logger, storageReporter)))
	router.HandleFunc("GET /v1/admin/storage", requireAdmin(adminTokenHash, storageHandler(logger, storageReporter, uint64(loadedConfig.ClickHouseDiskBudgetBytes))))
	router.HandleFunc("POST /ui/api/login", withSecurityHeaders(requireSameOriginJSON(loginHandler(sessionSigner, adminTokenHash, loginLimiter, userDirectory))))
	router.HandleFunc("POST /ui/api/logout", withSecurityHeaders(requireSameOriginJSON(logoutHandler())))
	router.HandleFunc("GET /ui/api/session", withSecurityHeaders(sessionStatusHandler(sessionSigner, userDirectory)))
	router.HandleFunc("GET /ui/api/requests", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ViewRequests}, searchRequestsHandler(logger, requestReader))))
	router.HandleFunc("GET /ui/api/requests/export", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ViewRequests}, exportRequestsHandler(logger, requestReader))))
	router.HandleFunc("GET /ui/api/request", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ViewRequests}, requestDetailHandler(logger, requestReader))))
	router.HandleFunc("GET /ui/api/request/body", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ViewRequests}, requestBodyHandler(logger, requestReader, bodyGetter))))
	router.HandleFunc("GET /ui/api/tape", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ViewRequests}, tapeHandler(logger, requestReader))))
	router.HandleFunc("GET /ui/api/facets", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ViewRequests, users.ViewBandwidth}, facetsHandler(logger, requestReader))))
	router.HandleFunc("GET /ui/api/bandwidth", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ViewBandwidth}, bandwidthHandler(logger, requestReader))))
	router.HandleFunc("GET /ui/api/alert-rules", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ViewAlerts, users.ManageAlerts}, listAlertRulesHandler(logger, alertStore))))
	router.HandleFunc("POST /ui/api/alert-rules", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ManageAlerts}, requireSameOriginJSON(createAlertRuleHandler(logger, alertStore, alertRuleRefresher)))))
	router.HandleFunc("PUT /ui/api/alert-rules/{rule_id}", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ManageAlerts}, requireSameOriginJSON(updateAlertRuleHandler(logger, alertStore, alertRuleRefresher)))))
	router.HandleFunc("DELETE /ui/api/alert-rules/{rule_id}", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ManageAlerts}, requireSameOriginJSON(deleteAlertRuleHandler(logger, alertStore, alertRuleRefresher)))))
	router.HandleFunc("POST /ui/api/alert-rules/{rule_id}/test", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ManageAlerts}, requireSameOriginJSON(testAlertRuleHandler(logger, alertStore, alertTester)))))
	router.HandleFunc("GET /ui/api/alert-events", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ViewAlerts, users.ManageAlerts}, listAlertEventsHandler(logger, alertStore))))
	router.HandleFunc("GET /ui/api/projects", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ManageAlerts}, listProjectsHandler(logger, keyStore))))
	router.HandleFunc("GET /ui/api/archives", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ViewArchives}, listArchivesHandler(logger, archivingEnabled, archiveLister))))
	router.HandleFunc("GET /ui/api/archives/{day}/download", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ViewArchives}, downloadArchiveHandler(logger, archivingEnabled, archiveLister, archiveDownloader))))
	router.HandleFunc("GET /ui/api/restores/estimate", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.EstimateRestores, users.ManageRestores}, estimateRestoreHandler(logger, archivingEnabled, restoreService))))
	router.HandleFunc("GET /ui/api/restores", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ViewRequests, users.ViewArchives, users.ManageRestores}, listRestoresHandler(logger, archivingEnabled, restoreService))))
	router.HandleFunc("POST /ui/api/restores", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ManageRestores}, requireSameOriginJSON(startRestoreHandler(logger, archivingEnabled, restoreService)))))
	router.HandleFunc("DELETE /ui/api/restores/{restore_id}", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{users.ManageRestores}, requireSameOriginJSON(deleteRestoreHandler(logger, archivingEnabled, restoreService)))))
	router.HandleFunc("GET /{$}", withSecurityHeaders(homeHandler(sessionSigner, userDirectory)))
	router.HandleFunc("GET /login", withSecurityHeaders(ui.PageHandler(logger, "login")))
	router.HandleFunc("GET /ui/api/users", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{}, listUsersHandler(logger, userDirectory))))
	router.HandleFunc("POST /ui/api/users", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{}, requireSameOriginJSON(createUserHandler(logger, userDirectory)))))
	router.HandleFunc("PUT /ui/api/users/{user_id}", withSecurityHeaders(requirePermission(sessionSigner, userDirectory, []string{}, requireSameOriginJSON(updateUserHandler(logger, userDirectory)))))
	router.HandleFunc("GET /bandwidth", withSecurityHeaders(requirePagePermission(sessionSigner, userDirectory, []string{users.ViewBandwidth}, ui.PageHandler(logger, "bandwidth"))))
	router.HandleFunc("GET /alerts", withSecurityHeaders(requirePagePermission(sessionSigner, userDirectory, []string{users.ViewAlerts, users.ManageAlerts}, ui.PageHandler(logger, "alerts"))))
	router.HandleFunc("GET /alerts/{rule_id}", withSecurityHeaders(requirePagePermission(sessionSigner, userDirectory, []string{users.ManageAlerts}, ui.PageHandler(logger, "alert-edit"))))
	router.HandleFunc("GET /archive", withSecurityHeaders(requirePagePermission(sessionSigner, userDirectory, []string{users.ViewArchives, users.EstimateRestores, users.ManageRestores}, ui.PageHandler(logger, "archive"))))
	router.HandleFunc("GET /requests", withSecurityHeaders(requirePagePermission(sessionSigner, userDirectory, []string{users.ViewRequests}, ui.PageHandler(logger, "requests"))))
	router.HandleFunc("GET /users", withSecurityHeaders(requirePagePermission(sessionSigner, userDirectory, []string{}, ui.PageHandler(logger, "users"))))
	router.HandleFunc("GET /assets/", withSecurityHeaders(ui.AssetHandler()))
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
