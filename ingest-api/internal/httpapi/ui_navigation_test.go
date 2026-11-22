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

var navigationPaths = []string{"/requests", "/bandwidth", "/alerts", "/archive"}

type navigationUser struct {
	userID          string
	username        string
	permissions     []string
	wantLinks       []string
	wantPermissions string
}

func navigationUsers() []navigationUser {
	return []navigationUser{
		{userID: users.AdminUsername, username: "admin", wantLinks: []string{"/requests", "/bandwidth", "/alerts", "/archive"}, wantPermissions: strings.Join(users.AllPermissions, " ")},
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
