package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
)

func requestDocs(docsRequest *http.Request) *httptest.ResponseRecorder {
	return requestDocsWithConfig(docsRequest, config.Config{AdminToken: "admin-token"})
}

func requestDocsWithConfig(docsRequest *http.Request, loadedConfig config.Config) *httptest.ResponseRecorder {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	recorder := httptest.NewRecorder()
	NewHandler(context.Background(), logger, &fakePinger{}, &fakeKeyStore{}, &fakeRefresher{}, &fakeRecordWriter{}, &fakeBodyStore{}, &fakeStorageReporter{}, &fakeDiskGuard{}, loadedConfig).ServeHTTP(recorder, docsRequest)
	return recorder
}

func TestDocsShowConfiguredLimits(t *testing.T) {
	loadedConfig := config.Config{
		AdminToken:                "admin-token",
		MaxBatchRecords:           1234,
		MaxRequestBytes:           3145728,
		MaxBodyBytes:              524288,
		RateLimitRecordsPerMinute: 98760,
		RedactedHeaders:           map[string]bool{"x-secret-header": true},
		S3Endpoint:                "https://s3.example.com",
	}

	body := requestDocsWithConfig(httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/docs", nil), loadedConfig).Body.String()

	for _, expectedText := range []string{"1234", "3145728", "12582912", "524288", "98760", "1646", "x-secret-header", "Response bodies are stored"} {
		if !strings.Contains(body, expectedText) {
			t.Errorf("docs do not contain %q", expectedText)
		}
	}
}

func TestDocsServesHTMLWithoutAuthentication(t *testing.T) {
	recorder := requestDocs(httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/docs", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("content type %q, want text/html", contentType)
	}
	if !strings.Contains(recorder.Body.String(), "http://127.0.0.1:8080/v1/requests") {
		t.Fatal("docs do not show the batch endpoint under the request base URL")
	}
}

func TestDocsUseForwardedProtoAndPrefix(t *testing.T) {
	docsRequest := httptest.NewRequest(http.MethodGet, "http://click.example.com/docs", nil)
	docsRequest.Header.Set("X-Forwarded-Proto", "https")
	docsRequest.Header.Set("X-Forwarded-Prefix", "/api")

	body := requestDocs(docsRequest).Body.String()

	if !strings.Contains(body, "https://click.example.com/api/v1/requests") {
		t.Fatal("docs do not use the forwarded scheme and prefix")
	}
}

func TestDocsEscapeForwardedPrefix(t *testing.T) {
	docsRequest := httptest.NewRequest(http.MethodGet, "http://click.example.com/docs", nil)
	docsRequest.Header.Set("X-Forwarded-Prefix", "<script>alert(1)</script>")

	body := requestDocs(docsRequest).Body.String()

	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("forwarded prefix is rendered unescaped")
	}
}
