package bodies

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/ingest"
)

var silentLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeUploader struct {
	mutex    sync.Mutex
	objects  map[string][]byte
	err      error
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	block    chan struct{}
}

func (uploader *fakeUploader) Put(ctx context.Context, key string, gzippedBody []byte) error {
	current := uploader.inFlight.Add(1)
	defer uploader.inFlight.Add(-1)
	for {
		seen := uploader.maxSeen.Load()
		if current <= seen || uploader.maxSeen.CompareAndSwap(seen, current) {
			break
		}
	}
	if uploader.block != nil {
		select {
		case <-uploader.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if uploader.err != nil {
		return uploader.err
	}
	uploader.mutex.Lock()
	defer uploader.mutex.Unlock()
	if uploader.objects == nil {
		uploader.objects = map[string][]byte{}
	}
	uploader.objects[key] = gzippedBody
	return nil
}

func recordWithBody(requestID string, body string, sourceIndex int) ingest.Record {
	return ingest.Record{
		Project:     "acme",
		RequestID:   requestID,
		ReceivedAt:  time.Date(2026, 11, 8, 23, 59, 0, 0, time.UTC),
		Body:        []byte(body),
		SourceIndex: sourceIndex,
	}
}

func gunzip(t *testing.T, compressed []byte) string {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return string(decompressed)
}

func TestStoreUploadsGzippedBodyUnderProjectAndDateKey(t *testing.T) {
	uploader := &fakeUploader{}
	records := []ingest.Record{recordWithBody("req-1", "hello world", 0)}

	warnings := NewStore(uploader, "bodies/", silentLogger).Store(context.Background(), records)

	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	expectedKey := "bodies/acme/2026/11/08/req-1.gz"
	if records[0].BodyKey != expectedKey {
		t.Fatalf("body key %q, want %q", records[0].BodyKey, expectedKey)
	}
	if records[0].Body != nil {
		t.Fatal("body must be cleared after upload")
	}
	if got := gunzip(t, uploader.objects[expectedKey]); got != "hello world" {
		t.Fatalf("stored body %q", got)
	}
}

func TestStoreSanitizesRequestID(t *testing.T) {
	uploader := &fakeUploader{}
	records := []ingest.Record{recordWithBody("../../etc/pass wd?é", "x", 0)}

	NewStore(uploader, "bodies/", silentLogger).Store(context.Background(), records)

	expectedKey := "bodies/acme/2026/11/08/.._.._etc_pass_wd___.gz"
	if records[0].BodyKey != expectedKey {
		t.Fatalf("body key %q, want %q", records[0].BodyKey, expectedKey)
	}
}

func TestStoreReportsUploadFailure(t *testing.T) {
	uploader := &fakeUploader{err: errors.New("connection refused")}
	records := []ingest.Record{recordWithBody("req-1", "x", 4)}

	warnings := NewStore(uploader, "bodies/", silentLogger).Store(context.Background(), records)

	if len(warnings) != 1 || warnings[0] != (Warning{Index: 4, Warning: "body upload failed"}) {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if records[0].BodyKey != "" || records[0].Body != nil {
		t.Fatalf("record must have no body key and no body: %+v", records[0])
	}
}

func TestStoreWithoutUploaderWarnsNotConfigured(t *testing.T) {
	records := []ingest.Record{recordWithBody("req-1", "x", 2), {Project: "acme", SourceIndex: 3}}

	warnings := NewStore(nil, "bodies/", silentLogger).Store(context.Background(), records)

	if len(warnings) != 1 || warnings[0] != (Warning{Index: 2, Warning: "body dropped: S3 is not configured"}) {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if records[0].Body != nil {
		t.Fatal("body must be cleared")
	}
}

func TestStoreReportsDropReasonWithoutUploading(t *testing.T) {
	uploader := &fakeUploader{}
	records := []ingest.Record{{Project: "acme", SourceIndex: 7, BodyDropReason: "body exceeds MAX_BODY_BYTES (16 bytes)"}}

	warnings := NewStore(uploader, "bodies/", silentLogger).Store(context.Background(), records)

	if len(warnings) != 1 || warnings[0] != (Warning{Index: 7, Warning: "body exceeds MAX_BODY_BYTES (16 bytes)"}) {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(uploader.objects) != 0 {
		t.Fatal("nothing must be uploaded")
	}
}

func TestStoreReturnsEmptyNonNilWarningsWhenNoBodies(t *testing.T) {
	warnings := NewStore(&fakeUploader{}, "bodies/", silentLogger).Store(context.Background(), []ingest.Record{{Project: "acme"}})
	if warnings == nil || len(warnings) != 0 {
		t.Fatalf("got %#v, want empty non-nil slice", warnings)
	}
}

func TestStoreBoundsConcurrentUploads(t *testing.T) {
	uploader := &fakeUploader{block: make(chan struct{})}
	records := make([]ingest.Record, 30)
	for index := range records {
		records[index] = recordWithBody(fmt.Sprintf("req-%d", index), "x", index)
	}
	done := make(chan []Warning)
	go func() { done <- NewStore(uploader, "bodies/", silentLogger).Store(context.Background(), records) }()

	deadline := time.Now().Add(5 * time.Second)
	for uploader.inFlight.Load() < maxConcurrentUploads && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(uploader.block)
	warnings := <-done

	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if uploader.maxSeen.Load() != maxConcurrentUploads {
		t.Fatalf("max in flight %d, want %d", uploader.maxSeen.Load(), maxConcurrentUploads)
	}
	for index := range records {
		if records[index].BodyKey == "" {
			t.Fatalf("record %d has no body key", index)
		}
	}
}

func TestStoreReportsTimeoutWhenPhaseDeadlinePasses(t *testing.T) {
	uploader := &fakeUploader{block: make(chan struct{})}
	records := make([]ingest.Record, 12)
	for index := range records {
		records[index] = recordWithBody(fmt.Sprintf("req-%d", index), "x", index+100)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	warnings := NewStore(uploader, "bodies/", silentLogger).Store(ctx, records)

	if len(warnings) != len(records) {
		t.Fatalf("got %d warnings, want %d", len(warnings), len(records))
	}
	for position, warning := range warnings {
		if warning.Warning != "body upload timed out" || warning.Index != position+100 {
			t.Fatalf("unexpected warning at %d: %+v", position, warning)
		}
	}
	for index := range records {
		if records[index].BodyKey != "" || records[index].Body != nil {
			t.Fatalf("record %d must have no body key and no body", index)
		}
	}
}
