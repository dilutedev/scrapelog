package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
)

type fakePinger struct {
	pingError        error
	receivedDeadline bool
}

func (pinger *fakePinger) Ping(ctx context.Context) error {
	_, pinger.receivedDeadline = ctx.Deadline()
	return pinger.pingError
}

func requestHealth(pinger *fakePinger) *httptest.ResponseRecorder {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	recorder := httptest.NewRecorder()
	NewHandler(logger, pinger, &fakeKeyStore{}, &fakeRefresher{}, &fakeRecordWriter{}, &fakeStorageReporter{}, &fakeDiskGuard{}, config.Config{AdminToken: "admin-token"}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	return recorder
}

func TestHealthReturnsOkWhenPingSucceeds(t *testing.T) {
	recorder := requestHealth(&fakePinger{})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.Code)
	}
	if recorder.Body.String() != `{"status":"ok"}` {
		t.Fatalf("unexpected body %q", recorder.Body.String())
	}
}

func TestHealthReturnsUnavailableWhenPingFails(t *testing.T) {
	recorder := requestHealth(&fakePinger{pingError: errors.New("connection refused")})

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", recorder.Code)
	}
}

func TestHealthPingContextHasDeadline(t *testing.T) {
	pinger := &fakePinger{}
	requestHealth(pinger)

	if !pinger.receivedDeadline {
		t.Fatal("ping context has no deadline")
	}
}
