package ui

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"strings"
	"testing"
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
			if err := pageTemplates.ExecuteTemplate(&rendered, "topbar", pageData{AssetVersion: "v", Page: test.page}); err != nil {
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
