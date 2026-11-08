package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckHealthSucceedsOnOk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := checkHealth(server.URL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckHealthFailsOnUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	if err := checkHealth(server.URL); err == nil {
		t.Fatal("expected error for 503")
	}
}

func TestCheckHealthFailsWhenServerIsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {}))
	closedURL := server.URL
	server.Close()

	if err := checkHealth(closedURL); err == nil {
		t.Fatal("expected error for closed server")
	}
}
