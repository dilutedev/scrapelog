package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/apikeys"
	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
)

type fakeKeyStore struct {
	createdProject string
	createdName    string
	createCalls    int
	records        []apikeys.Record
	err            error
}

func (store *fakeKeyStore) Create(ctx context.Context, project string, name string) (string, string, error) {
	store.createCalls++
	store.createdProject = project
	store.createdName = name
	if store.err != nil {
		return "", "", store.err
	}
	return "sl_testkeyid123", "sl_testkeyid123_secret", nil
}

func (store *fakeKeyStore) List(ctx context.Context) ([]apikeys.Record, error) {
	return store.records, store.err
}

func (store *fakeKeyStore) Revoke(ctx context.Context, keyID string) error {
	return store.err
}

type fakeRefresher struct {
	calls int
}

func (refresher *fakeRefresher) Refresh(ctx context.Context) error {
	refresher.calls++
	return nil
}

func (refresher *fakeRefresher) Authenticate(fullKey string) (apikeys.Identity, bool) {
	if fullKey == "good-key" {
		return apikeys.Identity{KeyID: "goodkeyid", Project: "acme"}, true
	}
	return apikeys.Identity{}, false
}

func serveAdmin(store *fakeKeyStore, refresher *fakeRefresher, method string, path string, body string, authorization string) *httptest.ResponseRecorder {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	NewHandler(logger, &fakePinger{}, store, refresher, &fakeRecordWriter{}, config.Config{AdminToken: "admin-token"}).ServeHTTP(recorder, request)
	return recorder
}

func serveAdminAuthorized(store *fakeKeyStore, refresher *fakeRefresher, method string, path string, body string) *httptest.ResponseRecorder {
	return serveAdmin(store, refresher, method, path, body, "Bearer admin-token")
}

func TestCreateKeyReturnsKeyOnce(t *testing.T) {
	store := &fakeKeyStore{}
	refresher := &fakeRefresher{}

	recorder := serveAdminAuthorized(store, refresher, http.MethodPost, "/v1/admin/keys", `{"project":"acme-scraper","name":"  prod-box-1 "}`)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201", recorder.Code)
	}
	expectedBody := `{"key_id":"sl_testkeyid123","key":"sl_testkeyid123_secret","project":"acme-scraper"}` + "\n"
	if recorder.Body.String() != expectedBody {
		t.Fatalf("unexpected body %q", recorder.Body.String())
	}
	if store.createdProject != "acme-scraper" || store.createdName != "prod-box-1" {
		t.Fatalf("store received %q, %q", store.createdProject, store.createdName)
	}
	if refresher.calls != 1 {
		t.Fatalf("refresher called %d times, want 1", refresher.calls)
	}
}

func TestCreateKeyRejectsInvalidInput(t *testing.T) {
	longName := strings.Repeat("n", 129)
	for name, body := range map[string]string{
		"uppercase project": `{"project":"Acme","name":"x"}`,
		"empty project":     `{"project":"","name":"x"}`,
		"leading dash":      `{"project":"-x","name":"x"}`,
		"long project":      `{"project":"` + strings.Repeat("a", 65) + `","name":"x"}`,
		"missing name":      `{"project":"acme"}`,
		"blank name":        `{"project":"acme","name":"   "}`,
		"long name":         `{"project":"acme","name":"` + longName + `"}`,
		"invalid json":      `{not json`,
	} {
		store := &fakeKeyStore{}

		recorder := serveAdminAuthorized(store, &fakeRefresher{}, http.MethodPost, "/v1/admin/keys", body)

		if recorder.Code != http.StatusBadRequest || store.createCalls != 0 {
			t.Errorf("%s: status %d, create calls %d", name, recorder.Code, store.createCalls)
		}
	}
}

func TestCreateKeyReturnsUnavailableOnStoreError(t *testing.T) {
	recorder := serveAdminAuthorized(&fakeKeyStore{err: errors.New("down")}, &fakeRefresher{}, http.MethodPost, "/v1/admin/keys", `{"project":"acme","name":"x"}`)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", recorder.Code)
	}
}

func TestListKeysOmitsSecrets(t *testing.T) {
	store := &fakeKeyStore{records: []apikeys.Record{{
		KeyID: "sl_abc", Hash: "deadbeef", Project: "acme", Name: "box", CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 6000000, time.UTC),
	}}}

	recorder := serveAdminAuthorized(store, &fakeRefresher{}, http.MethodGet, "/v1/admin/keys", "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "hash") || strings.Contains(body, "deadbeef") || strings.Contains(body, `"key"`) {
		t.Fatalf("response leaks key material: %s", body)
	}
	if !strings.Contains(body, `"created_at":"2026-01-02T03:04:05.006Z"`) || !strings.Contains(body, `"revoked":false`) {
		t.Fatalf("unexpected body %s", body)
	}
}

func TestListKeysReturnsEmptyArray(t *testing.T) {
	recorder := serveAdminAuthorized(&fakeKeyStore{}, &fakeRefresher{}, http.MethodGet, "/v1/admin/keys", "")

	if strings.TrimSpace(recorder.Body.String()) != "[]" {
		t.Fatalf("unexpected body %q", recorder.Body.String())
	}
}

func TestRevokeKeyOutcomes(t *testing.T) {
	cases := map[string]struct {
		err            error
		expectedStatus int
		expectedCalls  int
	}{
		"revoked":   {nil, http.StatusNoContent, 1},
		"not found": {apikeys.ErrKeyNotFound, http.StatusNotFound, 0},
		"store err": {errors.New("down"), http.StatusServiceUnavailable, 0},
	}
	for name, testCase := range cases {
		refresher := &fakeRefresher{}

		recorder := serveAdminAuthorized(&fakeKeyStore{err: testCase.err}, refresher, http.MethodDelete, "/v1/admin/keys/sl_abc", "")

		if recorder.Code != testCase.expectedStatus || refresher.calls != testCase.expectedCalls {
			t.Errorf("%s: status %d, refresher calls %d", name, recorder.Code, refresher.calls)
		}
	}
}

func TestAdminRoutesRequireAdminToken(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/v1/admin/keys"},
		{http.MethodGet, "/v1/admin/keys"},
		{http.MethodDelete, "/v1/admin/keys/sl_abc"},
	} {
		recorder := serveAdmin(&fakeKeyStore{}, &fakeRefresher{}, route.method, route.path, "", "")

		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status %d, want 401", route.method, route.path, recorder.Code)
		}
	}
}
