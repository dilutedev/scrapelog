package ui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed assets pages
var embeddedFiles embed.FS

var assetVersion = assetTreeVersion()
var pageTemplates = template.Must(template.ParseFS(embeddedFiles, "pages/*.html"))

type pageData struct {
	AssetVersion string
	Page         string
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
		err := pageTemplates.ExecuteTemplate(responseWriter, pageName+".html", pageData{AssetVersion: assetVersion, Page: pageName})
		if err != nil {
			logger.Warn("page render failed", "page", pageName, "error", err)
		}
	}
}
