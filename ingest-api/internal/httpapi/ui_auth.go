package httpapi

import (
	"encoding/json"
	"errors"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/dilutedev/scrapelog/ingest-api/internal/ratelimit"
	"github.com/dilutedev/scrapelog/ingest-api/internal/session"
	"github.com/dilutedev/scrapelog/ingest-api/internal/ui"
	"github.com/dilutedev/scrapelog/ingest-api/internal/users"
)

const (
	contentSecurityPolicy   = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	wrongCredentialsMessage = "wrong username or password"
	usersUnavailableMessage = "user accounts unavailable: apply clickhouse/initdb/005_users.sql and the scrapelog.users grant, then restart ingest-api"
	maxLoginBodyBytes       = 4096
)

var errNoSession = errors.New("no valid session")

type UserDirectory interface {
	VerifyLogin(username string, password string) (users.Record, error)
	Lookup(userID string) (users.Record, error)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type principal struct {
	username    string
	isAdmin     bool
	permissions []string
}

func (sessionPrincipal principal) allows(anyOf []string) bool {
	if sessionPrincipal.isAdmin {
		return true
	}
	for _, permission := range anyOf {
		if slices.Contains(sessionPrincipal.permissions, permission) {
			return true
		}
	}
	return false
}

func (sessionPrincipal principal) viewer() ui.Viewer {
	permissions := sessionPrincipal.permissions
	if sessionPrincipal.isAdmin {
		permissions = users.AllPermissions
	}
	return ui.Viewer{Username: sessionPrincipal.username, IsAdmin: sessionPrincipal.isAdmin, Permissions: permissions}
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

func resolvePrincipal(signer *session.Signer, userDirectory UserDirectory, request *http.Request) (principal, error) {
	cookie, err := request.Cookie(session.CookieName)
	if err != nil {
		return principal{}, errNoSession
	}
	claims, valid := signer.Verify(cookie.Value)
	if !valid {
		return principal{}, errNoSession
	}
	if claims.Subject == users.AdminUsername {
		return principal{username: users.AdminUsername, isAdmin: true}, nil
	}
	record, err := userDirectory.Lookup(claims.Subject)
	if errors.Is(err, users.ErrUnavailable) {
		return principal{}, err
	}
	if err != nil || record.Disabled || record.SessionVersion != claims.SessionVersion {
		return principal{}, errNoSession
	}
	return principal{username: record.Username, permissions: record.Permissions}, nil
}

func requireSession(signer *session.Signer, userDirectory UserDirectory, next http.HandlerFunc) http.HandlerFunc {
	return requirePermissionOrAnyPrincipal(signer, userDirectory, nil, true, next)
}

func requirePermission(signer *session.Signer, userDirectory UserDirectory, anyOf []string, next http.HandlerFunc) http.HandlerFunc {
	return requirePermissionOrAnyPrincipal(signer, userDirectory, anyOf, false, next)
}

func requirePermissionOrAnyPrincipal(signer *session.Signer, userDirectory UserDirectory, anyOf []string, anyPrincipal bool, next http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		sessionPrincipal, err := resolvePrincipal(signer, userDirectory, request)
		if errors.Is(err, users.ErrUnavailable) {
			writeError(responseWriter, http.StatusServiceUnavailable, usersUnavailableMessage)
			return
		}
		if err != nil {
			writeError(responseWriter, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !anyPrincipal && !sessionPrincipal.allows(anyOf) {
			writeError(responseWriter, http.StatusForbidden, "you don't have permission for this")
			return
		}
		next(responseWriter, request)
	}
}

func requirePagePermission(signer *session.Signer, userDirectory UserDirectory, anyOf []string, next http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		sessionPrincipal, err := resolvePrincipal(signer, userDirectory, request)
		if errors.Is(err, users.ErrUnavailable) {
			http.Error(responseWriter, usersUnavailableMessage, http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			http.Redirect(responseWriter, request, "/login?next="+url.QueryEscape(request.URL.RequestURI()), http.StatusFound)
			return
		}
		if !sessionPrincipal.allows(anyOf) {
			http.Error(responseWriter, "You don't have permission to open this page.", http.StatusForbidden)
			return
		}
		next(responseWriter, request.WithContext(ui.WithViewer(request.Context(), sessionPrincipal.viewer())))
	}
}

func loginHandler(signer *session.Signer, adminTokenHash [32]byte, loginLimiter *ratelimit.Limiter, userDirectory UserDirectory) http.HandlerFunc {
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
		username := ""
		if err := json.NewDecoder(limitedBody).Decode(&body); err == nil {
			username = users.NormalizeUsername(body.Username)
		}
		if username == "" || body.Password == "" {
			writeError(responseWriter, http.StatusBadRequest, "invalid json body")
			return
		}
		sessionValue := ""
		if username == users.AdminUsername {
			if !adminTokenMatches(adminTokenHash, body.Password) {
				writeError(responseWriter, http.StatusUnauthorized, wrongCredentialsMessage)
				return
			}
			sessionValue = signer.Issue(users.AdminUsername, 0)
		} else {
			record, err := userDirectory.VerifyLogin(username, body.Password)
			if errors.Is(err, users.ErrUnavailable) {
				writeError(responseWriter, http.StatusServiceUnavailable, usersUnavailableMessage)
				return
			}
			if err != nil {
				writeError(responseWriter, http.StatusUnauthorized, wrongCredentialsMessage)
				return
			}
			sessionValue = signer.Issue(record.UserID, record.SessionVersion)
		}
		http.SetCookie(responseWriter, sessionCookie(request, sessionValue, int(session.Lifetime.Seconds())))
		responseWriter.WriteHeader(http.StatusNoContent)
	}
}

func logoutHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		http.SetCookie(responseWriter, sessionCookie(request, "", -1))
		responseWriter.WriteHeader(http.StatusNoContent)
	}
}

func sessionStatusHandler(signer *session.Signer, userDirectory UserDirectory) http.HandlerFunc {
	return requireSession(signer, userDirectory, func(responseWriter http.ResponseWriter, request *http.Request) {
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
