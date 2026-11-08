package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/dilutedev/scrapelog/ingest-api/internal/apikeys"
	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/ingest"
	"github.com/dilutedev/scrapelog/ingest-api/internal/storage"
)

type fakeRecordWriter struct {
	received []ingest.Record
	calls    int
	err      error
}

func (writer *fakeRecordWriter) Insert(ctx context.Context, records []ingest.Record) error {
	writer.calls++
	writer.received = records
	return writer.err
}

type failingReader struct {
	t *testing.T
}

func (reader failingReader) Read([]byte) (int, error) {
	reader.t.Fatal("request body was read")
	return 0, io.EOF
}

var testIngestConfig = config.Config{
	AdminToken:      "admin-token",
	MaxBatchRecords: 100,
	MaxRequestBytes: 1 << 20,
	RedactedHeaders: map[string]bool{"authorization": true},

	RateLimitRecordsPerMinute: 1 << 30,
}

func serveIngest(writer *fakeRecordWriter, loadedConfig config.Config, body io.Reader, contentEncoding string) *httptest.ResponseRecorder {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	request := httptest.NewRequest(http.MethodPost, "/v1/requests", body)
	request.Header.Set("Authorization", "Bearer good-key")
	if contentEncoding != "" {
		request.Header.Set("Content-Encoding", contentEncoding)
	}
	recorder := httptest.NewRecorder()
	NewHandler(logger, &fakePinger{}, &fakeKeyStore{}, &fakeRefresher{}, writer, &fakeStorageReporter{}, &fakeDiskGuard{}, loadedConfig).ServeHTTP(recorder, request)
	return recorder
}

type twoKeyRefresher struct {
	fakeRefresher
}

func (refresher *twoKeyRefresher) Authenticate(fullKey string) (apikeys.Identity, bool) {
	if fullKey == "other-key" {
		return apikeys.Identity{KeyID: "otherkeyid", Project: "other"}, true
	}
	return refresher.fakeRefresher.Authenticate(fullKey)
}

func newRateLimitedHandler(writer *fakeRecordWriter, loadedConfig config.Config) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewHandler(logger, &fakePinger{}, &fakeKeyStore{}, &twoKeyRefresher{}, writer, &fakeStorageReporter{}, &fakeDiskGuard{}, loadedConfig)
}

func serveIngestWith(handler http.Handler, apiKey string, body io.Reader) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/requests", body)
	request.Header.Set("Authorization", "Bearer "+apiKey)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func rateLimitConfig() config.Config {
	limitedConfig := testIngestConfig
	limitedConfig.RateLimitRecordsPerMinute = int64(limitedConfig.MaxBatchRecords)
	return limitedConfig
}

func requireRateLimited(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429, body %s", recorder.Code, recorder.Body.String())
	}
	retryAfterSeconds, err := strconv.Atoi(recorder.Header().Get("Retry-After"))
	if err != nil || retryAfterSeconds < 1 {
		t.Fatalf("Retry-After %q is not an integer >= 1", recorder.Header().Get("Retry-After"))
	}
}

func TestIngestRateLimitFullBatchPassesOnFreshKey(t *testing.T) {
	writer := &fakeRecordWriter{}
	handler := newRateLimitedHandler(writer, rateLimitConfig())

	recorder := serveIngestWith(handler, "good-key", strings.NewReader(recordsJSON(100)))
	if recorder.Code != http.StatusOK || len(writer.received) != 100 {
		t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
	}
}

func TestIngestRateLimitEmptyBucketRejectsWithoutReadingBody(t *testing.T) {
	writer := &fakeRecordWriter{}
	handler := newRateLimitedHandler(writer, rateLimitConfig())
	serveIngestWith(handler, "good-key", strings.NewReader(recordsJSON(100)))

	recorder := serveIngestWith(handler, "good-key", failingReader{t: t})
	requireRateLimited(t, recorder)
	if writer.calls != 1 {
		t.Fatalf("writer called %d times, want 1", writer.calls)
	}
}

func TestIngestRateLimitBatchAboveRemainingTokensIsRejectedWithoutSpending(t *testing.T) {
	writer := &fakeRecordWriter{}
	handler := newRateLimitedHandler(writer, rateLimitConfig())
	serveIngestWith(handler, "good-key", strings.NewReader(recordsJSON(70)))

	recorder := serveIngestWith(handler, "good-key", strings.NewReader(recordsJSON(31)))
	requireRateLimited(t, recorder)
	if writer.calls != 1 {
		t.Fatalf("writer called %d times, want 1", writer.calls)
	}

	recorder = serveIngestWith(handler, "good-key", strings.NewReader(recordsJSON(30)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("remaining tokens were spent by the rejected batch: status %d", recorder.Code)
	}
}

func TestIngestRateLimitDoesNotChargeInvalidRecords(t *testing.T) {
	writer := &fakeRecordWriter{}
	handler := newRateLimitedHandler(writer, rateLimitConfig())

	recorder := serveIngestWith(handler, "good-key", strings.NewReader(`[{"method":"GET"},{"method":"POST"}]`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", recorder.Code)
	}
	recorder = serveIngestWith(handler, "good-key", strings.NewReader(recordsJSON(100, 0, 1)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
	}
	recorder = serveIngestWith(handler, "good-key", strings.NewReader(recordsJSON(2)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("2 invalid records should have cost nothing: status %d", recorder.Code)
	}
}

func TestIngestRateLimitIsPerKey(t *testing.T) {
	writer := &fakeRecordWriter{}
	handler := newRateLimitedHandler(writer, rateLimitConfig())
	serveIngestWith(handler, "good-key", strings.NewReader(recordsJSON(100)))
	requireRateLimited(t, serveIngestWith(handler, "good-key", strings.NewReader(recordsJSON(1))))

	recorder := serveIngestWith(handler, "other-key", strings.NewReader(recordsJSON(100)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("other key status %d body %s", recorder.Code, recorder.Body.String())
	}
}

func recordsJSON(count int, invalidIndexes ...int) string {
	invalid := map[int]bool{}
	for _, index := range invalidIndexes {
		invalid[index] = true
	}
	elements := make([]string, count)
	for index := range elements {
		if invalid[index] {
			elements[index] = `{"method":"GET"}`
		} else {
			elements[index] = fmt.Sprintf(`{"url":"https://example.com/%d","method":"GET","status":200,"project":"other"}`, index)
		}
	}
	return "[" + strings.Join(elements, ",") + "]"
}

func decodeSummary(t *testing.T, recorder *httptest.ResponseRecorder) ingestSummary {
	t.Helper()
	var summary ingestSummary
	if err := json.Unmarshal(recorder.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decoding %q: %v", recorder.Body.String(), err)
	}
	return summary
}

func gzipped(t *testing.T, content string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	gzipWriter.Write([]byte(content))
	gzipWriter.Close()
	return buffer.Bytes()
}

func zstded(t *testing.T, content string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	zstdWriter, err := zstd.NewWriter(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	zstdWriter.Write([]byte(content))
	zstdWriter.Close()
	return buffer.Bytes()
}

func TestIngestAcceptsValidRecordsAndReportsInvalidOnes(t *testing.T) {
	writer := &fakeRecordWriter{}

	recorder := serveIngest(writer, testIngestConfig, strings.NewReader(recordsJSON(100, 17, 63)), "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	summary := decodeSummary(t, recorder)
	if summary.Accepted != 98 || summary.Rejected != 2 || len(summary.Errors) != 2 {
		t.Fatalf("unexpected summary %+v", summary)
	}
	if summary.Errors[0].Index != 17 || summary.Errors[0].Error != "url is required" || summary.Errors[1].Index != 63 {
		t.Fatalf("unexpected errors %+v", summary.Errors)
	}
	if len(writer.received) != 98 {
		t.Fatalf("writer received %d records", len(writer.received))
	}
	for _, record := range writer.received {
		if record.Project != "acme" {
			t.Fatalf("record project %q, want acme", record.Project)
		}
	}
}

func TestIngestRejectsBatchWithNoValidRecords(t *testing.T) {
	for name, body := range map[string]string{"all invalid": recordsJSON(2, 0, 1), "empty array": "[]"} {
		writer := &fakeRecordWriter{}

		recorder := serveIngest(writer, testIngestConfig, strings.NewReader(body), "")

		summary := decodeSummary(t, recorder)
		if recorder.Code != http.StatusBadRequest || summary.Accepted != 0 || writer.calls != 0 {
			t.Errorf("%s: status %d, summary %+v, writer calls %d", name, recorder.Code, summary, writer.calls)
		}
		if !strings.Contains(recorder.Body.String(), `"errors":[`) {
			t.Errorf("%s: errors must be an array, got %s", name, recorder.Body.String())
		}
	}
}

func TestIngestRejectsSingleObjectAndMalformedJSON(t *testing.T) {
	tests := map[string]struct {
		body    string
		message string
	}{
		"single object": {`{"url":"https://a.com","method":"GET"}`, "body must be a JSON array of records; wrap a single record in [ ]"},
		"malformed":     {`[{"url":`, "invalid request body"},
		"empty body":    {``, "invalid request body"},
	}
	for name, test := range tests {
		recorder := serveIngest(&fakeRecordWriter{}, testIngestConfig, strings.NewReader(test.body), "")

		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), test.message) {
			t.Errorf("%s: status %d body %s", name, recorder.Code, recorder.Body.String())
		}
	}
}

func TestIngestEnforcesSizeAndCountLimits(t *testing.T) {
	smallConfig := testIngestConfig
	smallConfig.MaxRequestBytes = 1000

	tests := map[string]struct {
		config  config.Config
		body    []byte
		encoded string
	}{
		"wire body over limit":      {smallConfig, []byte("[" + strings.Repeat(" ", 1000) + "]"), ""},
		"decoded body over 4x":      {smallConfig, gzipped(t, "["+strings.Repeat(" ", 4100)+"]"), "gzip"},
		"too many records":          {testIngestConfig, []byte(recordsJSON(101)), ""},
		"zstd decoded body over 4x": {smallConfig, zstded(t, "["+strings.Repeat(" ", 4100)+"]"), "zstd"},
	}
	for name, test := range tests {
		writer := &fakeRecordWriter{}

		recorder := serveIngest(writer, test.config, bytes.NewReader(test.body), test.encoded)

		if recorder.Code != http.StatusRequestEntityTooLarge || writer.calls != 0 {
			t.Errorf("%s: status %d body %s writer calls %d", name, recorder.Code, recorder.Body.String(), writer.calls)
		}
	}
}

func TestIngestAcceptsExactLimits(t *testing.T) {
	writer := &fakeRecordWriter{}

	recorder := serveIngest(writer, testIngestConfig, strings.NewReader(recordsJSON(100)), "")
	if recorder.Code != http.StatusOK || len(writer.received) != 100 {
		t.Fatalf("exactly MaxBatchRecords: status %d body %s", recorder.Code, recorder.Body.String())
	}

	body := recordsJSON(1)
	exactConfig := testIngestConfig
	exactConfig.MaxRequestBytes = int64(len(body))
	recorder = serveIngest(&fakeRecordWriter{}, exactConfig, strings.NewReader(body), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("exactly MaxRequestBytes: status %d body %s", recorder.Code, recorder.Body.String())
	}
}

func TestIngestDecodesCompressedBodies(t *testing.T) {
	body := recordsJSON(5)
	for name, test := range map[string]struct {
		compressed []byte
		encoding   string
	}{
		"gzip":           {gzipped(t, body), "gzip"},
		"zstd":           {zstded(t, body), "zstd"},
		"uppercase gzip": {gzipped(t, body), "GZIP"},
		"identity":       {[]byte(body), "identity"},
	} {
		writer := &fakeRecordWriter{}

		recorder := serveIngest(writer, testIngestConfig, bytes.NewReader(test.compressed), test.encoding)

		if recorder.Code != http.StatusOK || len(writer.received) != 5 {
			t.Errorf("%s: status %d body %s", name, recorder.Code, recorder.Body.String())
		}
	}
}

func TestIngestRejectsUnsupportedAndCorruptEncodings(t *testing.T) {
	recorder := serveIngest(&fakeRecordWriter{}, testIngestConfig, strings.NewReader(recordsJSON(1)), "br")
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Errorf("br: status %d", recorder.Code)
	}

	recorder = serveIngest(&fakeRecordWriter{}, testIngestConfig, strings.NewReader("not gzip"), "gzip")
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid gzip body") {
		t.Errorf("corrupt gzip: status %d body %s", recorder.Code, recorder.Body.String())
	}

	truncated := gzipped(t, recordsJSON(50))
	recorder = serveIngest(&fakeRecordWriter{}, testIngestConfig, bytes.NewReader(truncated[:len(truncated)/2]), "gzip")
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("truncated gzip: status %d", recorder.Code)
	}
}

func TestIngestReturnsUnavailableWhenInsertFails(t *testing.T) {
	writer := &fakeRecordWriter{err: errors.New("connection refused")}

	recorder := serveIngest(writer, testIngestConfig, strings.NewReader(recordsJSON(3)), "")

	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") != "5" {
		t.Fatalf("status %d, retry-after %q", recorder.Code, recorder.Header().Get("Retry-After"))
	}
	if strings.Contains(recorder.Body.String(), "accepted") {
		t.Fatalf("503 must not report accepted records: %s", recorder.Body.String())
	}
}

func TestIngestInsertContextHasDeadline(t *testing.T) {
	deadlineWriter := &deadlineRecordWriter{}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	request := httptest.NewRequest(http.MethodPost, "/v1/requests", strings.NewReader(recordsJSON(1)))
	request.Header.Set("Authorization", "Bearer good-key")

	NewHandler(logger, &fakePinger{}, &fakeKeyStore{}, &fakeRefresher{}, deadlineWriter, &fakeStorageReporter{}, &fakeDiskGuard{}, testIngestConfig).ServeHTTP(httptest.NewRecorder(), request)

	if !deadlineWriter.hadDeadline {
		t.Fatal("insert context has no deadline")
	}
}

type deadlineRecordWriter struct {
	hadDeadline bool
}

func (writer *deadlineRecordWriter) Insert(ctx context.Context, records []ingest.Record) error {
	_, writer.hadDeadline = ctx.Deadline()
	return nil
}

func TestIngestRequiresAPIKeyBeforeReadingBody(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	for name, authorization := range map[string]string{"missing": "", "wrong key": "Bearer bad-key"} {
		request := httptest.NewRequest(http.MethodPost, "/v1/requests", failingReader{t})
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		recorder := httptest.NewRecorder()

		NewHandler(logger, &fakePinger{}, &fakeKeyStore{}, &fakeRefresher{}, &fakeRecordWriter{}, &fakeStorageReporter{}, &fakeDiskGuard{}, testIngestConfig).ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, recorder.Code)
		}
	}
}

func newGuardedHandler(writer *fakeRecordWriter, guard *fakeDiskGuard, logOutput io.Writer, loadedConfig config.Config) http.Handler {
	logger := slog.New(slog.NewJSONHandler(logOutput, nil))
	return NewHandler(logger, &fakePinger{}, &fakeKeyStore{}, &fakeRefresher{}, writer, &fakeStorageReporter{}, guard, loadedConfig)
}

func TestIngestOverBudgetRejectsWithoutReadingBodyOrSpendingTokens(t *testing.T) {
	writer := &fakeRecordWriter{}
	guard := &fakeDiskGuard{usage: storage.Usage{UsedBytes: 1001, BudgetBytes: 1000}}
	handler := newGuardedHandler(writer, guard, io.Discard, rateLimitConfig())

	recorder := serveIngestWith(handler, "good-key", failingReader{t: t})

	if recorder.Code != http.StatusInsufficientStorage {
		t.Fatalf("status %d, want 507, body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != "{\"error\":\"storage budget exceeded\"}\n" {
		t.Fatalf("body %q", recorder.Body.String())
	}
	if recorder.Header().Get("Retry-After") != "" {
		t.Fatalf("unexpected Retry-After %q", recorder.Header().Get("Retry-After"))
	}
	if writer.calls != 0 {
		t.Fatalf("writer called %d times, want 0", writer.calls)
	}

	guard.usage = storage.Usage{UsedBytes: 10, BudgetBytes: 1000}
	recorder = serveIngestWith(handler, "good-key", strings.NewReader(recordsJSON(100)))
	if recorder.Code != http.StatusOK || len(writer.received) != 100 {
		t.Fatalf("after recovery: status %d body %s", recorder.Code, recorder.Body.String())
	}
}

func TestIngestNearBudgetInsertsAndLogsWarningOnce(t *testing.T) {
	writer := &fakeRecordWriter{}
	guard := &fakeDiskGuard{usage: storage.Usage{UsedBytes: 950, BudgetBytes: 1000}}
	var logOutput bytes.Buffer
	handler := newGuardedHandler(writer, guard, &logOutput, testIngestConfig)

	recorder := serveIngestWith(handler, "good-key", strings.NewReader(recordsJSON(3)))

	if recorder.Code != http.StatusOK || writer.calls != 1 {
		t.Fatalf("status %d, writer calls %d", recorder.Code, writer.calls)
	}
	if count := strings.Count(logOutput.String(), "clickhouse disk usage above 90% of budget"); count != 1 {
		t.Fatalf("warning logged %d times, want 1; log %s", count, logOutput.String())
	}
}
