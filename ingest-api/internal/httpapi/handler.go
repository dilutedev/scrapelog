package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

type ClickHousePinger interface {
	Ping(ctx context.Context) error
}

func NewHandler(logger *slog.Logger, clickHouse ClickHousePinger) http.Handler {
	router := http.NewServeMux()
	router.HandleFunc("GET /v1/health", healthHandler(logger, clickHouse))
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
