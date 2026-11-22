package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/session"
	"github.com/dilutedev/scrapelog/ingest-api/internal/users"
)

type permissionRoute struct {
	pattern      string
	page         bool
	anyOf        []string
	anyPrincipal bool
}

var permissionMatrix = []permissionRoute{
	{pattern: "GET /ui/api/session", anyPrincipal: true},
	{pattern: "GET /ui/api/requests", anyOf: []string{users.ViewRequests}},
	{pattern: "GET /ui/api/requests/export", anyOf: []string{users.ViewRequests}},
	{pattern: "GET /ui/api/request", anyOf: []string{users.ViewRequests}},
	{pattern: "GET /ui/api/request/body", anyOf: []string{users.ViewRequests}},
	{pattern: "GET /ui/api/tape", anyOf: []string{users.ViewRequests}},
	{pattern: "GET /ui/api/facets", anyOf: []string{users.ViewRequests, users.ViewBandwidth}},
	{pattern: "GET /ui/api/bandwidth", anyOf: []string{users.ViewBandwidth}},
	{pattern: "GET /ui/api/alert-rules", anyOf: []string{users.ViewAlerts, users.ManageAlerts}},
	{pattern: "POST /ui/api/alert-rules", anyOf: []string{users.ManageAlerts}},
	{pattern: "PUT /ui/api/alert-rules/{rule_id}", anyOf: []string{users.ManageAlerts}},
	{pattern: "DELETE /ui/api/alert-rules/{rule_id}", anyOf: []string{users.ManageAlerts}},
	{pattern: "POST /ui/api/alert-rules/{rule_id}/test", anyOf: []string{users.ManageAlerts}},
	{pattern: "GET /ui/api/alert-events", anyOf: []string{users.ViewAlerts, users.ManageAlerts}},
	{pattern: "GET /ui/api/archives", anyOf: []string{users.ViewArchives}},
	{pattern: "GET /ui/api/archives/{day}/download", anyOf: []string{users.ViewArchives}},
	{pattern: "GET /ui/api/restores/estimate", anyOf: []string{users.EstimateRestores, users.ManageRestores}},
	{pattern: "GET /ui/api/restores", anyOf: []string{users.ViewRequests, users.ViewArchives, users.ManageRestores}},
	{pattern: "POST /ui/api/restores", anyOf: []string{users.ManageRestores}},
	{pattern: "DELETE /ui/api/restores/{restore_id}", anyOf: []string{users.ManageRestores}},
	{pattern: "GET /requests", page: true, anyOf: []string{users.ViewRequests}},
	{pattern: "GET /bandwidth", page: true, anyOf: []string{users.ViewBandwidth}},
	{pattern: "GET /alerts", page: true, anyOf: []string{users.ViewAlerts, users.ManageAlerts}},
	{pattern: "GET /alerts/{rule_id}", page: true, anyOf: []string{users.ManageAlerts}},
	{pattern: "GET /archive", page: true, anyOf: []string{users.ViewArchives, users.EstimateRestores, users.ManageRestores}},
}

var ungatedRoutePatterns = []string{
	"GET /v1/health",
	"GET /docs",
	"POST /v1/admin/keys",
	"GET /v1/admin/keys",
	"DELETE /v1/admin/keys/{key_id}",
	"POST /v1/requests",
	"POST /v1/requests/stream",
	"GET /v1/stats",
	"GET /v1/admin/storage",
	"POST /ui/api/login",
	"POST /ui/api/logout",
	"GET /{$}",
	"GET /login",
	"GET /assets/",
}

func TestEveryUIRouteIsClassified(t *testing.T) {
	source, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatalf("read handler.go: %v", err)
	}
	registered := []string{}
	for _, match := range regexp.MustCompile(`router\.HandleFunc\("([A-Z]+ [^"]+)"`).FindAllStringSubmatch(string(source), -1) {
		registered = append(registered, match[1])
	}
	classified := slices.Clone(ungatedRoutePatterns)
	for _, route := range permissionMatrix {
		classified = append(classified, route.pattern)
	}
	for _, pattern := range registered {
		if !slices.Contains(classified, pattern) {
			t.Errorf("route %q is not classified in permissionMatrix or ungatedRoutePatterns", pattern)
		}
	}
	for _, pattern := range classified {
		if !slices.Contains(registered, pattern) {
			t.Errorf("classified pattern %q is not registered in handler.go", pattern)
		}
	}
}

func newPermissionHandler(userDirectory UserDirectory) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewHandler(context.Background(), logger, &fakePinger{}, &fakeKeyStore{}, &fakeRefresher{}, &fakeRecordWriter{}, &fakeBodyStore{}, &fakeStorageReporter{}, &fakeDiskGuard{}, &fakeRequestReader{}, &fakeBodyGetter{}, &fakeAlertStore{}, &fakeRefresher{}, &fakeAlertTester{}, emptyAlertMatcher(), &fakeAlertQueue{}, false, &fakeArchiveLister{}, &fakeArchiveDownloader{}, &fakeRestoreService{}, userDirectory, config.Config{AdminToken: testAdminToken})
}

func permissionRequest(route permissionRoute, cookieValue string) *http.Request {
	method, pattern, _ := strings.Cut(route.pattern, " ")
	target := regexp.MustCompile(`\{[a-z_]+\}`).ReplaceAllString(pattern, "x")
	request := httptest.NewRequest(method, target, strings.NewReader("{}"))
	if method != http.MethodGet {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookieValue != "" {
		request.AddCookie(&http.Cookie{Name: session.CookieName, Value: cookieValue})
	}
	return request
}

func userSession(userID string, sessionVersion uint32) string {
	return newTestSigner().Issue(userID, sessionVersion)
}

func TestPermissionMatrix(t *testing.T) {
	directory := &fakeUserDirectory{records: map[string]users.Record{
		"user-none": {UserID: "user-none", Username: "none", SessionVersion: 1},
	}}
	for _, permission := range users.AllPermissions {
		userID := "user-" + permission
		directory.records[userID] = users.Record{UserID: userID, Username: permission, Permissions: []string{permission}, SessionVersion: 1}
	}
	handler := newPermissionHandler(directory)

	for _, route := range permissionMatrix {
		t.Run(route.pattern, func(t *testing.T) {
			unauthenticated := serve(handler, permissionRequest(route, ""))
			wantUnauthenticated := http.StatusUnauthorized
			if route.page {
				wantUnauthenticated = http.StatusFound
			}
			if unauthenticated.Code != wantUnauthenticated {
				t.Fatalf("no cookie: status %d, want %d", unauthenticated.Code, wantUnauthenticated)
			}

			admin := serve(handler, permissionRequest(route, adminSession()))
			if admin.Code == http.StatusUnauthorized || admin.Code == http.StatusForbidden {
				t.Fatalf("admin: status %d", admin.Code)
			}

			for userID := range directory.records {
				recorder := serve(handler, permissionRequest(route, userSession(userID, 1)))
				permission := strings.TrimPrefix(userID, "user-")
				wantAllowed := route.anyPrincipal || slices.Contains(route.anyOf, permission)
				gotAllowed := recorder.Code != http.StatusUnauthorized && recorder.Code != http.StatusForbidden
				if gotAllowed != wantAllowed {
					t.Errorf("%s: status %d, allowed = %v, want %v", userID, recorder.Code, gotAllowed, wantAllowed)
				}
				if !wantAllowed && recorder.Code != http.StatusForbidden {
					t.Errorf("%s: status %d, want 403", userID, recorder.Code)
				}
			}
		})
	}
}

func TestUserSessionRevocation(t *testing.T) {
	record := users.Record{UserID: "user-1", Username: "alice", Permissions: []string{users.ViewRequests}, SessionVersion: 1}
	cookieValue := userSession("user-1", 1)
	tests := map[string]struct {
		mutate   func(directory *fakeUserDirectory)
		wantAPI  int
		wantPage int
	}{
		"unchanged":      {mutate: func(directory *fakeUserDirectory) {}, wantAPI: http.StatusOK, wantPage: http.StatusOK},
		"password reset": {mutate: func(directory *fakeUserDirectory) { record.SessionVersion = 2; directory.records["user-1"] = record }, wantAPI: http.StatusUnauthorized, wantPage: http.StatusFound},
		"disabled":       {mutate: func(directory *fakeUserDirectory) { record.Disabled = true; directory.records["user-1"] = record }, wantAPI: http.StatusUnauthorized, wantPage: http.StatusFound},
		"removed":        {mutate: func(directory *fakeUserDirectory) { delete(directory.records, "user-1") }, wantAPI: http.StatusUnauthorized, wantPage: http.StatusFound},
		"permissions changed": {mutate: func(directory *fakeUserDirectory) {
			record.Permissions = []string{users.ViewBandwidth}
			directory.records["user-1"] = record
		}, wantAPI: http.StatusForbidden, wantPage: http.StatusForbidden},
		"users unavailable": {mutate: func(directory *fakeUserDirectory) { directory.unavailable = true }, wantAPI: http.StatusServiceUnavailable, wantPage: http.StatusServiceUnavailable},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			record = users.Record{UserID: "user-1", Username: "alice", Permissions: []string{users.ViewRequests}, SessionVersion: 1}
			directory := &fakeUserDirectory{records: map[string]users.Record{"user-1": record}}
			handler := newPermissionHandler(directory)
			test.mutate(directory)

			api := serve(handler, cookieRequest(http.MethodGet, "/ui/api/requests", cookieValue))
			if api.Code != test.wantAPI {
				t.Fatalf("api status %d, want %d", api.Code, test.wantAPI)
			}
			page := serve(handler, cookieRequest(http.MethodGet, "/requests", cookieValue))
			if page.Code != test.wantPage {
				t.Fatalf("page status %d, want %d", page.Code, test.wantPage)
			}
			if directory.unavailable {
				adminAPI := serve(handler, cookieRequest(http.MethodGet, "/ui/api/requests", adminSession()))
				if adminAPI.Code != http.StatusOK {
					t.Fatalf("admin status %d, want 200 while users are unavailable", adminAPI.Code)
				}
			}
		})
	}
}
