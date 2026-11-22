package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/users"
)

const maxUserBodyBytes = 16384

type userResponse struct {
	UserID      string   `json:"user_id"`
	Username    string   `json:"username"`
	Permissions []string `json:"permissions"`
	Disabled    bool     `json:"disabled"`
	CreatedAt   string   `json:"created_at"`
}

type createUserRequest struct {
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	Permissions []string `json:"permissions"`
}

type updateUserRequest struct {
	Permissions []string `json:"permissions"`
	Password    string   `json:"password"`
	Disabled    bool     `json:"disabled"`
}

func toUserResponse(record users.Record) userResponse {
	permissions := record.Permissions
	if permissions == nil {
		permissions = []string{}
	}
	return userResponse{
		UserID:      record.UserID,
		Username:    record.Username,
		Permissions: permissions,
		Disabled:    record.Disabled,
		CreatedAt:   record.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func decodeUserBody(responseWriter http.ResponseWriter, request *http.Request, destination any) bool {
	limitedBody := http.MaxBytesReader(responseWriter, request.Body, maxUserBodyBytes)
	if err := json.NewDecoder(limitedBody).Decode(destination); err != nil {
		writeError(responseWriter, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

func writeUserError(responseWriter http.ResponseWriter, logger *slog.Logger, err error) {
	switch {
	case errors.Is(err, users.ErrUsernameRequired), errors.Is(err, users.ErrUsernameReserved), errors.Is(err, users.ErrPasswordTooShort), errors.Is(err, users.ErrUnknownPermission):
		writeError(responseWriter, http.StatusBadRequest, err.Error())
	case errors.Is(err, users.ErrUsernameTaken):
		writeError(responseWriter, http.StatusConflict, err.Error())
	case errors.Is(err, users.ErrUserNotFound):
		writeError(responseWriter, http.StatusNotFound, err.Error())
	case errors.Is(err, users.ErrUnavailable):
		writeError(responseWriter, http.StatusServiceUnavailable, usersUnavailableMessage)
	default:
		logger.Error("save user failed", "error", err)
		writeError(responseWriter, http.StatusInternalServerError, "could not save the user")
	}
}

func listUsersHandler(logger *slog.Logger, userDirectory UserDirectory) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		records, err := userDirectory.List()
		if err != nil {
			writeUserError(responseWriter, logger, err)
			return
		}
		listed := make([]userResponse, 0, len(records))
		for _, record := range records {
			listed = append(listed, toUserResponse(record))
		}
		writeJSON(responseWriter, http.StatusOK, map[string][]userResponse{"users": listed})
	}
}

func createUserHandler(logger *slog.Logger, userDirectory UserDirectory) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		var body createUserRequest
		if !decodeUserBody(responseWriter, request, &body) {
			return
		}
		record, err := userDirectory.Create(request.Context(), body.Username, body.Password, body.Permissions)
		if err != nil {
			writeUserError(responseWriter, logger, err)
			return
		}
		writeJSON(responseWriter, http.StatusCreated, toUserResponse(record))
	}
}

func updateUserHandler(logger *slog.Logger, userDirectory UserDirectory) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		var body updateUserRequest
		if !decodeUserBody(responseWriter, request, &body) {
			return
		}
		record, err := userDirectory.Update(request.Context(), request.PathValue("user_id"), users.Change{Permissions: body.Permissions, Password: body.Password, Disabled: body.Disabled})
		if err != nil {
			writeUserError(responseWriter, logger, err)
			return
		}
		writeJSON(responseWriter, http.StatusOK, toUserResponse(record))
	}
}
