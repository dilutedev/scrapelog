package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/apikeys"
	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/session"
	"github.com/dilutedev/scrapelog/ingest-api/internal/users"
)

func newKeyRoutesHandler(store *fakeKeyStore, userDirectory UserDirectory) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewHandler(context.Background(), logger, &fakePinger{}, store, &fakeRefresher{}, &fakeRecordWriter{}, &fakeBodyStore{}, &fakeStorageReporter{}, &fakeDiskGuard{}, &fakeRequestReader{}, nil, &fakeAlertStore{}, &fakeRefresher{}, nil, emptyAlertMatcher(), &fakeAlertQueue{}, false, nil, nil, nil, userDirectory, config.Config{AdminToken: testAdminToken})
}

func TestKeyRoutesRefuseNonAdmin(t *testing.T) {
	directory := &fakeUserDirectory{records: map[string]users.Record{
		"user-everything": {UserID: "user-everything", Username: "everything", Permissions: users.AllPermissions, SessionVersion: 1},
	}}
	store := &fakeKeyStore{}
	handler := newKeyRoutesHandler(store, directory)

	for _, target := range []struct{ method, path string }{
		{http.MethodGet, "/ui/api/keys"},
		{http.MethodPost, "/ui/api/keys"},
		{http.MethodDelete, "/ui/api/keys/sl_testkeyid123"},
	} {
		request := httptest.NewRequest(target.method, target.path, strings.NewReader(`{"project":"acme","name":"scraper"}`))
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: session.CookieName, Value: userSession("user-everything", 1)})
		if recorder := serve(handler, request); recorder.Code != http.StatusForbidden {
			t.Errorf("%s %s: status %d, want 403", target.method, target.path, recorder.Code)
		}
	}
	if store.createCalls != 0 || store.revokedKeyID != "" {
		t.Errorf("store touched: creates %d, revoked %q", store.createCalls, store.revokedKeyID)
	}
}

func TestListKeysOmitsHashAndSecret(t *testing.T) {
	store := &fakeKeyStore{records: []apikeys.Record{
		{KeyID: "sl_one", Project: "acme", Name: "scraper", Hash: "hash-value", CreatedAt: time.Unix(1700000000, 0).UTC()},
		{KeyID: "sl_two", Project: "acme", Name: "old", Hash: "other-hash", Revoked: true},
	}}
	handler := newKeyRoutesHandler(store, &fakeUserDirectory{})

	recorder := serve(handler, adminJSONRequest(http.MethodGet, "/ui/api/keys", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var entries []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries %d, want 2", len(entries))
	}
	wantFields := []string{"created_at", "key_id", "name", "project", "revoked"}
	for _, entry := range entries {
		gotFields := []string{}
		for field := range entry {
			gotFields = append(gotFields, field)
		}
		slices.Sort(gotFields)
		if !slices.Equal(gotFields, wantFields) {
			t.Errorf("fields %v, want %v", gotFields, wantFields)
		}
	}
	if strings.Contains(recorder.Body.String(), "hash-value") {
		t.Errorf("response leaks key hash: %s", recorder.Body.String())
	}
}

func TestCreateKeyReturnsKeyOnceWithNoStore(t *testing.T) {
	store := &fakeKeyStore{records: []apikeys.Record{{KeyID: "sl_testkeyid123", Project: "acme", Name: "scraper"}}}
	handler := newKeyRoutesHandler(store, &fakeUserDirectory{})

	created := serve(handler, adminJSONRequest(http.MethodPost, "/ui/api/keys", `{"project":"acme","name":"scraper"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", created.Code, created.Body.String())
	}
	if got := created.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control %q, want no-store", got)
	}
	var body map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["key"] != "sl_testkeyid123_secret" {
		t.Errorf("key %v, want the issued key", body["key"])
	}

	listed := serve(handler, adminJSONRequest(http.MethodGet, "/ui/api/keys", ""))
	if strings.Contains(listed.Body.String(), `"key"`) || strings.Contains(listed.Body.String(), "_secret") {
		t.Errorf("list exposes the key: %s", listed.Body.String())
	}
}

func TestRevokeKeyRequiresJSONBody(t *testing.T) {
	store := &fakeKeyStore{}
	handler := newKeyRoutesHandler(store, &fakeUserDirectory{})

	withoutType := httptest.NewRequest(http.MethodDelete, "/ui/api/keys/sl_testkeyid123", nil)
	withoutType.AddCookie(&http.Cookie{Name: session.CookieName, Value: adminSession()})
	if recorder := serve(handler, withoutType); recorder.Code != http.StatusUnsupportedMediaType {
		t.Errorf("without content type: status %d, want 415", recorder.Code)
	}
	if store.revokedKeyID != "" {
		t.Errorf("revoked %q without a JSON body", store.revokedKeyID)
	}

	if recorder := serve(handler, adminJSONRequest(http.MethodDelete, "/ui/api/keys/sl_testkeyid123", `{}`)); recorder.Code != http.StatusNoContent {
		t.Errorf("with JSON body: status %d, want 204", recorder.Code)
	}
	if store.revokedKeyID != "sl_testkeyid123" {
		t.Errorf("revoked %q, want sl_testkeyid123", store.revokedKeyID)
	}
}

func TestCreateKeyRejectsCrossOrigin(t *testing.T) {
	store := &fakeKeyStore{}
	handler := newKeyRoutesHandler(store, &fakeUserDirectory{})

	request := adminJSONRequest(http.MethodPost, "/ui/api/keys", `{"project":"acme","name":"scraper"}`)
	request.Header.Set("Origin", "https://evil.example")
	if recorder := serve(handler, request); recorder.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403", recorder.Code)
	}
	if store.createCalls != 0 {
		t.Errorf("created %d keys for a cross-origin request", store.createCalls)
	}
}
