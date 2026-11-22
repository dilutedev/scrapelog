package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/archive"
	"github.com/dilutedev/scrapelog/ingest-api/internal/bodies"
	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
)

type fakeArchiveLister struct {
	archivedDays []archive.ArchivedDay
	err          error
	calls        int
}

func (lister *fakeArchiveLister) List(ctx context.Context) ([]archive.ArchivedDay, error) {
	lister.calls++
	return lister.archivedDays, lister.err
}

type fakeArchiveDownloader struct {
	content string
	err     error
	keys    []string
}

func (downloader *fakeArchiveDownloader) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	downloader.keys = append(downloader.keys, key)
	if downloader.err != nil {
		return nil, 0, downloader.err
	}
	return io.NopCloser(strings.NewReader(downloader.content)), int64(len(downloader.content)), nil
}

func newArchivesHandler(archivingEnabled bool, archiveLister ArchiveLister, archiveDownloader ArchiveDownloader) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewHandler(context.Background(), logger, &fakePinger{}, &fakeKeyStore{}, &fakeRefresher{}, &fakeRecordWriter{}, &fakeBodyStore{}, &fakeStorageReporter{}, &fakeDiskGuard{}, &fakeRequestReader{}, nil, &fakeAlertStore{}, &fakeRefresher{}, nil, emptyAlertMatcher(), &fakeAlertQueue{}, archivingEnabled, archiveLister, archiveDownloader, nil, &fakeUserDirectory{}, config.Config{AdminToken: testAdminToken})
}

func sampleArchivedDays() []archive.ArchivedDay {
	return []archive.ArchivedDay{{
		Day:        time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
		ObjectKey:  "archive/requests/2026-11-10.parquet",
		RowCount:   80000,
		Partial:    true,
		ArchivedAt: time.Date(2026, 11, 12, 0, 5, 11, 123000000, time.UTC),
	}}
}

func TestArchiveRoutesRequireSession(t *testing.T) {
	for _, path := range []string{"/ui/api/archives", "/ui/api/archives/2026-11-10/download"} {
		t.Run(path, func(t *testing.T) {
			recorder := serve(newArchivesHandler(true, &fakeArchiveLister{}, &fakeArchiveDownloader{}), httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401", recorder.Code)
			}
		})
	}
}

func TestListArchivesReturnsManifestWithoutObjectKey(t *testing.T) {
	handler := newArchivesHandler(true, &fakeArchiveLister{archivedDays: sampleArchivedDays()}, &fakeArchiveDownloader{})

	recorder := getWithSession(handler, "/ui/api/archives")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "archive/requests") {
		t.Fatalf("object key leaked: %s", recorder.Body.String())
	}
	var response archivesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !response.ArchivingEnabled || len(response.Archives) != 1 {
		t.Fatalf("unexpected response: %+v", response)
	}
	archived := response.Archives[0]
	if archived.Day != "2026-11-10" || archived.RowCount != 80000 || !archived.Partial || !archived.ArchivedAt.Equal(sampleArchivedDays()[0].ArchivedAt) {
		t.Fatalf("unexpected archive: %+v", archived)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
}

func TestListArchivesWhenDisabledReturnsEmptyListWithoutQuerying(t *testing.T) {
	archiveLister := &fakeArchiveLister{archivedDays: sampleArchivedDays()}
	handler := newArchivesHandler(false, archiveLister, nil)

	recorder := getWithSession(handler, "/ui/api/archives")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d", recorder.Code)
	}
	if strings.TrimSpace(recorder.Body.String()) != `{"archiving_enabled":false,"archives":[]}` {
		t.Fatalf("unexpected body: %s", recorder.Body.String())
	}
	if archiveLister.calls != 0 {
		t.Fatal("lister was called")
	}
}

func TestListArchivesReturnsEmptyArrayForEmptyManifest(t *testing.T) {
	recorder := getWithSession(newArchivesHandler(true, &fakeArchiveLister{}, nil), "/ui/api/archives")
	if strings.TrimSpace(recorder.Body.String()) != `{"archiving_enabled":true,"archives":[]}` {
		t.Fatalf("unexpected body: %s", recorder.Body.String())
	}
}

func TestListArchivesReturns500WhenListerFails(t *testing.T) {
	handler := newArchivesHandler(true, &fakeArchiveLister{err: errors.New("boom")}, nil)

	recorder := getWithSession(handler, "/ui/api/archives")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", recorder.Code)
	}
}

func TestDownloadArchiveRejectsBadDaysWithoutOpening(t *testing.T) {
	for _, day := range []string{"..%2Fx", "2026-13-01", "2026-1-5", "abc", "2026-11-10%2F..%2F..", "20261110"} {
		t.Run(day, func(t *testing.T) {
			archiveDownloader := &fakeArchiveDownloader{}
			handler := newArchivesHandler(true, &fakeArchiveLister{archivedDays: sampleArchivedDays()}, archiveDownloader)

			recorder := getWithSession(handler, "/ui/api/archives/"+day+"/download")
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", recorder.Code)
			}
			if len(archiveDownloader.keys) != 0 {
				t.Fatal("downloader was called")
			}
		})
	}
}

func TestDownloadArchiveUnknownDayReturns404WithoutOpening(t *testing.T) {
	archiveDownloader := &fakeArchiveDownloader{}
	handler := newArchivesHandler(true, &fakeArchiveLister{archivedDays: sampleArchivedDays()}, archiveDownloader)

	recorder := getWithSession(handler, "/ui/api/archives/2026-11-11/download")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", recorder.Code)
	}
	if len(archiveDownloader.keys) != 0 {
		t.Fatal("downloader was called")
	}
}

func TestDownloadArchiveWhenDisabledReturns404WithoutOpening(t *testing.T) {
	archiveDownloader := &fakeArchiveDownloader{}
	archiveLister := &fakeArchiveLister{archivedDays: sampleArchivedDays()}
	handler := newArchivesHandler(false, archiveLister, archiveDownloader)

	recorder := getWithSession(handler, "/ui/api/archives/2026-11-10/download")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", recorder.Code)
	}
	if len(archiveDownloader.keys) != 0 || archiveLister.calls != 0 {
		t.Fatal("lister or downloader was called")
	}
}

func TestDownloadArchiveStreamsManifestObject(t *testing.T) {
	archiveDownloader := &fakeArchiveDownloader{content: "PAR1-bytes"}
	handler := newArchivesHandler(true, &fakeArchiveLister{archivedDays: sampleArchivedDays()}, archiveDownloader)

	recorder := getWithSession(handler, "/ui/api/archives/2026-11-10/download")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "application/vnd.apache.parquet" {
		t.Fatalf("content type %q", recorder.Header().Get("Content-Type"))
	}
	if recorder.Header().Get("Content-Disposition") != `attachment; filename="scrapelog-requests-2026-11-10.parquet"` {
		t.Fatalf("content disposition %q", recorder.Header().Get("Content-Disposition"))
	}
	if recorder.Body.String() != "PAR1-bytes" {
		t.Fatalf("unexpected body %q", recorder.Body.String())
	}
	if len(archiveDownloader.keys) != 1 || archiveDownloader.keys[0] != "archive/requests/2026-11-10.parquet" {
		t.Fatalf("unexpected keys %v", archiveDownloader.keys)
	}
}

func TestDownloadArchiveMissingFileReturns404WithoutAttachmentHeaders(t *testing.T) {
	archiveDownloader := &fakeArchiveDownloader{err: bodies.ErrBodyNotFound}
	handler := newArchivesHandler(true, &fakeArchiveLister{archivedDays: sampleArchivedDays()}, archiveDownloader)

	recorder := getWithSession(handler, "/ui/api/archives/2026-11-10/download")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", recorder.Code)
	}
	if recorder.Header().Get("Content-Disposition") != "" {
		t.Fatal("attachment header set")
	}
}

func TestDownloadArchiveReturns502WhenOpenFails(t *testing.T) {
	handler := newArchivesHandler(true, &fakeArchiveLister{archivedDays: sampleArchivedDays()}, &fakeArchiveDownloader{err: errors.New("s3 down")})

	recorder := getWithSession(handler, "/ui/api/archives/2026-11-10/download")
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", recorder.Code)
	}
}
