package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKeysCreateCallsAdminAPI(t *testing.T) {
	var receivedMethod, receivedPath, receivedAuthorization, receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		receivedMethod = request.Method
		receivedPath = request.URL.Path
		receivedAuthorization = request.Header.Get("Authorization")
		bodyBytes, _ := io.ReadAll(request.Body)
		receivedBody = string(bodyBytes)
		responseWriter.WriteHeader(http.StatusCreated)
		responseWriter.Write([]byte(`{"key_id":"sl_x"}`))
	}))
	defer server.Close()
	var output bytes.Buffer

	err := runKeysCommand([]string{"create", "--project", "p", "--name", "n"}, server.URL, "admin-token", &output)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedMethod != http.MethodPost || receivedPath != "/v1/admin/keys" {
		t.Fatalf("server saw %s %s", receivedMethod, receivedPath)
	}
	if receivedAuthorization != "Bearer admin-token" {
		t.Fatalf("authorization %q", receivedAuthorization)
	}
	if receivedBody != `{"name":"n","project":"p"}` {
		t.Fatalf("body %q", receivedBody)
	}
	if output.String() != `{"key_id":"sl_x"}` {
		t.Fatalf("output %q", output.String())
	}
}

func TestKeysListCallsAdminAPI(t *testing.T) {
	var receivedMethod, receivedPath string
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		receivedMethod = request.Method
		receivedPath = request.URL.Path
		responseWriter.Write([]byte(`[]`))
	}))
	defer server.Close()
	var output bytes.Buffer

	if err := runKeysCommand([]string{"list"}, server.URL, "admin-token", &output); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedMethod != http.MethodGet || receivedPath != "/v1/admin/keys" || output.String() != "[]" {
		t.Fatalf("server saw %s %s, output %q", receivedMethod, receivedPath, output.String())
	}
}

func TestKeysRevokeCallsAdminAPI(t *testing.T) {
	var receivedMethod, receivedPath string
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		receivedMethod = request.Method
		receivedPath = request.URL.Path
		responseWriter.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var output bytes.Buffer

	if err := runKeysCommand([]string{"revoke", "sl_x"}, server.URL, "admin-token", &output); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedMethod != http.MethodDelete || receivedPath != "/v1/admin/keys/sl_x" || output.Len() != 0 {
		t.Fatalf("server saw %s %s, output %q", receivedMethod, receivedPath, output.String())
	}
}

func TestKeysReportsServerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusNotFound)
		responseWriter.Write([]byte(`{"error":"api key not found"}`))
	}))
	defer server.Close()

	err := runKeysCommand([]string{"revoke", "sl_x"}, server.URL, "admin-token", io.Discard)

	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "api key not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestKeysRejectsBadUsage(t *testing.T) {
	for name, arguments := range map[string][]string{
		"no arguments":    {},
		"unknown":         {"frobnicate"},
		"revoke no id":    {"revoke"},
		"revoke two ids":  {"revoke", "a", "b"},
		"create bad flag": {"create", "--bogus"},
	} {
		if err := runKeysCommand(arguments, "http://unused", "admin-token", io.Discard); !errors.Is(err, errUsage) {
			t.Errorf("%s: got %v, want usage error", name, err)
		}
	}
}
