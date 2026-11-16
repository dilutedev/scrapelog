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
