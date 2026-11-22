package ui

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/dilutedev/scrapelog/ingest-api/internal/users"
)

func serveAsset(target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	AssetHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

func TestAssetHandlerServesVersionedFileWithImmutableCache(t *testing.T) {
	recorder := serveAsset("/assets/" + assetVersion + "/app.css")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/css") {
		t.Fatalf("content type %q, want text/css", contentType)
	}
	if recorder.Body.Len() == 0 {
		t.Fatal("empty body")
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "public, max-age=31536000, immutable" {
		t.Fatalf("cache control %q", cacheControl)
	}
}

func TestAssetHandlerReturnsNotFoundWithoutCacheHeader(t *testing.T) {
	tests := map[string]string{
		"wrong version": "/assets/000000000000/app.css",
		"unknown file":  "/assets/" + assetVersion + "/missing.css",
		"directory":     "/assets/" + assetVersion + "/fonts",
		"bare prefix":   "/assets/",
	}
	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := serveAsset(target)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status %d, want 404", recorder.Code)
			}
			if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "" {
				t.Fatalf("cache control %q, want empty", cacheControl)
			}
		})
	}
}

func TestAssetHandlerServesWoff2AsFontType(t *testing.T) {
	for _, fileName := range []string{"AtkinsonHyperlegibleNext.woff2", "AtkinsonHyperlegibleMono.woff2"} {
		t.Run(fileName, func(t *testing.T) {
			recorder := serveAsset("/assets/" + assetVersion + "/fonts/" + fileName)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d, want 200", recorder.Code)
			}
			if contentType := recorder.Header().Get("Content-Type"); contentType != "font/woff2" {
				t.Fatalf("content type %q, want font/woff2", contentType)
			}
		})
	}
}

func TestAssetVersionIsTwelveHexCharacters(t *testing.T) {
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(assetVersion) {
		t.Fatalf("asset version %q is not 12 hex characters", assetVersion)
	}
}

func TestTopbarMarksCurrentSection(t *testing.T) {
	tests := map[string]struct {
		page             string
		requestsCurrent  string
		bandwidthCurrent string
		alertsCurrent    string
		archiveCurrent   string
	}{
		"bandwidth":  {page: "bandwidth", requestsCurrent: "false", bandwidthCurrent: "page", alertsCurrent: "false", archiveCurrent: "false"},
		"alert edit": {page: "alert-edit", requestsCurrent: "false", bandwidthCurrent: "false", alertsCurrent: "page", archiveCurrent: "false"},
		"archive":    {page: "archive", requestsCurrent: "false", bandwidthCurrent: "false", alertsCurrent: "false", archiveCurrent: "page"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var rendered bytes.Buffer
			if err := pageTemplates.ExecuteTemplate(&rendered, "topbar", pageData{AssetVersion: "v", Page: test.page, Viewer: Viewer{IsAdmin: true, Permissions: []string{"view_requests", "view_bandwidth", "view_alerts", "manage_alerts", "view_archives"}}}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			output := rendered.String()
			wantFragments := []string{
				`<a href="/requests" aria-current="` + test.requestsCurrent + `">Requests</a>`,
				`<a href="/bandwidth" aria-current="` + test.bandwidthCurrent + `">Bandwidth</a>`,
				`<a href="/alerts" aria-current="` + test.alertsCurrent + `">Alerts</a>`,
				`<a href="/archive" aria-current="` + test.archiveCurrent + `">Archive</a>`,
				`id="sign-out"`,
				`/assets/v/shell.js`,
			}
			for _, fragment := range wantFragments {
				if !strings.Contains(output, fragment) {
					t.Fatalf("output missing %q:\n%s", fragment, output)
				}
			}
		})
	}
}

func TestScriptsUseNoHTMLSinks(t *testing.T) {
	forbiddenStrings := []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function"}
	walkError := fs.WalkDir(embeddedFiles, "assets", func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && filePath == "assets/vendor" {
			return fs.SkipDir
		}
		if entry.IsDir() || path.Ext(filePath) != ".js" {
			return nil
		}
		content, readErr := fs.ReadFile(embeddedFiles, filePath)
		if readErr != nil {
			return readErr
		}
		for _, forbidden := range forbiddenStrings {
			if strings.Contains(string(content), forbidden) {
				t.Errorf("%s contains %q", filePath, forbidden)
			}
		}
		return nil
	})
	if walkError != nil {
		t.Fatalf("unexpected error: %v", walkError)
	}
}

func TestVendoredUPlotIsServed(t *testing.T) {
	tests := map[string]string{
		"uPlot.esm.js":  "javascript",
		"uPlot.min.css": "text/css",
		"LICENSE":       "",
	}
	for fileName, expectedContentType := range tests {
		t.Run(fileName, func(t *testing.T) {
			recorder := serveAsset("/assets/" + assetVersion + "/vendor/uplot/" + fileName)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d, want 200", recorder.Code)
			}
			if !strings.Contains(recorder.Header().Get("Content-Type"), expectedContentType) {
				t.Fatalf("content type %q, want it to contain %q", recorder.Header().Get("Content-Type"), expectedContentType)
			}
			if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "public, max-age=31536000, immutable" {
				t.Fatalf("cache control %q", cacheControl)
			}
			if recorder.Body.Len() == 0 {
				t.Fatal("empty body")
			}
		})
	}
}

func TestTopbarShowsUsersLinkOnlyToAdmin(t *testing.T) {
	tests := map[string]struct {
		viewer   Viewer
		wantLink bool
	}{
		"admin":     {viewer: Viewer{IsAdmin: true}, wantLink: true},
		"non-admin": {viewer: Viewer{Username: "alice", Permissions: users.AllPermissions}, wantLink: false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var rendered bytes.Buffer
			if err := pageTemplates.ExecuteTemplate(&rendered, "topbar", pageData{AssetVersion: "v", Page: "users", Viewer: test.viewer}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			hasLink := strings.Contains(rendered.String(), `<a href="/users" aria-current="page">Users</a>`)
			if hasLink != test.wantLink {
				t.Fatalf("Users link present = %v, want %v:\n%s", hasLink, test.wantLink, rendered.String())
			}
		})
	}
}

func TestUsersPageRendersFormAndEveryPermission(t *testing.T) {
	var rendered bytes.Buffer
	if err := pageTemplates.ExecuteTemplate(&rendered, "users.html", pageData{AssetVersion: "v", Page: "users", Viewer: Viewer{IsAdmin: true}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := rendered.String()
	for _, fragment := range []string{`/assets/v/users.js`, `id="user-form"`, `id="user-list"`} {
		if !strings.Contains(output, fragment) {
			t.Errorf("output missing %q", fragment)
		}
	}
	for _, permission := range users.AllPermissions {
		if !strings.Contains(output, `name="permission" value="`+permission+`"`) {
			t.Errorf("output missing a checkbox for %q", permission)
		}
	}
	if got := strings.Count(output, `name="permission"`); got != len(users.AllPermissions) {
		t.Errorf("checkbox count = %d, want %d", got, len(users.AllPermissions))
	}
}

func TestManifestIsServedWithRequiredFields(t *testing.T) {
	recorder := serveAsset("/assets/" + assetVersion + "/manifest.json")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("content type %q, want application/json", contentType)
	}
	var manifest struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
		StartURL  string `json:"start_url"`
		Scope     string `json:"scope"`
		Display   string `json:"display"`
		Icons     []struct {
			Source  string `json:"src"`
			Sizes   string `json:"sizes"`
			Type    string `json:"type"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if manifest.Name == "" || manifest.ShortName == "" {
		t.Fatalf("name %q and short_name %q must both be set", manifest.Name, manifest.ShortName)
	}
	if manifest.ID != "/" || manifest.StartURL != "/" || manifest.Scope != "/" {
		t.Fatalf("id %q, start_url %q, scope %q, want all /", manifest.ID, manifest.StartURL, manifest.Scope)
	}
	if manifest.Display != "standalone" {
		t.Fatalf("display %q, want standalone", manifest.Display)
	}
	foundSizes := map[string]bool{}
	foundMaskable := false
	for _, icon := range manifest.Icons {
		if icon.Type != "image/png" {
			t.Fatalf("icon %q type %q, want image/png", icon.Source, icon.Type)
		}
		foundSizes[icon.Sizes] = true
		if icon.Purpose == "maskable" {
			foundMaskable = true
		}
		iconRecorder := serveAsset("/assets/" + assetVersion + "/" + icon.Source)
		if iconRecorder.Code != http.StatusOK {
			t.Fatalf("icon %q status %d, want 200", icon.Source, iconRecorder.Code)
		}
		if contentType := iconRecorder.Header().Get("Content-Type"); contentType != "image/png" {
			t.Fatalf("icon %q content type %q, want image/png", icon.Source, contentType)
		}
	}
	if !foundSizes["192x192"] || !foundSizes["512x512"] {
		t.Fatalf("icon sizes %v must include 192x192 and 512x512", foundSizes)
	}
	if !foundMaskable {
		t.Fatal("no icon has purpose maskable")
	}
}

func TestIconSVGIsServedAsSVG(t *testing.T) {
	recorder := serveAsset("/assets/" + assetVersion + "/icon.svg")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "image/svg+xml" {
		t.Fatalf("content type %q, want image/svg+xml", contentType)
	}
}

func TestEveryPageIncludesHeadPartial(t *testing.T) {
	entries, err := fs.ReadDir(embeddedFiles, "pages")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pageNames := []string{}
	for _, entry := range entries {
		if entry.Name() == "topbar.html" || entry.Name() == "head.html" {
			continue
		}
		pageNames = append(pageNames, entry.Name())
	}
	if len(pageNames) != 8 {
		t.Fatalf("found %d pages %v, want 8", len(pageNames), pageNames)
	}
	wantFragments := []string{
		`href="/assets/v/manifest.json" crossorigin="use-credentials"`,
		`href="/assets/v/icon.svg"`,
		`rel="apple-touch-icon"`,
		`name="theme-color"`,
		`src="/assets/v/theme-init.js"`,
	}
	for _, pageName := range pageNames {
		t.Run(pageName, func(t *testing.T) {
			var rendered bytes.Buffer
			if err := pageTemplates.ExecuteTemplate(&rendered, pageName, pageData{AssetVersion: "v", Viewer: Viewer{IsAdmin: true}}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, fragment := range wantFragments {
				if !strings.Contains(rendered.String(), fragment) {
					t.Fatalf("output missing %q", fragment)
				}
			}
			if strings.Index(rendered.String(), "theme-init.js") > strings.Index(rendered.String(), "app.css") {
				t.Fatal("theme-init.js must come before app.css so the theme is set before first paint")
			}
		})
	}
}

func TestBrandMarkAppearsInTopbarAndLogin(t *testing.T) {
	for _, templateName := range []string{"topbar", "login.html"} {
		t.Run(templateName, func(t *testing.T) {
			var rendered bytes.Buffer
			if err := pageTemplates.ExecuteTemplate(&rendered, templateName, pageData{AssetVersion: "v", Viewer: Viewer{IsAdmin: true}}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			output := rendered.String()
			if !strings.Contains(output, `class="brand-mark" src="/assets/v/icon.svg"`) {
				t.Fatalf("output missing brand mark:\n%s", output)
			}
		})
	}
}

func TestTopbarHasThemeToggle(t *testing.T) {
	var topbar bytes.Buffer
	if err := pageTemplates.ExecuteTemplate(&topbar, "topbar", pageData{AssetVersion: "v"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, fragment := range []string{`id="theme-toggle"`, `aria-pressed="false"`} {
		if !strings.Contains(topbar.String(), fragment) {
			t.Fatalf("topbar missing %q", fragment)
		}
	}
	var login bytes.Buffer
	if err := pageTemplates.ExecuteTemplate(&login, "login.html", pageData{AssetVersion: "v"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(login.String(), `id="theme-toggle"`) {
		t.Fatal("login page must not have a theme toggle")
	}
}

func TestAppCSSHasNoColourLiteralsOutsideThemeBlocks(t *testing.T) {
	stylesheet, err := fs.ReadFile(embeddedFiles, "assets/app.css")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	withoutThemeBlocks := regexp.MustCompile(`:root[^{]*\{[^}]*\}`).ReplaceAllString(string(stylesheet), "")
	if literal := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|rgba?\(`).FindString(withoutThemeBlocks); literal != "" {
		t.Fatalf("colour literal %q outside the :root theme blocks", literal)
	}
	darkBlock := regexp.MustCompile(`:root\[data-theme="dark"\]\s*\{[^}]*\}`).FindString(string(stylesheet))
	if darkBlock == "" {
		t.Fatal("dark theme block not found")
	}
	for _, variable := range []string{"--chart-1:", "--chart-2:", "--chart-3:", "--chart-4:", "--chart-5:", "--chart-6:", "--chart-7:", "--chart-8:", "--chart-other:"} {
		if !strings.Contains(darkBlock, variable) {
			t.Fatalf("dark theme block missing %s", variable)
		}
	}
}

func TestBandwidthScriptHasNoColourLiterals(t *testing.T) {
	script, err := fs.ReadFile(embeddedFiles, "assets/bandwidth.js")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if literal := regexp.MustCompile(`#[0-9a-fA-F]{6}\b`).FindString(string(script)); literal != "" {
		t.Fatalf("colour literal %q in bandwidth.js", literal)
	}
}
