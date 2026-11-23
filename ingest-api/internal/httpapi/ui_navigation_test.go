package httpapi

import (
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/dilutedev/scrapelog/ingest-api/internal/users"
)

var (
	navigationHrefPattern    = regexp.MustCompile(`<a href="([^"]+)" aria-current=`)
	topbarUsernamePattern    = regexp.MustCompile(`class="topbar-user" title="[^"]*">([^<]*)<`)
	topbarPermissionsPattern = regexp.MustCompile(`data-permissions="([^"]*)"`)
)

var navigationPaths = []string{"/requests", "/bandwidth", "/alerts", "/archive", "/users", "/projects"}

type navigationUser struct {
	userID          string
	username        string
	permissions     []string
	wantLinks       []string
	wantPermissions string
}

func navigationUsers() []navigationUser {
	return []navigationUser{
		{userID: users.AdminUsername, username: "admin", wantLinks: []string{"/requests", "/bandwidth", "/alerts", "/archive", "/users", "/projects"}, wantPermissions: strings.Join(users.AllPermissions, " ")},
		{userID: "user-requests", username: "reader", permissions: []string{users.ViewRequests}, wantLinks: []string{"/requests"}, wantPermissions: users.ViewRequests},
		{userID: "user-bandwidth", username: "bandwidth-only", permissions: []string{users.ViewBandwidth}, wantLinks: []string{"/bandwidth"}, wantPermissions: users.ViewBandwidth},
		{userID: "user-alerts", username: "alert-viewer", permissions: []string{users.ViewAlerts}, wantLinks: []string{"/alerts"}, wantPermissions: users.ViewAlerts},
		{userID: "user-manage-alerts", username: "alert-manager", permissions: []string{users.ManageAlerts}, wantLinks: []string{"/alerts"}, wantPermissions: users.ManageAlerts},
		{userID: "user-view-archives", username: "archive-viewer", permissions: []string{users.ViewArchives}, wantLinks: []string{"/archive"}, wantPermissions: users.ViewArchives},
		{userID: "user-estimate", username: "estimator", permissions: []string{users.EstimateRestores}, wantLinks: []string{"/archive"}, wantPermissions: users.EstimateRestores},
		{userID: "user-manage-restores", username: "restorer", permissions: []string{users.ManageRestores}, wantLinks: []string{"/archive"}, wantPermissions: users.ManageRestores},
		{userID: "user-two", username: "two-pages", permissions: []string{users.ViewRequests, users.ViewBandwidth}, wantLinks: []string{"/requests", "/bandwidth"}, wantPermissions: users.ViewRequests + " " + users.ViewBandwidth},
	}
}

func navigationDirectory() *fakeUserDirectory {
	directory := &fakeUserDirectory{records: map[string]users.Record{}}
	for _, testUser := range navigationUsers() {
		if testUser.userID != users.AdminUsername {
			directory.records[testUser.userID] = users.Record{UserID: testUser.userID, Username: testUser.username, Permissions: testUser.permissions, SessionVersion: 1}
		}
	}
	return directory
}

func navigationSession(testUser navigationUser) string {
	if testUser.userID == users.AdminUsername {
		return userSession(users.AdminUsername, 0)
	}
	return userSession(testUser.userID, 1)
}

func TestTopbarShowsPermittedLinksUsernameAndPermissions(t *testing.T) {
	handler := newPermissionHandler(navigationDirectory())
	for _, testUser := range navigationUsers() {
		t.Run(testUser.username, func(t *testing.T) {
			recorder := serve(handler, cookieRequest(http.MethodGet, testUser.wantLinks[0], navigationSession(testUser)))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d, want 200", recorder.Code)
			}
			body := recorder.Body.String()
			var gotLinks []string
			for _, match := range navigationHrefPattern.FindAllStringSubmatch(body, -1) {
				gotLinks = append(gotLinks, match[1])
			}
			if !slices.Equal(gotLinks, testUser.wantLinks) {
				t.Fatalf("links %v, want %v", gotLinks, testUser.wantLinks)
			}
			if match := topbarUsernamePattern.FindStringSubmatch(body); match == nil || match[1] != testUser.username {
				t.Fatalf("topbar user %v, want %q", match, testUser.username)
			}
			if match := topbarPermissionsPattern.FindStringSubmatch(body); match == nil || match[1] != testUser.wantPermissions {
				t.Fatalf("data-permissions %v, want %q", match, testUser.wantPermissions)
			}
		})
	}
}

func TestNavigationLinksMatchPagesTheUserCanOpen(t *testing.T) {
	handler := newPermissionHandler(navigationDirectory())
	for _, testUser := range navigationUsers() {
		for _, pagePath := range navigationPaths {
			t.Run(testUser.username+" "+pagePath, func(t *testing.T) {
				recorder := serve(handler, cookieRequest(http.MethodGet, pagePath, navigationSession(testUser)))
				wantOpen := slices.Contains(testUser.wantLinks, pagePath)
				if (recorder.Code == http.StatusOK) != wantOpen {
					t.Fatalf("status %d for %s, link shown %v", recorder.Code, pagePath, wantOpen)
				}
			})
		}
	}
}

func forbiddenDirectory() *fakeUserDirectory {
	return &fakeUserDirectory{records: map[string]users.Record{
		"user-none":      {UserID: "user-none", Username: "nobody", SessionVersion: 1},
		"user-bandwidth": {UserID: "user-bandwidth", Username: "bob", Permissions: []string{users.ViewBandwidth}, SessionVersion: 1},
	}}
}

func TestForbiddenPageLinksToFirstPermittedPage(t *testing.T) {
	recorder := serve(newPermissionHandler(forbiddenDirectory()), cookieRequest(http.MethodGet, "/requests", userSession("user-bandwidth", 1)))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("content type %q, want text/html", contentType)
	}
	body := recorder.Body.String()
	for _, fragment := range []string{"You don't have access to this page", `<a class="action-button" href="/bandwidth">Open Bandwidth</a>`, `class="topbar-user" title="bob">bob<`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("body missing %q:\n%s", fragment, body)
		}
	}
	var gotLinks []string
	for _, match := range navigationHrefPattern.FindAllStringSubmatch(body, -1) {
		gotLinks = append(gotLinks, match[1])
	}
	if !slices.Equal(gotLinks, []string{"/bandwidth"}) {
		t.Fatalf("links %v, want [/bandwidth]", gotLinks)
	}
}

func TestForbiddenPageForUserWithoutPermissions(t *testing.T) {
	handler := newPermissionHandler(forbiddenDirectory())
	for _, target := range []string{"/", "/requests", "/archive"} {
		t.Run(target, func(t *testing.T) {
			recorder := serve(handler, cookieRequest(http.MethodGet, target, userSession("user-none", 1)))
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403", recorder.Code)
			}
			body := recorder.Body.String()
			if !strings.Contains(body, "Your account has no access yet") || strings.Contains(body, "action-button") {
				t.Fatalf("unexpected body:\n%s", body)
			}
			if len(navigationHrefPattern.FindAllString(body, -1)) != 0 {
				t.Fatalf("nav links rendered for a user without permissions:\n%s", body)
			}
		})
	}
}

func TestRootRedirectsToFirstPermittedPage(t *testing.T) {
	tests := map[string]struct {
		permissions  []string
		wantLocation string
	}{
		"view requests":          {permissions: []string{users.ViewRequests}, wantLocation: "/requests"},
		"view bandwidth":         {permissions: []string{users.ViewBandwidth}, wantLocation: "/bandwidth"},
		"manage alerts":          {permissions: []string{users.ManageAlerts}, wantLocation: "/alerts"},
		"estimate restores":      {permissions: []string{users.EstimateRestores}, wantLocation: "/archive"},
		"requests and bandwidth": {permissions: []string{users.ViewBandwidth, users.ViewRequests}, wantLocation: "/requests"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			directory := &fakeUserDirectory{records: map[string]users.Record{
				"user-a": {UserID: "user-a", Username: "a", Permissions: test.permissions, SessionVersion: 1},
			}}
			recorder := serve(newPermissionHandler(directory), cookieRequest(http.MethodGet, "/", userSession("user-a", 1)))
			if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != test.wantLocation {
				t.Fatalf("status %d location %q, want 302 %q", recorder.Code, recorder.Header().Get("Location"), test.wantLocation)
			}
		})
	}
}

func TestRootWithoutSessionOrUsersDirectory(t *testing.T) {
	withoutCookie := serve(newPermissionHandler(&fakeUserDirectory{}), cookieRequest(http.MethodGet, "/", ""))
	if withoutCookie.Code != http.StatusFound || withoutCookie.Header().Get("Location") != "/login" {
		t.Fatalf("status %d location %q, want 302 /login", withoutCookie.Code, withoutCookie.Header().Get("Location"))
	}
	unavailable := serve(newPermissionHandler(&fakeUserDirectory{unavailable: true}), cookieRequest(http.MethodGet, "/", userSession("user-a", 1)))
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", unavailable.Code)
	}
}

func TestArchivePageShowsOnlyPermittedBlocks(t *testing.T) {
	tests := map[string]struct {
		permissions []string
		wantPresent []string
	}{
		"admin":                      {wantPresent: []string{"archive-list", "restore-form", "start-restore", "restore-list"}},
		"estimate only":              {permissions: []string{users.EstimateRestores}, wantPresent: []string{"restore-form"}},
		"manage restores":            {permissions: []string{users.ManageRestores}, wantPresent: []string{"restore-form", "start-restore", "restore-list"}},
		"view archives":              {permissions: []string{users.ViewArchives}, wantPresent: []string{"archive-list", "restore-list"}},
		"view archives and estimate": {permissions: []string{users.ViewArchives, users.EstimateRestores}, wantPresent: []string{"archive-list", "restore-form", "restore-list"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			directory := &fakeUserDirectory{records: map[string]users.Record{
				"user-a": {UserID: "user-a", Username: "a", Permissions: test.permissions, SessionVersion: 1},
			}}
			cookieValue := userSession("user-a", 1)
			if name == "admin" {
				cookieValue = userSession(users.AdminUsername, 0)
			}
			recorder := serve(newPermissionHandler(directory), cookieRequest(http.MethodGet, "/archive", cookieValue))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d, want 200", recorder.Code)
			}
			for _, elementID := range []string{"archive-list", "restore-form", "start-restore", "restore-list"} {
				present := strings.Contains(recorder.Body.String(), `id="`+elementID+`"`)
				if present != slices.Contains(test.wantPresent, elementID) {
					t.Errorf("%s present %v, want %v", elementID, present, !present)
				}
			}
		})
	}
}

func TestAlertsPageOffersNewRuleOnlyToManagers(t *testing.T) {
	directory := &fakeUserDirectory{records: map[string]users.Record{
		"viewer":  {UserID: "viewer", Username: "viewer", Permissions: []string{users.ViewAlerts}, SessionVersion: 1},
		"manager": {UserID: "manager", Username: "manager", Permissions: []string{users.ManageAlerts}, SessionVersion: 1},
	}}
	handler := newPermissionHandler(directory)
	tests := map[string]struct {
		cookieValue string
		wantLink    bool
	}{
		"admin":   {cookieValue: userSession(users.AdminUsername, 0), wantLink: true},
		"manager": {cookieValue: userSession("manager", 1), wantLink: true},
		"viewer":  {cookieValue: userSession("viewer", 1), wantLink: false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := serve(handler, cookieRequest(http.MethodGet, "/alerts", test.cookieValue))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d, want 200", recorder.Code)
			}
			if hasLink := strings.Contains(recorder.Body.String(), `href="/alerts/new"`); hasLink != test.wantLink {
				t.Fatalf("new rule link present %v, want %v", hasLink, test.wantLink)
			}
		})
	}
	editor := serve(handler, cookieRequest(http.MethodGet, "/alerts/x", userSession("viewer", 1)))
	if editor.Code != http.StatusForbidden || !strings.Contains(editor.Body.String(), "You don't have access to this page") {
		t.Fatalf("editor status %d for view-only user, want styled 403", editor.Code)
	}
}
