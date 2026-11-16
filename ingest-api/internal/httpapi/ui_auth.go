package httpapi

import (
	"encoding/json"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/dilutedev/scrapelog/ingest-api/internal/ratelimit"
	"github.com/dilutedev/scrapelog/ingest-api/internal/session"
)

const (
	contentSecurityPolicy = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	wrongTokenMessage     = "That token doesn't match ADMIN_TOKEN on the server."
	maxLoginBodyBytes     = 4096
)

type loginRequest struct {
	Token string `json:"token"`
}

func withSecurityHeaders(next http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		headers := responseWriter.Header()
		headers.Set("Content-Security-Policy", contentSecurityPolicy)
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("Referrer-Policy", "same-origin")
		next(responseWriter, request)
	}
}

func requireSameOriginJSON(next http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeError(responseWriter, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		if origin := request.Header.Get("Origin"); origin != "" {
			parsedOrigin, err := url.Parse(origin)
			if err != nil || parsedOrigin.Host != request.Host {
				writeError(responseWriter, http.StatusForbidden, "cross-origin request refused")
				return
			}
		}
		next(responseWriter, request)
	}
}

func hasValidSession(signer *session.Signer, request *http.Request) bool {
	cookie, err := request.Cookie(session.CookieName)
	return err == nil && signer.Valid(cookie.Value)
}

func requireSession(signer *session.Signer, next http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if !hasValidSession(signer, request) {
			writeError(responseWriter, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(responseWriter, request)
	}
}

func requireSessionPage(signer *session.Signer, next http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if !hasValidSession(signer, request) {
			http.Redirect(responseWriter, request, "/login?next="+url.QueryEscape(request.URL.RequestURI()), http.StatusFound)
			return
		}
		next(responseWriter, request)
	}
}

func loginHandler(signer *session.Signer, adminTokenHash [32]byte, loginLimiter *ratelimit.Limiter) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		allowed, retryAfter := loginLimiter.Take(clientIP(request), 1)
		if !allowed {
			retryAfterSeconds := max(1, int(math.Ceil(retryAfter.Seconds())))
			responseWriter.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
			writeError(responseWriter, http.StatusTooManyRequests, "too many sign-in attempts, try again shortly")
			return
		}
		var body loginRequest
		limitedBody := http.MaxBytesReader(responseWriter, request.Body, maxLoginBodyBytes)
		if err := json.NewDecoder(limitedBody).Decode(&body); err != nil || body.Token == "" {
			writeError(responseWriter, http.StatusBadRequest, "invalid json body")
			return
		}
		if !adminTokenMatches(adminTokenHash, body.Token) {
			writeError(responseWriter, http.StatusUnauthorized, wrongTokenMessage)
			return
		}
		http.SetCookie(responseWriter, sessionCookie(request, signer.Issue(), int(session.Lifetime.Seconds())))
		responseWriter.WriteHeader(http.StatusNoContent)
	}
}

func logoutHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		http.SetCookie(responseWriter, sessionCookie(request, "", -1))
		responseWriter.WriteHeader(http.StatusNoContent)
	}
}

func sessionStatusHandler(signer *session.Signer) http.HandlerFunc {
	return requireSession(signer, func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusNoContent)
	})
}

func clientIP(request *http.Request) string {
	if realIP := request.Header.Get("X-Real-IP"); realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	return host
}

func sessionCookie(request *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     session.CookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   request.TLS != nil || request.Header.Get("X-Forwarded-Proto") == "https",
	}
}
