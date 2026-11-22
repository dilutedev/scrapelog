package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dilutedev/scrapelog/ingest-api/internal/session"
	"github.com/dilutedev/scrapelog/ingest-api/internal/users"
)

type memoryRowStore struct {
	rows []users.Record
}

func (store *memoryRowStore) List(ctx context.Context) ([]users.Record, error) {
	return store.rows, nil
}

func (store *memoryRowStore) Insert(ctx context.Context, record users.Record) error {
	store.rows = append(store.rows, record)
	return nil
}

func newRealDirectory(t *testing.T) *users.Directory {
	t.Helper()
	directory := users.NewDirectory(&memoryRowStore{})
	if err := directory.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return directory
}

func adminJSONRequest(method string, target string, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: adminSession()})
	return request
}

func decodeUser(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return body
}

func createUserVia(t *testing.T, handler http.Handler, body string) map[string]any {
	t.Helper()
	recorder := serve(handler, adminJSONRequest(http.MethodPost, "/ui/api/users", body))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", recorder.Code, recorder.Body.String())
	}
	return decodeUser(t, recorder)
}

func TestListUsersOmitsSecretsAndEmptyPermissionsAreArrays(t *testing.T) {
	handler := newUIHandlerWithUsers(newRealDirectory(t))
	createUserVia(t, handler, `{"username":"alice","password":"alice password 1"}`)
	recorder := serve(handler, adminJSONRequest(http.MethodGet, "/ui/api/users", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d", recorder.Code)
	}
	raw := recorder.Body.String()
	for _, forbidden := range []string{"password_hash", "session_version", "PasswordHash"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("response contains %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(raw, `"permissions":[]`) {
		t.Errorf("permissions not an empty array: %s", raw)
	}
}

func TestListUsersEmptyIsArray(t *testing.T) {
	recorder := serve(newUIHandlerWithUsers(newRealDirectory(t)), adminJSONRequest(http.MethodGet, "/ui/api/users", ""))
	if !strings.Contains(recorder.Body.String(), `"users":[]`) {
		t.Fatalf("body %s", recorder.Body.String())
	}
}

func TestCreateUserCanSignIn(t *testing.T) {
	directory := newRealDirectory(t)
	handler := newUIHandlerWithUsers(directory)
	created := createUserVia(t, handler, `{"username":" Alice ","password":"alice password 1","permissions":["view_requests"]}`)
	if created["username"] != "alice" {
		t.Errorf("username = %v", created["username"])
	}
	if _, present := created["password_hash"]; present {
		t.Error("response contains password_hash")
	}
	if _, err := directory.VerifyLogin("alice", "alice password 1"); err != nil {
		t.Fatalf("VerifyLogin: %v", err)
	}
}

func TestCreateUserRejections(t *testing.T) {
	tests := map[string]struct {
		body       string
		wantStatus int
		wantError  string
	}{
		"empty username":     {`{"username":"  ","password":"alice password 1"}`, http.StatusBadRequest, "username is required"},
		"reserved":           {`{"username":"ADMIN","password":"alice password 1"}`, http.StatusBadRequest, "username is reserved"},
		"short password":     {`{"username":"alice","password":"12345678901"}`, http.StatusBadRequest, "password must be at least 12 characters"},
		"unknown permission": {`{"username":"alice","password":"alice password 1","permissions":["root"]}`, http.StatusBadRequest, "unknown permission"},
		"invalid json":       {`{`, http.StatusBadRequest, "invalid json body"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			directory := newRealDirectory(t)
			recorder := serve(newUIHandlerWithUsers(directory), adminJSONRequest(http.MethodPost, "/ui/api/users", test.body))
			if recorder.Code != test.wantStatus || errorMessage(t, recorder) != test.wantError {
				t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
			}
			if records, _ := directory.List(); len(records) != 0 {
				t.Fatal("a rejected create wrote a user")
			}
		})
	}
}

func TestCreateUserDuplicateIgnoresCase(t *testing.T) {
	handler := newUIHandlerWithUsers(newRealDirectory(t))
	createUserVia(t, handler, `{"username":"alice","password":"alice password 1"}`)
	recorder := serve(handler, adminJSONRequest(http.MethodPost, "/ui/api/users", `{"username":"Alice","password":"alice password 1"}`))
	if recorder.Code != http.StatusConflict || errorMessage(t, recorder) != "username is already taken" {
		t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
	}
}

func TestUpdateUser(t *testing.T) {
	directory := newRealDirectory(t)
	handler := newUIHandlerWithUsers(directory)
	created := createUserVia(t, handler, `{"username":"alice","password":"alice password 1","permissions":["view_requests"]}`)
	target := "/ui/api/users/" + created["user_id"].(string)
	sessionVersion := func() uint32 {
		record, err := directory.Lookup(created["user_id"].(string))
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		return record.SessionVersion
	}
	baseline := sessionVersion()

	recorder := serve(handler, adminJSONRequest(http.MethodPut, target, `{"permissions":["view_bandwidth","view_alerts"],"password":"","disabled":false}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("permissions update status %d: %s", recorder.Code, recorder.Body.String())
	}
	if permissions := decodeUser(t, recorder)["permissions"].([]any); len(permissions) != 2 {
		t.Errorf("permissions = %v", permissions)
	}
	if sessionVersion() != baseline {
		t.Error("blank password and unchanged disabled flag bumped the session version")
	}
	if _, err := directory.VerifyLogin("alice", "alice password 1"); err != nil {
		t.Errorf("blank password changed the password: %v", err)
	}

	serve(handler, adminJSONRequest(http.MethodPut, target, `{"permissions":[],"password":"new password 123","disabled":false}`))
	afterReset := sessionVersion()
	if afterReset == baseline {
		t.Error("password reset did not bump the session version")
	}
	if _, err := directory.VerifyLogin("alice", "new password 123"); err != nil {
		t.Errorf("new password rejected: %v", err)
	}

	recorder = serve(handler, adminJSONRequest(http.MethodPut, target, `{"permissions":[],"password":"","disabled":true}`))
	if decodeUser(t, recorder)["disabled"] != true {
		t.Error("user not disabled")
	}
	if sessionVersion() == afterReset {
		t.Error("disabling did not bump the session version")
	}
	if _, err := directory.VerifyLogin("alice", "new password 123"); err == nil {
		t.Error("disabled user can sign in")
	}
}

func TestUpdateUserRejections(t *testing.T) {
	handler := newUIHandlerWithUsers(newRealDirectory(t))
	created := createUserVia(t, handler, `{"username":"alice","password":"alice password 1"}`)
	tests := map[string]struct {
		target     string
		body       string
		wantStatus int
	}{
		"unknown id":         {"/ui/api/users/missing", `{"permissions":[]}`, http.StatusNotFound},
		"unknown permission": {"/ui/api/users/" + created["user_id"].(string), `{"permissions":["root"]}`, http.StatusBadRequest},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := serve(handler, adminJSONRequest(http.MethodPut, test.target, test.body))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestUserRoutesRefuseNonAdminAndBadRequests(t *testing.T) {
	directory := newRealDirectory(t)
	created, err := directory.Create(context.Background(), "everything", "everything pass 1", users.AllPermissions)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	handler := newUIHandlerWithUsers(directory)
	userCookie := &http.Cookie{Name: session.CookieName, Value: userSession(created.UserID, created.SessionVersion)}

	for _, target := range []struct{ method, path string }{
		{http.MethodGet, "/ui/api/users"},
		{http.MethodPost, "/ui/api/users"},
		{http.MethodPut, "/ui/api/users/" + created.UserID},
	} {
		request := httptest.NewRequest(target.method, target.path, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(userCookie)
		if recorder := serve(handler, request); recorder.Code != http.StatusForbidden {
			t.Errorf("%s %s: status %d, want 403", target.method, target.path, recorder.Code)
		}
	}

	wrongType := adminJSONRequest(http.MethodPost, "/ui/api/users", `{}`)
	wrongType.Header.Set("Content-Type", "text/plain")
	if recorder := serve(handler, wrongType); recorder.Code != http.StatusUnsupportedMediaType {
		t.Errorf("wrong content type: status %d", recorder.Code)
	}
	crossOrigin := adminJSONRequest(http.MethodPost, "/ui/api/users", `{"username":"x","password":"long enough pass"}`)
	crossOrigin.Header.Set("Origin", "https://evil.example")
	if recorder := serve(handler, crossOrigin); recorder.Code != http.StatusForbidden {
		t.Errorf("cross origin: status %d", recorder.Code)
	}
}

func TestUserRoutesReportUnavailableDirectory(t *testing.T) {
	handler := newUIHandlerWithUsers(users.NewDirectory(&memoryRowStore{}))
	for _, target := range []struct{ method, path, body string }{
		{http.MethodGet, "/ui/api/users", ""},
		{http.MethodPost, "/ui/api/users", `{"username":"alice","password":"alice password 1"}`},
		{http.MethodPut, "/ui/api/users/x", `{"permissions":[]}`},
	} {
		if recorder := serve(handler, adminJSONRequest(target.method, target.path, target.body)); recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status %d, want 503", target.method, target.path, recorder.Code)
		}
	}
}
