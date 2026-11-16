package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/dilutedev/scrapelog/ingest-api/internal/ui"
)

var versionedCSSPattern = regexp.MustCompile(`/assets/[0-9a-f]{12}/app\.css`)

func TestRootRedirectsToRequests(t *testing.T) {
	recorder := serve(newUIHandler(), cookieRequest(http.MethodGet, "/", ""))
	if recorder.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); location != "/requests" {
		t.Fatalf("location %q, want /requests", location)
	}
}

func TestLoginPageRendersWithoutSession(t *testing.T) {
	recorder := serve(newUIHandler(), cookieRequest(http.MethodGet, "/login", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("content type %q, want text/html", contentType)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `id="admin-token"`) || !strings.Contains(body, "login.js") || !versionedCSSPattern.MatchString(body) {
		t.Fatalf("login page is missing expected content:\n%s", body)
	}
	for _, forbidden := range []string{"<script>", "style=", " onclick=", " onsubmit=", " onload="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("login page contains inline %q", forbidden)
		}
	}
}

func TestUnknownPathReturnsNotFound(t *testing.T) {
	for _, target := range []string{"/nope", "/assets/nope"} {
		t.Run(target, func(t *testing.T) {
			recorder := serve(newUIHandler(), cookieRequest(http.MethodGet, target, ""))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status %d, want 404", recorder.Code)
			}
		})
	}
}

func TestUIPagesAndAssetsCarrySecurityHeaders(t *testing.T) {
	handler := newUIHandler()
	loginBody := serve(handler, cookieRequest(http.MethodGet, "/login", "")).Body.String()
	stylesheetPath := versionedCSSPattern.FindString(loginBody)
	if stylesheetPath == "" {
		t.Fatal("no stylesheet path in login page")
	}
	for _, target := range []string{"/", "/login", stylesheetPath, "/assets/000000000000/app.css"} {
		t.Run(target, func(t *testing.T) {
			headers := serve(handler, cookieRequest(http.MethodGet, target, "")).Header()
			if headers.Get("Content-Security-Policy") != contentSecurityPolicy ||
				headers.Get("X-Content-Type-Options") != "nosniff" ||
				headers.Get("Referrer-Policy") != "same-origin" {
				t.Fatalf("missing security headers: %v", headers)
			}
		})
	}
}

func TestSessionGatedPageRedirectsOrRenders(t *testing.T) {
	signer := newTestSigner()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	gatedPage := withSecurityHeaders(requireSessionPage(signer, ui.PageHandler(logger, "login")))

	withoutSession := serve(gatedPage, cookieRequest(http.MethodGet, "/requests?q=a", ""))
	if withoutSession.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", withoutSession.Code)
	}
	wantLocation := "/login?next=" + url.QueryEscape("/requests?q=a")
	if location := withoutSession.Header().Get("Location"); location != wantLocation {
		t.Fatalf("location %q, want %q", location, wantLocation)
	}

	withSession := serve(gatedPage, cookieRequest(http.MethodGet, "/requests?q=a", signer.Issue()))
	if withSession.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", withSession.Code)
	}
	if !strings.Contains(withSession.Body.String(), `id="admin-token"`) {
		t.Fatal("page not rendered with a valid session")
	}
}

func TestRequestsPageRequiresSession(t *testing.T) {
	recorder := serve(newUIHandler(), cookieRequest(http.MethodGet, "/requests?q=status:403", ""))
	if recorder.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", recorder.Code)
	}
	wantLocation := "/login?next=" + url.QueryEscape("/requests?q=status:403")
	if location := recorder.Header().Get("Location"); location != wantLocation {
		t.Fatalf("location %q, want %q", location, wantLocation)
	}
	if recorder.Header().Get("Content-Security-Policy") != contentSecurityPolicy {
		t.Fatal("missing content security policy")
	}
}

func TestRequestsPageRendersWithSession(t *testing.T) {
	recorder := serve(newUIHandler(), cookieRequest(http.MethodGet, "/requests", newTestSigner().Issue()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("content type %q, want text/html", contentType)
	}
	body := recorder.Body.String()
	wantFragments := []string{"requests.js", "shell.js", `href="/requests" aria-current="page"`, `id="query-input"`, `id="tape-chart"`, `id="detail-pane"`}
	for _, fragment := range wantFragments {
		if !strings.Contains(body, fragment) {
			t.Fatalf("requests page missing %q:\n%s", fragment, body)
		}
	}
	for _, forbidden := range []string{"<script>", "style="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("requests page contains inline %q", forbidden)
		}
	}
}

func TestBandwidthPageRequiresSession(t *testing.T) {
	recorder := serve(newUIHandler(), cookieRequest(http.MethodGet, "/bandwidth", ""))
	if recorder.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", recorder.Code)
	}
	wantLocation := "/login?next=" + url.QueryEscape("/bandwidth")
	if location := recorder.Header().Get("Location"); location != wantLocation {
		t.Fatalf("location %q, want %q", location, wantLocation)
	}
}

func TestBandwidthPageRendersWithSession(t *testing.T) {
	recorder := serve(newUIHandler(), cookieRequest(http.MethodGet, "/bandwidth", newTestSigner().Issue()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	wantFragments := []string{"bandwidth.js", "shell.js", "vendor/uplot/uPlot.min.css", `href="/bandwidth" aria-current="page"`, `id="bandwidth-chart"`, `id="bandwidth-rows"`}
	for _, fragment := range wantFragments {
		if !strings.Contains(body, fragment) {
			t.Fatalf("bandwidth page missing %q:\n%s", fragment, body)
		}
	}
	for _, forbidden := range []string{"<script>", "style=", "onclick=", "onload="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("bandwidth page contains inline %q", forbidden)
		}
	}
}

func TestAlertPagesRequireSession(t *testing.T) {
	for _, target := range []string{"/alerts", "/alerts/new", "/alerts/0190abcd"} {
		t.Run(target, func(t *testing.T) {
			recorder := serve(newUIHandler(), cookieRequest(http.MethodGet, target, ""))
			if recorder.Code != http.StatusFound {
				t.Fatalf("status %d, want 302", recorder.Code)
			}
			wantLocation := "/login?next=" + url.QueryEscape(target)
			if location := recorder.Header().Get("Location"); location != wantLocation {
				t.Fatalf("location %q, want %q", location, wantLocation)
			}
			if recorder.Header().Get("Content-Security-Policy") != contentSecurityPolicy {
				t.Fatal("missing content security policy")
			}
		})
	}
}

func TestAlertPagesRenderWithSession(t *testing.T) {
	tests := map[string]struct {
		target        string
		wantFragments []string
	}{
		"list": {
			target:        "/alerts",
			wantFragments: []string{"alerts.js", "shell.js", `href="/alerts" aria-current="page"`, `id="rule-list"`, `id="event-list"`},
		},
		"new rule": {
			target:        "/alerts/new",
			wantFragments: []string{"alert-edit.js", "shell.js", `href="/alerts" aria-current="page"`, `id="rule-form"`, `id="discord-webhook"`, `id="send-test"`},
		},
		"existing rule": {
			target:        "/alerts/0190abcd",
			wantFragments: []string{"alert-edit.js", "shell.js", `href="/alerts" aria-current="page"`, `id="rule-form"`, `id="discord-webhook"`, `id="send-test"`},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := serve(newUIHandler(), cookieRequest(http.MethodGet, test.target, newTestSigner().Issue()))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d, want 200", recorder.Code)
			}
			body := recorder.Body.String()
			for _, fragment := range test.wantFragments {
				if !strings.Contains(body, fragment) {
					t.Fatalf("page missing %q:\n%s", fragment, body)
				}
			}
			for _, forbidden := range []string{"<script>", "style=", "onclick=", "onload="} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("page contains inline %q", forbidden)
				}
			}
		})
	}
}

func TestArchivePageRequiresSession(t *testing.T) {
	recorder := serve(newUIHandler(), cookieRequest(http.MethodGet, "/archive", ""))
	if recorder.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); location != "/login?next=%2Farchive" {
		t.Fatalf("location %q, want /login?next=%%2Farchive", location)
	}
	if recorder.Header().Get("Content-Security-Policy") != contentSecurityPolicy {
		t.Fatal("missing content security policy")
	}
}

func TestArchivePageRendersWithSession(t *testing.T) {
	recorder := serve(newUIHandler(), cookieRequest(http.MethodGet, "/archive", newTestSigner().Issue()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, fragment := range []string{"archive.js", "shell.js", `href="/archive" aria-current="page"`, `id="archive-list"`, `id="restore-form"`, `id="restore-list"`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("page missing %q:\n%s", fragment, body)
		}
	}
	for _, forbidden := range []string{"<script>", "style=", "onclick=", "onload="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("page contains inline %q", forbidden)
		}
	}
}
