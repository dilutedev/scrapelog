package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/ingest"
	"github.com/dilutedev/scrapelog/ingest-api/internal/ratelimit"
	"github.com/dilutedev/scrapelog/ingest-api/internal/storage"
)

const streamTestTimeout = 5 * time.Second

type chunkRecordingWriter struct {
	mutex       sync.Mutex
	chunks      [][]ingest.Record
	failOnCall  int
	afterInsert func()
	inserted    chan int
}

func newChunkRecordingWriter() *chunkRecordingWriter {
	return &chunkRecordingWriter{inserted: make(chan int, 1000)}
}

func (writer *chunkRecordingWriter) Insert(ctx context.Context, records []ingest.Record) error {
	writer.mutex.Lock()
	writer.chunks = append(writer.chunks, records)
	callNumber := len(writer.chunks)
	writer.mutex.Unlock()
	if writer.afterInsert != nil {
		writer.afterInsert()
	}
	writer.inserted <- len(records)
	if callNumber == writer.failOnCall {
		return errors.New("insert failed")
	}
	return nil
}

func (writer *chunkRecordingWriter) chunkSizes() []int {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	sizes := make([]int, len(writer.chunks))
	for index, chunk := range writer.chunks {
		sizes[index] = len(chunk)
	}
	return sizes
}

type switchableDiskGuard struct {
	overBudget atomic.Bool
}

func (guard *switchableDiskGuard) Usage() storage.Usage {
	if guard.overBudget.Load() {
		return storage.Usage{UsedBytes: 1001, BudgetBytes: 1000}
	}
	return storage.Usage{BudgetBytes: 1000}
}

func streamConfig(maxBatchRecords int) config.Config {
	streamTestConfig := testIngestConfig
	streamTestConfig.MaxBatchRecords = maxBatchRecords
	return streamTestConfig
}

func newStreamHandler(writer RecordWriter, guard DiskGuard, loadedConfig config.Config, shutdownContext context.Context, flushInterval time.Duration) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	recordLimiter := ratelimit.New(loadedConfig.RateLimitRecordsPerMinute, time.Now)
	return requireAPIKey(&fakeRefresher{}, streamRequestsHandler(logger, writer, recordLimiter, guard, shutdownContext, loadedConfig, flushInterval))
}

func serveStream(handler http.Handler, body io.Reader, contentEncoding string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/requests/stream", body)
	request.Header.Set("Authorization", "Bearer good-key")
	if contentEncoding != "" {
		request.Header.Set("Content-Encoding", contentEncoding)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func serveStreamSimple(writer RecordWriter, loadedConfig config.Config, body string) *httptest.ResponseRecorder {
	handler := newStreamHandler(writer, &fakeDiskGuard{}, loadedConfig, context.Background(), time.Hour)
	return serveStream(handler, strings.NewReader(body), "")
}

func decodeStreamResponse(t *testing.T, body []byte) streamResponse {
	t.Helper()
	var response streamResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}
	return response
}

func ndjsonLines(count int) string {
	var lines strings.Builder
	for index := 0; index < count; index++ {
		fmt.Fprintf(&lines, "{\"url\":\"https://example.com/%d\",\"method\":\"GET\",\"status\":200}\n", index)
	}
	return lines.String()
}

func paddedLine(totalBytes int) string {
	base := `{"url":"https://example.com/","method":"GET"}`
	padded := `{"url":"https://example.com/` + strings.Repeat("a", totalBytes-len(base)) + `","method":"GET"}`
	return padded
}

func requireStatus(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int) streamResponse {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status %d, want %d, body %s", recorder.Code, wantStatus, recorder.Body.String())
	}
	return decodeStreamResponse(t, recorder.Body.Bytes())
}

func requireChunkSizes(t *testing.T, writer *chunkRecordingWriter, wantSizes ...int) {
	t.Helper()
	gotSizes := writer.chunkSizes()
	if fmt.Sprint(gotSizes) != fmt.Sprint(wantSizes) {
		t.Fatalf("insert sizes %v, want %v", gotSizes, wantSizes)
	}
}

func waitForInsert(t *testing.T, writer *chunkRecordingWriter) int {
	t.Helper()
	select {
	case size := <-writer.inserted:
		return size
	case <-time.After(streamTestTimeout):
		t.Fatal("timed out waiting for insert")
		return 0
	}
}

func TestStreamAcceptsTenThousandLinesInChunksOfTwoThousand(t *testing.T) {
	writer := newChunkRecordingWriter()

	recorder := serveStreamSimple(writer, streamConfig(5000), ndjsonLines(10000))

	response := requireStatus(t, recorder, http.StatusOK)
	if response.Accepted != 10000 || response.Rejected != 0 || len(response.Errors) != 0 {
		t.Fatalf("response %+v", response)
	}
	if recorder.Body.String() != "{\"accepted\":10000,\"rejected\":0,\"errors\":[]}\n" {
		t.Fatalf("body %q", recorder.Body.String())
	}
	requireChunkSizes(t, writer, 2000, 2000, 2000, 2000, 2000)
}

func TestStreamInvalidLinesAreRejectedAndStreamContinues(t *testing.T) {
	writer := newChunkRecordingWriter()
	valid := `{"url":"https://example.com/","method":"GET"}`
	body := strings.Join([]string{valid, `{not json`, valid, "", "  ", `{"method":"GET"}`, valid}, "\r\n") + "\r\n"

	recorder := serveStreamSimple(writer, streamConfig(100), body)

	response := requireStatus(t, recorder, http.StatusOK)
	if response.Accepted != 3 || response.Rejected != 2 {
		t.Fatalf("response %+v", response)
	}
	wantErrors := []recordError{{Index: 1, Error: "record is not valid JSON"}, {Index: 3, Error: "url is required"}}
	if fmt.Sprint(response.Errors) != fmt.Sprint(wantErrors) {
		t.Fatalf("errors %v, want %v", response.Errors, wantErrors)
	}
}

func TestStreamStoresRequestBodyAndRejectsOverCapRecord(t *testing.T) {
	writer := newChunkRecordingWriter()
	bodyConfig := streamConfig(100)
	bodyConfig.MaxBodyBytes = 8
	body := `{"url":"https://a.com","method":"GET","request_body":"aGk=","request_body_encoding":"base64"}` + "\n" +
		`{"url":"https://a.com","method":"POST","request_body":"secret-body-content"}` + "\n"

	recorder := serveStreamSimple(writer, bodyConfig, body)

	response := requireStatus(t, recorder, http.StatusOK)
	if response.Accepted != 1 || response.Rejected != 1 || len(response.Errors) != 1 {
		t.Fatalf("response %+v", response)
	}
	if response.Errors[0].Index != 1 || response.Errors[0].Error != "request_body exceeds MAX_BODY_BYTES (8 bytes)" {
		t.Fatalf("errors %+v", response.Errors)
	}
	if len(writer.chunks) != 1 || writer.chunks[0][0].RequestBody != "aGk=" || writer.chunks[0][0].RequestBodyEncoding != "base64" {
		t.Fatalf("chunks %+v", writer.chunks)
	}
}

func TestStreamHandlesFinalLineWithoutNewline(t *testing.T) {
	writer := newChunkRecordingWriter()

	recorder := serveStreamSimple(writer, streamConfig(100), `{"url":"https://example.com/","method":"GET"}`)

	if response := requireStatus(t, recorder, http.StatusOK); response.Accepted != 1 {
		t.Fatalf("response %+v", response)
	}
}

func TestStreamDecodesGzipAndZstd(t *testing.T) {
	for encoding, compress := range map[string]func(*testing.T, string) []byte{"gzip": gzipped, "zstd": zstded} {
		writer := newChunkRecordingWriter()
		handler := newStreamHandler(writer, &fakeDiskGuard{}, streamConfig(100), context.Background(), time.Hour)

		recorder := serveStream(handler, strings.NewReader(string(compress(t, ndjsonLines(7)))), encoding)

		if response := requireStatus(t, recorder, http.StatusOK); response.Accepted != 7 {
			t.Fatalf("%s: response %+v", encoding, response)
		}
		requireChunkSizes(t, writer, 7)
	}
}

func TestStreamFlushesIdleOpenStreamOnTimer(t *testing.T) {
	writer := newChunkRecordingWriter()
	handler := newStreamHandler(writer, &fakeDiskGuard{}, streamConfig(100), context.Background(), 20*time.Millisecond)
	pipeReader, pipeWriter := io.Pipe()
	recorderChannel := make(chan *httptest.ResponseRecorder, 1)
	go func() { recorderChannel <- serveStream(handler, pipeReader, "") }()

	pipeWriter.Write([]byte(ndjsonLines(3)))
	if size := waitForInsert(t, writer); size != 3 {
		t.Fatalf("timer flush inserted %d records, want 3", size)
	}
	pipeWriter.Close()

	select {
	case recorder := <-recorderChannel:
		if response := requireStatus(t, recorder, http.StatusOK); response.Accepted != 3 {
			t.Fatalf("response %+v", response)
		}
	case <-time.After(streamTestTimeout):
		t.Fatal("handler did not return")
	}
}

func TestStreamFlushesWhenBufferedBytesReachFourTimesMaxRequestBytes(t *testing.T) {
	writer := newChunkRecordingWriter()
	loadedConfig := streamConfig(5000)
	loadedConfig.MaxRequestBytes = 1024
	body := strings.Repeat(paddedLine(600)+"\n", 10)

	recorder := serveStreamSimple(writer, loadedConfig, body)

	requireStatus(t, recorder, http.StatusOK)
	requireChunkSizes(t, writer, 7, 3)
}

func TestStreamRecordTriggerFollowsMaxBatchRecords(t *testing.T) {
	writer := newChunkRecordingWriter()

	recorder := serveStreamSimple(writer, streamConfig(10), ndjsonLines(25))

	requireStatus(t, recorder, http.StatusOK)
	requireChunkSizes(t, writer, 10, 10, 5)
}

func TestStreamLineCapBoundary(t *testing.T) {
	loadedConfig := streamConfig(100)
	loadedConfig.MaxRequestBytes = 2048

	exactWriter := newChunkRecordingWriter()
	exact := serveStreamSimple(exactWriter, loadedConfig, paddedLine(2048)+"\n")
	if response := requireStatus(t, exact, http.StatusOK); response.Accepted != 1 {
		t.Fatalf("response %+v", response)
	}

	oversizeWriter := newChunkRecordingWriter()
	oversize := serveStreamSimple(oversizeWriter, loadedConfig, ndjsonLines(3)+paddedLine(2049)+"\n"+ndjsonLines(1))
	response := requireStatus(t, oversize, http.StatusRequestEntityTooLarge)
	if response.Accepted != 3 || response.Error != "line exceeds MAX_REQUEST_BYTES (2048 bytes)" {
		t.Fatalf("response %+v", response)
	}
	requireChunkSizes(t, oversizeWriter, 3)
}

type streamClientResult struct {
	status     int
	retryAfter string
	response   streamResponse
	err        error
}

func startStreamClient(serverURL string, body io.Reader) <-chan streamClientResult {
	results := make(chan streamClientResult, 1)
	go func() {
		request, err := http.NewRequest(http.MethodPost, serverURL+"/v1/requests/stream", body)
		if err != nil {
			results <- streamClientResult{err: err}
			return
		}
		request.Header.Set("Authorization", "Bearer good-key")
		httpResponse, err := http.DefaultClient.Do(request)
		if err != nil {
			results <- streamClientResult{err: err}
			return
		}
		defer httpResponse.Body.Close()
		var response streamResponse
		decodeErr := json.NewDecoder(httpResponse.Body).Decode(&response)
		results <- streamClientResult{status: httpResponse.StatusCode, retryAfter: httpResponse.Header.Get("Retry-After"), response: response, err: decodeErr}
	}()
	return results
}

func awaitStreamClient(t *testing.T, results <-chan streamClientResult) streamClientResult {
	t.Helper()
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatalf("client error: %v", result.err)
		}
		return result
	case <-time.After(streamTestTimeout):
		t.Fatal("stream request did not return")
		return streamClientResult{}
	}
}

func TestStreamRateLimitMidStreamStopsReadingWhileClientKeepsSending(t *testing.T) {
	writer := newChunkRecordingWriter()
	loadedConfig := streamConfig(10)
	loadedConfig.RateLimitRecordsPerMinute = 15
	server := httptest.NewServer(newStreamHandler(writer, &fakeDiskGuard{}, loadedConfig, context.Background(), time.Hour))
	defer server.Close()
	pipeReader, pipeWriter := io.Pipe()
	defer pipeWriter.Close()
	go pipeWriter.Write([]byte(ndjsonLines(20)))

	result := awaitStreamClient(t, startStreamClient(server.URL, pipeReader))

	if result.status != http.StatusTooManyRequests || result.retryAfter == "" || result.response.Accepted != 10 || result.response.Error != "rate limit exceeded" {
		t.Fatalf("result %+v", result)
	}
	requireChunkSizes(t, writer, 10)
}

func TestStreamOverBudgetMidStream(t *testing.T) {
	writer := newChunkRecordingWriter()
	guard := &switchableDiskGuard{}
	writer.afterInsert = func() { guard.overBudget.Store(true) }
	handler := newStreamHandler(writer, guard, streamConfig(10), context.Background(), time.Hour)

	recorder := serveStream(handler, strings.NewReader(ndjsonLines(25)), "")

	response := requireStatus(t, recorder, http.StatusInsufficientStorage)
	if response.Accepted != 10 || response.Error != "storage budget exceeded" {
		t.Fatalf("response %+v", response)
	}
	requireChunkSizes(t, writer, 10)
}

func TestStreamInsertFailureMidStreamDropsFailedChunk(t *testing.T) {
	writer := newChunkRecordingWriter()
	writer.failOnCall = 2
	valid := `{"url":"https://example.com/","method":"GET"}`
	lines := make([]string, 25)
	for index := range lines {
		lines[index] = valid
	}
	lines[12] = `{"method":"GET"}`

	recorder := serveStreamSimple(writer, streamConfig(10), strings.Join(lines, "\n")+"\n")

	response := requireStatus(t, recorder, http.StatusServiceUnavailable)
	if recorder.Header().Get("Retry-After") != "5" {
		t.Fatalf("Retry-After %q", recorder.Header().Get("Retry-After"))
	}
	if response.Accepted != 10 || response.Rejected != 0 || len(response.Errors) != 0 || response.Error != "clickhouse unavailable" {
		t.Fatalf("response %+v", response)
	}
	requireChunkSizes(t, writer, 10, 9)
}

func TestStreamReadErrorFlushesAlreadyReadRecords(t *testing.T) {
	writer := newChunkRecordingWriter()
	body := io.MultiReader(strings.NewReader(ndjsonLines(3)), failingAfterReader{})
	handler := newStreamHandler(writer, &fakeDiskGuard{}, streamConfig(100), context.Background(), time.Hour)

	recorder := serveStream(handler, body, "")

	response := requireStatus(t, recorder, http.StatusBadRequest)
	if response.Accepted != 3 || response.Error != "reading request body failed" {
		t.Fatalf("response %+v", response)
	}
	requireChunkSizes(t, writer, 3)
}

type failingAfterReader struct{}

func (failingAfterReader) Read([]byte) (int, error) {
	return 0, errors.New("connection reset")
}

type consumptionWatchingBody struct {
	body        io.ReadCloser
	expected    int
	totalRead   int
	allConsumed chan struct{}
	once        sync.Once
}

func (watching *consumptionWatchingBody) Read(buffer []byte) (int, error) {
	if watching.totalRead >= watching.expected {
		watching.once.Do(func() { close(watching.allConsumed) })
	}
	bytesRead, err := watching.body.Read(buffer)
	watching.totalRead += bytesRead
	return bytesRead, err
}

func (watching *consumptionWatchingBody) Close() error {
	return watching.body.Close()
}

func TestStreamShutdownFlushesPendingRecordsAndResponds503(t *testing.T) {
	writer := newChunkRecordingWriter()
	shutdownContext, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	inner := newStreamHandler(writer, &fakeDiskGuard{}, streamConfig(100), shutdownContext, time.Hour)
	allConsumed := make(chan struct{})
	lines := ndjsonLines(3)
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		request.Body = &consumptionWatchingBody{body: request.Body, expected: len(lines), allConsumed: allConsumed}
		inner.ServeHTTP(responseWriter, request)
	}))
	defer server.Close()
	pipeReader, pipeWriter := io.Pipe()
	defer pipeWriter.Close()
	go pipeWriter.Write([]byte(lines))
	results := startStreamClient(server.URL, pipeReader)

	select {
	case <-allConsumed:
	case <-time.After(streamTestTimeout):
		t.Fatal("handler did not consume the lines")
	}
	shutdown()

	result := awaitStreamClient(t, results)
	if result.status != http.StatusServiceUnavailable || result.retryAfter != "5" || result.response.Error != "server shutting down" || result.response.Accepted != 3 {
		t.Fatalf("result %+v", result)
	}
	requireChunkSizes(t, writer, 3)
}

func TestStreamGuardsRunBeforeReadingBody(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	unauthenticated := httptest.NewRequest(http.MethodPost, "/v1/requests/stream", failingReader{t})
	unauthenticatedRecorder := httptest.NewRecorder()
	NewHandler(context.Background(), logger, &fakePinger{}, &fakeKeyStore{}, &fakeRefresher{}, newChunkRecordingWriter(), &fakeStorageReporter{}, &fakeDiskGuard{}, testIngestConfig).ServeHTTP(unauthenticatedRecorder, unauthenticated)
	if unauthenticatedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", unauthenticatedRecorder.Code)
	}

	overBudgetGuard := &fakeDiskGuard{usage: storage.Usage{UsedBytes: 1001, BudgetBytes: 1000}}
	overBudget := serveStream(newGuardedHandler(&fakeRecordWriter{}, overBudgetGuard, io.Discard, rateLimitConfig()), failingReader{t}, "")
	if overBudget.Code != http.StatusInsufficientStorage {
		t.Fatalf("status %d, want 507", overBudget.Code)
	}

	handler := newRateLimitedHandler(&fakeRecordWriter{}, rateLimitConfig())
	serveIngestWith(handler, "good-key", strings.NewReader(recordsJSON(100)))
	emptyBucket := serveStream(handler, failingReader{t}, "")
	requireRateLimited(t, emptyBucket)
}

func TestStreamEmptyAndAllInvalidBodiesReturn400(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "blank lines": "\n\n", "all invalid": "{not json\n{\"method\":\"GET\"}\n"} {
		recorder := serveStreamSimple(newChunkRecordingWriter(), streamConfig(100), body)
		response := requireStatus(t, recorder, http.StatusBadRequest)
		if response.Accepted != 0 || response.Error != "" {
			t.Fatalf("%s: response %+v", name, response)
		}
	}
}

func TestStreamErrorsListIsCappedWhileRejectedKeepsCounting(t *testing.T) {
	body := strings.Repeat("{\"method\":\"GET\"}\n", 12)

	recorder := serveStreamSimple(newChunkRecordingWriter(), streamConfig(5), body)

	response := requireStatus(t, recorder, http.StatusBadRequest)
	if response.Rejected != 12 || len(response.Errors) != 5 {
		t.Fatalf("response %+v", response)
	}
	for index, rejection := range response.Errors {
		if rejection.Index != index {
			t.Fatalf("errors %v", response.Errors)
		}
	}
}

func TestStreamRejectsUnsupportedContentEncoding(t *testing.T) {
	handler := newStreamHandler(newChunkRecordingWriter(), &fakeDiskGuard{}, streamConfig(100), context.Background(), time.Hour)

	recorder := serveStream(handler, strings.NewReader(ndjsonLines(1)), "br")

	response := requireStatus(t, recorder, http.StatusUnsupportedMediaType)
	if response.Error != "unsupported Content-Encoding; use gzip, zstd or none" {
		t.Fatalf("response %+v", response)
	}
}

func TestStreamAndBatchShareOneRateLimitBucket(t *testing.T) {
	handler := newRateLimitedHandler(&fakeRecordWriter{}, rateLimitConfig())
	serveIngestWith(handler, "good-key", strings.NewReader(recordsJSON(100)))

	requireRateLimited(t, serveStream(handler, strings.NewReader(ndjsonLines(1)), ""))
}
