package bodies

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestS3UploaderPutsGzippedBodyPathStyleWithSignature(t *testing.T) {
	var requestMethod, requestPath, authorization string
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestMethod = request.Method
		requestPath = request.URL.Path
		authorization = request.Header.Get("Authorization")
		requestBody, _ = io.ReadAll(request.Body)
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	uploader := NewS3Uploader(server.URL, "scrapelog", "auto", "key-id", "key-secret")
	gzippedBody, err := gzipBytes([]byte("payload"))
	if err != nil {
		t.Fatal(err)
	}

	if err := uploader.Put(context.Background(), "bodies/acme/2026/11/08/req-1.gz", gzippedBody); err != nil {
		t.Fatalf("put failed: %v", err)
	}

	if requestMethod != http.MethodPut || requestPath != "/scrapelog/bodies/acme/2026/11/08/req-1.gz" {
		t.Fatalf("unexpected request %s %s", requestMethod, requestPath)
	}
	if !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 Credential=key-id/") {
		t.Fatalf("unexpected authorization header %q", authorization)
	}
	if gunzip(t, requestBody) != "payload" {
		t.Fatal("uploaded bytes are not the gzipped body")
	}
}

func TestS3UploaderReturnsErrorOnServerFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	uploader := NewS3Uploader(server.URL, "scrapelog", "auto", "key-id", "key-secret")

	if err := uploader.Put(context.Background(), "key", []byte("x")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestS3UploaderGetsObject(t *testing.T) {
	var requestMethod, requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestMethod = request.Method
		requestPath = request.URL.Path
		responseWriter.Write([]byte("stored bytes"))
	}))
	defer server.Close()
	uploader := NewS3Uploader(server.URL, "scrapelog", "auto", "key-id", "key-secret")

	storedBody, err := uploader.Get(context.Background(), "bodies/acme/req-1.gz")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	if requestMethod != http.MethodGet || requestPath != "/scrapelog/bodies/acme/req-1.gz" {
		t.Fatalf("unexpected request %s %s", requestMethod, requestPath)
	}
	if string(storedBody) != "stored bytes" {
		t.Fatalf("got %q", storedBody)
	}
}

func TestS3UploaderGetReturnsNotFoundForMissingKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/xml")
		responseWriter.WriteHeader(http.StatusNotFound)
		responseWriter.Write([]byte("<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>"))
	}))
	defer server.Close()
	uploader := NewS3Uploader(server.URL, "scrapelog", "auto", "key-id", "key-secret")

	_, err := uploader.Get(context.Background(), "bodies/missing.gz")

	if !errors.Is(err, ErrBodyNotFound) {
		t.Fatalf("expected ErrBodyNotFound, got %v", err)
	}
}
