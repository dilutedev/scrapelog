package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dilutedev/scrapelog/ingest-api/internal/apikeys"
)

const bearerPrefix = "Bearer "

type KeyAuthenticator interface {
	Authenticate(fullKey string) (apikeys.Identity, bool)
}

func bearerToken(request *http.Request) (string, bool) {
	header := request.Header.Get("Authorization")
	if !strings.HasPrefix(header, bearerPrefix) {
		return "", false
	}
	token := strings.TrimPrefix(header, bearerPrefix)
	return token, token != ""
}

func requireAdmin(adminTokenHash [32]byte, next http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		token, ok := bearerToken(request)
		if !ok {
			writeUnauthorized(responseWriter)
			return
		}
		presentedHash := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(presentedHash[:], adminTokenHash[:]) != 1 {
			writeUnauthorized(responseWriter)
			return
		}
		next(responseWriter, request)
	}
}

func requireAPIKey(authenticator KeyAuthenticator, next func(http.ResponseWriter, *http.Request, apikeys.Identity)) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		token, ok := bearerToken(request)
		if !ok {
			writeUnauthorized(responseWriter)
			return
		}
		identity, authenticated := authenticator.Authenticate(token)
		if !authenticated {
			writeUnauthorized(responseWriter)
			return
		}
		next(responseWriter, request, identity)
	}
}

func writeUnauthorized(responseWriter http.ResponseWriter) {
	responseWriter.Header().Set("WWW-Authenticate", "Bearer")
	writeJSON(responseWriter, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
}

func writeJSON(responseWriter http.ResponseWriter, status int, value any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(status)
	json.NewEncoder(responseWriter).Encode(value)
}
