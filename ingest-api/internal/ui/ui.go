package ui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"
)

//go:embed assets pages
var embeddedFiles embed.FS

var assetVersion = assetTreeVersion()
var pageTemplates = template.Must(template.ParseFS(embeddedFiles, "pages/*.html"))

type Viewer struct {
	Username    string
	IsAdmin     bool
	Permissions []string
}

type viewerContextKey struct{}

func WithViewer(parent context.Context, viewer Viewer) context.Context {
	return context.WithValue(parent, viewerContextKey{}, viewer)
}

func ViewerFromContext(requestContext context.Context) Viewer {
	viewer, _ := requestContext.Value(viewerContextKey{}).(Viewer)
	return viewer
}

type navigationEntry struct {
	Path      string
	Label     string
	Pages     []string
	AnyOf     []string
	AdminOnly bool
}

type navigationLink struct {
	Path    string
	Label   string
	Current bool
}

var navigationEntries = []navigationEntry{
	{Path: "/requests", Label: "Requests", Pages: []string{"requests"}, AnyOf: []string{"view_requests"}},
	{Path: "/bandwidth", Label: "Bandwidth", Pages: []string{"bandwidth"}, AnyOf: []string{"view_bandwidth"}},
	{Path: "/alerts", Label: "Alerts", Pages: []string{"alerts", "alert-edit"}, AnyOf: []string{"view_alerts", "manage_alerts"}},
	{Path: "/archive", Label: "Archive", Pages: []string{"archive"}, AnyOf: []string{"view_archives", "estimate_restores", "manage_restores"}},
	{Path: "/users", Label: "Users", Pages: []string{"users"}, AdminOnly: true},
	{Path: "/projects", Label: "Projects", Pages: []string{"projects"}, AdminOnly: true},
}

type pageData struct {
	AssetVersion string
	Page         string
	Viewer       Viewer
}

func (data pageData) Username() string {
	return data.Viewer.Username
}

func (data pageData) IsAdmin() bool {
	return data.Viewer.IsAdmin
}

func (data pageData) Can(permission string) bool {
	return slices.Contains(data.Viewer.Permissions, permission)
}

func (data pageData) CanAny(permissions ...string) bool {
	return slices.ContainsFunc(permissions, data.Can)
}

func (data pageData) PermissionList() string {
	return strings.Join(data.Viewer.Permissions, " ")
}

func (data pageData) NavigationLinks() []navigationLink {
	return data.visibleLinks(false)
}

func (data pageData) visibleLinks(includeAdminOnly bool) []navigationLink {
	links := []navigationLink{}
	for _, entry := range navigationEntries {
		visible := data.CanAny(entry.AnyOf...)
		if entry.AdminOnly {
			visible = includeAdminOnly && data.Viewer.IsAdmin
		}
		if visible {
			links = append(links, navigationLink{Path: entry.Path, Label: entry.Label, Current: slices.Contains(entry.Pages, data.Page)})
		}
	}
	return links
}

func assetTreeVersion() string {
	treeHash := sha256.New()
	walkError := fs.WalkDir(embeddedFiles, "assets", func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		content, readErr := fs.ReadFile(embeddedFiles, filePath)
		if readErr != nil {
			return readErr
		}
		treeHash.Write([]byte(filePath))
		treeHash.Write([]byte{0})
		treeHash.Write(content)
		return nil
	})
	if walkError != nil {
		panic(walkError)
	}
	return hex.EncodeToString(treeHash.Sum(nil))[:12]
}

func firstLink(viewer Viewer) (navigationLink, bool) {
	links := pageData{Viewer: viewer}.visibleLinks(true)
	if len(links) == 0 {
		return navigationLink{}, false
	}
	return links[0], true
}

func HomePath(viewer Viewer) (string, bool) {
	link, found := firstLink(viewer)
	return link.Path, found
}

func (data pageData) HomePath() string {
	link, _ := firstLink(data.Viewer)
	return link.Path
}

func (data pageData) HomeLabel() string {
	link, _ := firstLink(data.Viewer)
	return link.Label
}

func WriteForbidden(responseWriter http.ResponseWriter, viewer Viewer) {
	var rendered bytes.Buffer
	renderError := pageTemplates.ExecuteTemplate(&rendered, "forbidden.html", pageData{AssetVersion: assetVersion, Page: "forbidden", Viewer: viewer})
	if renderError != nil {
		http.Error(responseWriter, "You don't have permission to open this page.", http.StatusForbidden)
		return
	}
	responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
	responseWriter.WriteHeader(http.StatusForbidden)
	responseWriter.Write(rendered.Bytes())
}

func WriteNotFound(responseWriter http.ResponseWriter) {
	var rendered bytes.Buffer
	renderError := pageTemplates.ExecuteTemplate(&rendered, "not-found.html", pageData{AssetVersion: assetVersion, Page: "not-found"})
	if renderError != nil {
		http.Error(responseWriter, "404 page not found", http.StatusNotFound)
		return
	}
	responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
	responseWriter.WriteHeader(http.StatusNotFound)
	responseWriter.Write(rendered.Bytes())
}

func AssetHandler() http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		relativePath, found := strings.CutPrefix(request.URL.Path, "/assets/"+assetVersion+"/")
		if !found {
			http.NotFound(responseWriter, request)
			return
		}
		content, err := fs.ReadFile(embeddedFiles, "assets/"+relativePath)
		if err != nil {
			http.NotFound(responseWriter, request)
			return
		}
		if path.Ext(relativePath) == ".woff2" {
			responseWriter.Header().Set("Content-Type", "font/woff2")
		}
		responseWriter.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeContent(responseWriter, request, relativePath, time.Time{}, bytes.NewReader(content))
	}
}

func PageHandler(logger *slog.Logger, pageName string) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
		err := pageTemplates.ExecuteTemplate(responseWriter, pageName+".html", pageData{AssetVersion: assetVersion, Page: pageName, Viewer: ViewerFromContext(request.Context())})
		if err != nil {
			logger.Warn("page render failed", "page", pageName, "error", err)
		}
	}
}
