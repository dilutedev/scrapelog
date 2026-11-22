package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/dilutedev/scrapelog/ingest-api/internal/archive"
	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/session"
	"github.com/dilutedev/scrapelog/ingest-api/internal/users"
)

type fakeRestoreService struct {
	plan      archive.RestorePlan
	restore   archive.Restore
	restores  []archive.Restore
	err       error
	calls     int
	deletedID string
}

func (service *fakeRestoreService) Estimate(ctx context.Context, request archive.RestoreRequest) (archive.RestorePlan, error) {
	service.calls++
	return service.plan, service.err
}

func (service *fakeRestoreService) Start(ctx context.Context, request archive.RestoreRequest) (archive.Restore, error) {
	service.calls++
	return service.restore, service.err
}

func (service *fakeRestoreService) ListRestores(ctx context.Context) ([]archive.Restore, error) {
	service.calls++
	return service.restores, service.err
}

func (service *fakeRestoreService) Delete(ctx context.Context, restoreID string) error {
	service.calls++
	service.deletedID = restoreID
	return service.err
}

func newRestoresHandler(archivingEnabled bool, restoreService RestoreService) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewHandler(context.Background(), logger, &fakePinger{}, &fakeKeyStore{}, &fakeRefresher{}, &fakeRecordWriter{}, &fakeBodyStore{}, &fakeStorageReporter{}, &fakeDiskGuard{}, &fakeRequestReader{}, nil, &fakeAlertStore{}, &fakeRefresher{}, nil, emptyAlertMatcher(), &fakeAlertQueue{}, archivingEnabled, nil, nil, restoreService, &fakeUserDirectory{}, config.Config{AdminToken: testAdminToken})
}

func restoreRequestWithSession(method string, target string, body string, withSession bool) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if withSession {
		request.AddCookie(&http.Cookie{Name: session.CookieName, Value: newTestSigner().Issue(users.AdminUsername, 0)})
	}
	return request
}

const validRestoreBody = `{"from":"2026-09-01T00:00:00Z","to":"2026-09-02T00:00:00Z","project":"shop","domain":""}`
const validEstimateTarget = "/ui/api/restores/estimate?from=2026-09-01T00:00:00Z&to=2026-09-02T00:00:00Z"

func TestRestoreRoutesRequireSession(t *testing.T) {
	restoreID := uuid.NewV7().String()
	tests := map[string]struct {
		method string
		path   string
	}{
		"estimate": {http.MethodGet, validEstimateTarget},
		"list":     {http.MethodGet, "/ui/api/restores"},
		"start":    {http.MethodPost, "/ui/api/restores"},
		"delete":   {http.MethodDelete, "/ui/api/restores/" + restoreID},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			service := &fakeRestoreService{}
			recorder := serve(newRestoresHandler(true, service), restoreRequestWithSession(test.method, test.path, validRestoreBody, false))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401", recorder.Code)
			}
			if service.calls != 0 {
				t.Fatal("service called without a session")
			}
		})
	}
}

func TestRestoreWritesRequireJSONContentType(t *testing.T) {
	restoreID := uuid.NewV7().String()
	for _, test := range []struct{ method, path string }{{http.MethodPost, "/ui/api/restores"}, {http.MethodDelete, "/ui/api/restores/" + restoreID}} {
		request := restoreRequestWithSession(test.method, test.path, validRestoreBody, true)
		request.Header.Set("Content-Type", "text/plain")
		if recorder := serve(newRestoresHandler(true, &fakeRestoreService{}), request); recorder.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("%s status %d, want 415", test.method, recorder.Code)
		}
	}
}

func TestRestoreRoutesWhenArchivingDisabled(t *testing.T) {
	restoreID := uuid.NewV7().String()
	service := &fakeRestoreService{}
	handler := newRestoresHandler(false, service)
	for _, test := range []struct{ method, path string }{{http.MethodGet, validEstimateTarget}, {http.MethodPost, "/ui/api/restores"}, {http.MethodDelete, "/ui/api/restores/" + restoreID}} {
		if recorder := serve(handler, restoreRequestWithSession(test.method, test.path, validRestoreBody, true)); recorder.Code != http.StatusNotFound {
			t.Fatalf("%s %s status %d, want 404", test.method, test.path, recorder.Code)
		}
	}
	if service.calls != 0 {
		t.Fatal("service called while archiving is disabled")
	}
	recorder := serve(handler, restoreRequestWithSession(http.MethodGet, "/ui/api/restores", "", true))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"archiving_enabled":false`) || !strings.Contains(recorder.Body.String(), `"restores":[]`) {
		t.Fatalf("unexpected list response %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestEstimateRestoreRejectsBadRange(t *testing.T) {
	tests := map[string]string{
		"missing from":  "/ui/api/restores/estimate?to=2026-09-02T00:00:00Z",
		"missing to":    "/ui/api/restores/estimate?from=2026-09-01T00:00:00Z",
		"from at to":    "/ui/api/restores/estimate?from=2026-09-01T00:00:00Z&to=2026-09-01T00:00:00Z",
		"from after to": "/ui/api/restores/estimate?from=2026-09-02T00:00:00Z&to=2026-09-01T00:00:00Z",
	}
	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			service := &fakeRestoreService{}
			recorder := getWithSession(newRestoresHandler(true, service), target)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", recorder.Code)
			}
			if service.calls != 0 {
				t.Fatal("service called for a bad range")
			}
		})
	}
}

func TestEstimateRestoreFormatsDays(t *testing.T) {
	service := &fakeRestoreService{plan: archive.RestorePlan{ArchivedDays: []time.Time{time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, EstimatedRows: 80000, Allowed: true}}

	recorder := getWithSession(newRestoresHandler(true, service), validEstimateTarget)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	archivedDays := body["archived_days"].([]any)
	if len(archivedDays) != 1 || archivedDays[0] != "2026-09-01" {
		t.Fatalf("unexpected archived days: %v", archivedDays)
	}
	if excludedDays, ok := body["excluded_days"].([]any); !ok || len(excludedDays) != 0 {
		t.Fatalf("excluded_days must be an empty list, got %v", body["excluded_days"])
	}
}

func TestStartRestoreStatuses(t *testing.T) {
	restoreID := uuid.NewV7().String()
	tests := map[string]struct {
		service    *fakeRestoreService
		body       string
		wantStatus int
		wantBody   string
	}{
		"already running": {service: &fakeRestoreService{err: archive.ErrRestoreRunning}, body: validRestoreBody, wantStatus: http.StatusConflict, wantBody: "already running"},
		"refused":         {service: &fakeRestoreService{err: archive.RestoreRefusedError{Reason: "only 0 MiB is free below 80% of the disk budget"}}, body: validRestoreBody, wantStatus: http.StatusUnprocessableEntity, wantBody: "80%"},
		"failure":         {service: &fakeRestoreService{err: io.ErrUnexpectedEOF}, body: validRestoreBody, wantStatus: http.StatusInternalServerError, wantBody: "could not start restore"},
		"started":         {service: &fakeRestoreService{restore: archive.Restore{RestoreID: restoreID, Status: "running"}}, body: validRestoreBody, wantStatus: http.StatusAccepted, wantBody: `"status":"running"`},
		"missing times":   {service: &fakeRestoreService{}, body: `{"project":"shop"}`, wantStatus: http.StatusBadRequest},
		"malformed json":  {service: &fakeRestoreService{}, body: `{`, wantStatus: http.StatusBadRequest},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := serve(newRestoresHandler(true, test.service), restoreRequestWithSession(http.MethodPost, "/ui/api/restores", test.body, true))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status %d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), test.wantBody) {
				t.Fatalf("body %q does not contain %q", recorder.Body.String(), test.wantBody)
			}
			if test.wantStatus == http.StatusAccepted && !strings.Contains(recorder.Body.String(), restoreID) {
				t.Fatalf("body %q lacks the restore id", recorder.Body.String())
			}
		})
	}
}

func TestDeleteRestoreRejectsInvalidIDs(t *testing.T) {
	for _, restoreID := range []string{"abc", uuid.NewV4().String(), "x'%20OR%20'1'='1"} {
		service := &fakeRestoreService{}
		recorder := serve(newRestoresHandler(true, service), restoreRequestWithSession(http.MethodDelete, "/ui/api/restores/"+restoreID, "", true))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%q status %d, want 400", restoreID, recorder.Code)
		}
		if service.calls != 0 {
			t.Fatalf("%q reached the service", restoreID)
		}
	}
}

func TestDeleteRestoreStatuses(t *testing.T) {
	restoreID := uuid.NewV7().String()
	tests := map[string]struct {
		err        error
		wantStatus int
	}{
		"deleted":   {wantStatus: http.StatusNoContent},
		"not found": {err: archive.ErrRestoreNotFound, wantStatus: http.StatusNotFound},
		"failure":   {err: io.ErrUnexpectedEOF, wantStatus: http.StatusInternalServerError},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			service := &fakeRestoreService{err: test.err}
			recorder := serve(newRestoresHandler(true, service), restoreRequestWithSession(http.MethodDelete, "/ui/api/restores/"+restoreID, "", true))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status %d, want %d", recorder.Code, test.wantStatus)
			}
			if service.deletedID != restoreID {
				t.Fatalf("deleted %q, want %q", service.deletedID, restoreID)
			}
		})
	}
}

func TestListRestoresPassesStatusThrough(t *testing.T) {
	service := &fakeRestoreService{restores: []archive.Restore{{RestoreID: uuid.NewV7().String(), Status: "expired"}}}

	recorder := getWithSession(newRestoresHandler(true, service), "/ui/api/restores")

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"expired"`) || !strings.Contains(recorder.Body.String(), `"archiving_enabled":true`) {
		t.Fatalf("unexpected response %d: %s", recorder.Code, recorder.Body.String())
	}
}
