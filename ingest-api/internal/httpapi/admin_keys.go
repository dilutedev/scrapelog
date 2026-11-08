package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/apikeys"
)

const (
	maxCreateKeyBodyBytes = 4096
	maxKeyNameLength      = 128
)

var projectPattern = regexp.MustCompile("^[a-z0-9][a-z0-9_-]{0,63}$")

type KeyStore interface {
	Create(ctx context.Context, project string, name string) (string, string, error)
	List(ctx context.Context) ([]apikeys.Record, error)
	Revoke(ctx context.Context, keyID string) error
}

type KeyRefresher interface {
	Refresh(ctx context.Context) error
}

type createKeyRequest struct {
	Project string `json:"project"`
	Name    string `json:"name"`
}

type createKeyResponse struct {
	KeyID   string `json:"key_id"`
	Key     string `json:"key"`
	Project string `json:"project"`
}

type listedKey struct {
	KeyID     string    `json:"key_id"`
	Project   string    `json:"project"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Revoked   bool      `json:"revoked"`
}

func writeError(responseWriter http.ResponseWriter, status int, message string) {
	writeJSON(responseWriter, status, map[string]string{"error": message})
}

func refreshAfterWrite(logger *slog.Logger, refresher KeyRefresher, request *http.Request) {
	if err := refresher.Refresh(request.Context()); err != nil {
		logger.Warn("refreshing api keys after write failed", "error", err)
	}
}

func createKeyHandler(logger *slog.Logger, store KeyStore, refresher KeyRefresher) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		var body createKeyRequest
		limitedBody := http.MaxBytesReader(responseWriter, request.Body, maxCreateKeyBodyBytes)
		if err := json.NewDecoder(limitedBody).Decode(&body); err != nil {
			writeError(responseWriter, http.StatusBadRequest, "invalid json body")
			return
		}
		if !projectPattern.MatchString(body.Project) {
			writeError(responseWriter, http.StatusBadRequest, "project must match "+projectPattern.String())
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			writeError(responseWriter, http.StatusBadRequest, "name is required")
			return
		}
		if len([]rune(name)) > maxKeyNameLength {
			writeError(responseWriter, http.StatusBadRequest, "name must be at most 128 characters")
			return
		}

		keyID, fullKey, err := store.Create(request.Context(), body.Project, name)
		if err != nil {
			logger.Error("creating api key failed", "error", err)
			writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
			return
		}
		refreshAfterWrite(logger, refresher, request)
		writeJSON(responseWriter, http.StatusCreated, createKeyResponse{KeyID: keyID, Key: fullKey, Project: body.Project})
	}
}

func listKeysHandler(logger *slog.Logger, store KeyStore) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		records, err := store.List(request.Context())
		if err != nil {
			logger.Error("listing api keys failed", "error", err)
			writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
			return
		}
		keys := make([]listedKey, 0, len(records))
		for _, record := range records {
			keys = append(keys, listedKey{
				KeyID:     record.KeyID,
				Project:   record.Project,
				Name:      record.Name,
				CreatedAt: record.CreatedAt,
				Revoked:   record.Revoked,
			})
		}
		writeJSON(responseWriter, http.StatusOK, keys)
	}
}

func revokeKeyHandler(logger *slog.Logger, store KeyStore, refresher KeyRefresher) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		err := store.Revoke(request.Context(), request.PathValue("key_id"))
		if errors.Is(err, apikeys.ErrKeyNotFound) {
			writeError(responseWriter, http.StatusNotFound, "api key not found")
			return
		}
		if err != nil {
			logger.Error("revoking api key failed", "error", err)
			writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
			return
		}
		refreshAfterWrite(logger, refresher, request)
		responseWriter.WriteHeader(http.StatusNoContent)
	}
}
