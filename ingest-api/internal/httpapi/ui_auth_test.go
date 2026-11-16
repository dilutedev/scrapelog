package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/session"
)

const testAdminToken = "admin-token"

func newUIHandler() http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewHandler(context.Background(), logger, &fakePinger{}, &fakeKeyStore{}, &fakeRefresher{}, &fakeRecordWriter{}, &fakeBodyStore{}, &fakeStorageReporter{}, &fakeDiskGuard{}, &fakeRequestReader{}, nil, &fakeAlertStore{}, &fakeRefresher{}, nil, emptyAlertMatcher(), &fakeAlertQueue{}, false, nil, nil, nil, config.Config{AdminToken: testAdminToken})
}

func newTestSigner() *session.Signer {
	return session.New(testAdminToken, time.Now)
}

func loginRequestWith(token string, mutate func(*http.Request)) *http.Request {
	body := `{"token":"` + token + `"}`
	request := httptest.NewRequest(http.MethodPost, "/ui/api/login", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if mutate != nil {
		mutate(request)
	}
	return request
}

func serve(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func findSessionCookie(t *testing.T, recorder *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == session.CookieName {
			return cookie
		}
	}
	return nil
}

func errorMessage(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return body["error"]
}

func TestLoginSetsSessionCookieOnCorrectToken(t *testing.T) {
	tests := map[string]struct {
		forwardedProto string
		wantSecure     bool
	}{
		"plain http":   {forwardedProto: "", wantSecure: false},
		"behind https": {forwardedProto: "https", wantSecure: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := serve(newUIHandler(), loginRequestWith(testAdminToken, func(request *http.Request) {
				if test.forwardedProto != "" {
					request.Header.Set("X-Forwarded-Proto", test.forwardedProto)
				}
			}))
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("status %d, want 204", recorder.Code)
			}
			cookie := findSessionCookie(t, recorder)
			if cookie == nil {
				t.Fatal("no session cookie set")
			}
			if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.MaxAge != 604800 {
				t.Fatalf("unexpected cookie attributes %+v", cookie)
			}
			if cookie.Secure != test.wantSecure {
				t.Fatalf("Secure = %v, want %v", cookie.Secure, test.wantSecure)
			}
			if !newTestSigner().Valid(cookie.Value) {
				t.Fatal("issued cookie value is not valid")
			}
		})
	}
}

func TestLoginRejectsWrongToken(t *testing.T) {
	recorder := serve(newUIHandler(), loginRequestWith("wrong", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", recorder.Code)
	}
	if got := errorMessage(t, recorder); got != wrongTokenMessage {
		t.Fatalf("error %q, want %q", got, wrongTokenMessage)
	}
	if findSessionCookie(t, recorder) != nil {
		t.Fatal("cookie set on wrong token")
	}
}

func TestLoginRejectsMalformedBodies(t *testing.T) {
	tests := map[string]string{
		"not json":    "nope",
		"empty token": `{"token":""}`,
		"no token":    `{}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/ui/api/login", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			recorder := serve(newUIHandler(), request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", recorder.Code)
			}
		})
	}
}

func TestLoginRateLimitsPerClientIP(t *testing.T) {
	handler := newUIHandler()
	withRealIP := func(address string) func(*http.Request) {
		return func(request *http.Request) { request.Header.Set("X-Real-IP", address) }
	}
	for attempt := 1; attempt <= 10; attempt++ {
		recorder := serve(handler, loginRequestWith("wrong", withRealIP("203.0.113.1")))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d, want 401", attempt, recorder.Code)
		}
	}
	limited := serve(handler, loginRequestWith("wrong", withRealIP("203.0.113.1")))
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", limited.Code)
	}
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}
	other := serve(handler, loginRequestWith("wrong", withRealIP("203.0.113.2")))
	if other.Code != http.StatusUnauthorized {
		t.Fatalf("other client status %d, want 401", other.Code)
	}
}

func TestLoginRateLimitFallsBackToRemoteAddrHost(t *testing.T) {
	handler := newUIHandler()
	withRemoteAddr := func(address string) func(*http.Request) {
		return func(request *http.Request) { request.RemoteAddr = address }
	}
	for attempt := 1; attempt <= 10; attempt++ {
		address := "198.51.100.7:" + strconv.Itoa(40000+attempt)
		serve(handler, loginRequestWith("wrong", withRemoteAddr(address)))
	}
	recorder := serve(handler, loginRequestWith("wrong", withRemoteAddr("198.51.100.7:55555")))
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", recorder.Code)
	}
}

func TestSameOriginJSONChecks(t *testing.T) {
	tests := map[string]struct {
		contentType string
		origin      string
		wantStatus  int
	}{
		"text plain":           {contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType},
		"missing content type": {contentType: "", wantStatus: http.StatusUnsupportedMediaType},
		"foreign origin":       {contentType: "application/json", origin: "https://evil.example", wantStatus: http.StatusForbidden},
		"null origin":          {contentType: "application/json", origin: "null", wantStatus: http.StatusForbidden},
		"matching origin":      {contentType: "application/json", origin: "http://example.com", wantStatus: http.StatusNoContent},
		"no origin":            {contentType: "application/json", wantStatus: http.StatusNoContent},
		"json with charset":    {contentType: "application/json; charset=utf-8", wantStatus: http.StatusNoContent},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			guarded := requireSameOriginJSON(func(responseWriter http.ResponseWriter, request *http.Request) {
				responseWriter.WriteHeader(http.StatusNoContent)
			})
			request := httptest.NewRequest(http.MethodPost, "http://example.com/ui/api/x", nil)
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			recorder := serve(guarded, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status %d, want %d", recorder.Code, test.wantStatus)
			}
		})
	}
}

func TestLogoutExpiresSessionCookie(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/ui/api/logout", nil)
	request.Header.Set("Content-Type", "application/json")
	recorder := serve(newUIHandler(), request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", recorder.Code)
	}
	cookie := findSessionCookie(t, recorder)
	if cookie == nil {
		t.Fatal("no session cookie set")
	}
	if cookie.MaxAge >= 0 || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatalf("unexpected cookie attributes %+v", cookie)
	}
}

func cookieRequest(method, target, cookieValue string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	if cookieValue != "" {
		request.AddCookie(&http.Cookie{Name: session.CookieName, Value: cookieValue})
	}
	return request
}

func TestSessionStatusReflectsCookieValidity(t *testing.T) {
	expiredSigner := session.New(testAdminToken, func() time.Time { return time.Now().Add(-2 * session.Lifetime) })
	validValue := newTestSigner().Issue()
	tests := map[string]struct {
		cookieValue string
		wantStatus  int
	}{
		"valid":    {cookieValue: validValue, wantStatus: http.StatusNoContent},
		"missing":  {cookieValue: "", wantStatus: http.StatusUnauthorized},
		"expired":  {cookieValue: expiredSigner.Issue(), wantStatus: http.StatusUnauthorized},
		"tampered": {cookieValue: validValue + "x", wantStatus: http.StatusUnauthorized},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := serve(newUIHandler(), cookieRequest(http.MethodGet, "/ui/api/session", test.cookieValue))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status %d, want %d", recorder.Code, test.wantStatus)
			}
		})
	}
}

func TestRequireSessionGuardsNextHandler(t *testing.T) {
	signer := newTestSigner()
	tests := map[string]struct {
		cookieValue string
		wantCalled  bool
		wantStatus  int
	}{
		"without cookie": {cookieValue: "", wantCalled: false, wantStatus: http.StatusUnauthorized},
		"with cookie":    {cookieValue: signer.Issue(), wantCalled: true, wantStatus: http.StatusOK},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			called := false
			guarded := requireSession(signer, func(responseWriter http.ResponseWriter, request *http.Request) { called = true })
			recorder := serve(guarded, cookieRequest(http.MethodGet, "/ui/api/x", test.cookieValue))
			if called != test.wantCalled || recorder.Code != test.wantStatus {
				t.Fatalf("called=%v status=%d, want called=%v status=%d", called, recorder.Code, test.wantCalled, test.wantStatus)
			}
			if recorder.Header().Get("WWW-Authenticate") != "" {
				t.Fatal("unexpected WWW-Authenticate header")
			}
		})
	}
}

func TestRequireSessionPageRedirectsToLoginWithEscapedNext(t *testing.T) {
	signer := newTestSigner()
	called := false
	guarded := requireSessionPage(signer, func(responseWriter http.ResponseWriter, request *http.Request) { called = true })

	recorder := serve(guarded, cookieRequest(http.MethodGet, "/requests?q=status:500%20&x", ""))
	wantLocation := "/login?next=" + url.QueryEscape("/requests?q=status:500%20&x")
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != wantLocation {
		t.Fatalf("status %d location %q, want 302 %q", recorder.Code, recorder.Header().Get("Location"), wantLocation)
	}
	if called {
		t.Fatal("next called without a session")
	}

	serve(guarded, cookieRequest(http.MethodGet, "/requests", signer.Issue()))
	if !called {
		t.Fatal("next not called with a valid session")
	}
}

func TestUIAPIResponsesCarrySecurityHeaders(t *testing.T) {
	tests := map[string]*http.Request{
		"login success":     loginRequestWith(testAdminToken, nil),
		"login unsupported": httptest.NewRequest(http.MethodPost, "/ui/api/login", strings.NewReader("{}")),
		"logout": func() *http.Request {
			logoutRequest := httptest.NewRequest(http.MethodPost, "/ui/api/logout", nil)
			logoutRequest.Header.Set("Content-Type", "application/json")
			return logoutRequest
		}(),
		"session unauthorized": httptest.NewRequest(http.MethodGet, "/ui/api/session", nil),
	}
	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := serve(newUIHandler(), request)
			headers := recorder.Header()
			if headers.Get("Content-Security-Policy") != contentSecurityPolicy ||
				headers.Get("X-Content-Type-Options") != "nosniff" ||
				headers.Get("Referrer-Policy") != "same-origin" {
				t.Fatalf("missing security headers, got %v", headers)
			}
		})
	}
}

func TestAdminTokenMatchesComparesAgainstHash(t *testing.T) {
	hash := sha256.Sum256([]byte(testAdminToken))
	if !adminTokenMatches(hash, testAdminToken) || adminTokenMatches(hash, "other") {
		t.Fatal("adminTokenMatches gave the wrong answer")
	}
}
