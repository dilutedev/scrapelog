package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/dilutedev/scrapelog/ingest-api/internal/apikeys"
	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
)

func newProjectsHandler(store KeyStore) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewHandler(context.Background(), logger, &fakePinger{}, store, &fakeRefresher{}, &fakeRecordWriter{}, &fakeBodyStore{}, &fakeStorageReporter{}, &fakeDiskGuard{}, &fakeRequestReader{}, nil, &fakeAlertStore{}, &fakeRefresher{}, nil, emptyAlertMatcher(), &fakeAlertQueue{}, false, nil, nil, nil, &fakeUserDirectory{}, config.Config{AdminToken: testAdminToken})
}

func TestListProjectsReturnsSortedDistinctNames(t *testing.T) {
	store := &fakeKeyStore{records: []apikeys.Record{
		{KeyID: "key1", Project: "gamma", Revoked: true},
		{KeyID: "key2", Project: "alpha"},
		{KeyID: "key3", Project: "beta"},
		{KeyID: "key4", Project: "alpha"},
	}}
	recorder := serve(newProjectsHandler(store), alertRequest(http.MethodGet, "/ui/api/projects", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var projectNames []string
	if err := json.Unmarshal(recorder.Body.Bytes(), &projectNames); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(projectNames, ",") != "alpha,beta,gamma" {
		t.Fatalf("projects %v, want alpha,beta,gamma", projectNames)
	}
}

func TestListProjectsReturnsOnlyNames(t *testing.T) {
	store := &fakeKeyStore{records: []apikeys.Record{
		{KeyID: "sl_visiblekeyid", Hash: "hashsecretvalue", Project: "alpha", Name: "scraper-one"},
	}}
	recorder := serve(newProjectsHandler(store), alertRequest(http.MethodGet, "/ui/api/projects", ""))
	body := recorder.Body.String()
	for _, forbidden := range []string{"sl_visiblekeyid", "hashsecretvalue", "scraper-one"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaks %q: %s", forbidden, body)
		}
	}
	var projectNames []string
	if err := json.Unmarshal(recorder.Body.Bytes(), &projectNames); err != nil {
		t.Fatalf("response is not a list of names: %v", err)
	}
}

func TestListProjectsReturnsEmptyArray(t *testing.T) {
	recorder := serve(newProjectsHandler(&fakeKeyStore{}), alertRequest(http.MethodGet, "/ui/api/projects", ""))
	if strings.TrimSpace(recorder.Body.String()) != "[]" {
		t.Fatalf("body %q, want []", recorder.Body.String())
	}
}

func TestListProjectsReportsStoreFailure(t *testing.T) {
	recorder := serve(newProjectsHandler(&fakeKeyStore{err: errors.New("down")}), alertRequest(http.MethodGet, "/ui/api/projects", ""))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", recorder.Code)
	}
}

func TestListProjectsRequiresSession(t *testing.T) {
	request := alertRequest(http.MethodGet, "/ui/api/projects", "")
	request.Header.Del("Cookie")
	recorder := serve(newProjectsHandler(&fakeKeyStore{}), request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", recorder.Code)
	}
}
