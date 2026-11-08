package httpapi

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dilutedev/scrapelog/ingest-api/internal/apikeys"
)

func requestWithAuthorization(authorization string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	return request
}

func TestRequireAdminAcceptsCorrectToken(t *testing.T) {
	nextCalled := false
	handler := requireAdmin(sha256.Sum256([]byte("admin-token")), func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	})

	handler(httptest.NewRecorder(), requestWithAuthorization("Bearer admin-token"))

	if !nextCalled {
		t.Fatal("next handler was not called")
	}
}

func TestRequireAdminRejectsInvalidCredentials(t *testing.T) {
	for name, authorization := range map[string]string{
		"wrong token":  "Bearer other-token",
		"missing":      "",
		"basic scheme": "Basic admin-token",
		"empty bearer": "Bearer ",
	} {
		nextCalled := false
		handler := requireAdmin(sha256.Sum256([]byte("admin-token")), func(http.ResponseWriter, *http.Request) {
			nextCalled = true
		})
		recorder := httptest.NewRecorder()

		handler(recorder, requestWithAuthorization(authorization))

		if recorder.Code != http.StatusUnauthorized || nextCalled {
			t.Errorf("%s: status %d, next called %v", name, recorder.Code, nextCalled)
		}
		if recorder.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: missing WWW-Authenticate header", name)
		}
	}
}

type fakeAuthenticator struct {
	validToken string
	identity   apikeys.Identity
}

func (authenticator fakeAuthenticator) Authenticate(fullKey string) (apikeys.Identity, bool) {
	if fullKey == authenticator.validToken {
		return authenticator.identity, true
	}
	return apikeys.Identity{}, false
}

type failingBody struct {
	t *testing.T
}

func (body failingBody) Read([]byte) (int, error) {
	body.t.Fatal("request body was read before authentication")
	return 0, nil
}

func TestRequireAPIKeyPassesIdentityToNext(t *testing.T) {
	expectedIdentity := apikeys.Identity{KeyID: "sl_abc", Project: "acme"}
	var receivedIdentity apikeys.Identity
	handler := requireAPIKey(fakeAuthenticator{validToken: "good", identity: expectedIdentity}, func(_ http.ResponseWriter, _ *http.Request, identity apikeys.Identity) {
		receivedIdentity = identity
	})

	handler(httptest.NewRecorder(), requestWithAuthorization("Bearer good"))

	if receivedIdentity != expectedIdentity {
		t.Fatalf("got %+v, want %+v", receivedIdentity, expectedIdentity)
	}
}

func TestRequireAPIKeyRejectsInvalidTokenWithoutReadingBody(t *testing.T) {
	for name, authorization := range map[string]string{
		"wrong token":  "Bearer bad",
		"missing":      "",
		"empty bearer": "Bearer ",
	} {
		nextCalled := false
		handler := requireAPIKey(fakeAuthenticator{validToken: "good"}, func(http.ResponseWriter, *http.Request, apikeys.Identity) {
			nextCalled = true
		})
		request := httptest.NewRequest(http.MethodPost, "/", failingBody{t: t})
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		recorder := httptest.NewRecorder()

		handler(recorder, request)

		if recorder.Code != http.StatusUnauthorized || nextCalled {
			t.Errorf("%s: status %d, next called %v", name, recorder.Code, nextCalled)
		}
	}
}
