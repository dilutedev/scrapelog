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
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/bodies"
	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/query"
)

type fakeRequestReader struct {
	searchRows       []query.RequestSummary
	searchError      error
	detail           query.RequestDetail
	detailError      error
	tapeBuckets      []query.TapeBucket
	tapeError        error
	facetValues      []query.FacetValue
	facetError       error
	bandwidthRows    []query.BandwidthRow
	bandwidthError   error
	lastSearch       query.SearchRequest
	lastDetail       query.RequestKey
	lastTape         query.TapeRequest
	lastFacets       query.FacetRequest
	lastBandwidth    query.BandwidthRequest
	exportBody       string
	exportStream     io.ReadCloser
	exportError      error
	lastExport       query.ExportRequest
	lastExportFormat string
}

func (reader *fakeRequestReader) Search(ctx context.Context, searchRequest query.SearchRequest) ([]query.RequestSummary, error) {
	reader.lastSearch = searchRequest
	return reader.searchRows, reader.searchError
}

func (reader *fakeRequestReader) Detail(ctx context.Context, key query.RequestKey) (query.RequestDetail, error) {
	reader.lastDetail = key
	return reader.detail, reader.detailError
}

func (reader *fakeRequestReader) Tape(ctx context.Context, tapeRequest query.TapeRequest) ([]query.TapeBucket, error) {
	reader.lastTape = tapeRequest
	return reader.tapeBuckets, reader.tapeError
}

func (reader *fakeRequestReader) Facets(ctx context.Context, facetRequest query.FacetRequest) ([]query.FacetValue, error) {
	reader.lastFacets = facetRequest
	return reader.facetValues, reader.facetError
}

func (reader *fakeRequestReader) Bandwidth(ctx context.Context, bandwidthRequest query.BandwidthRequest) ([]query.BandwidthRow, error) {
	reader.lastBandwidth = bandwidthRequest
	return reader.bandwidthRows, reader.bandwidthError
}

func (reader *fakeRequestReader) Export(ctx context.Context, exportRequest query.ExportRequest, format string) (io.ReadCloser, error) {
	reader.lastExport = exportRequest
	reader.lastExportFormat = format
	if reader.exportError != nil {
		return nil, reader.exportError
	}
	if reader.exportStream != nil {
		return reader.exportStream, nil
	}
	return io.NopCloser(strings.NewReader(reader.exportBody)), nil
}

type fakeBodyGetter struct {
	body    []byte
	err     error
	lastKey string
}

func (getter *fakeBodyGetter) Get(ctx context.Context, key string) ([]byte, error) {
	getter.lastKey = key
	return getter.body, getter.err
}

func newRequestsHandler(requestReader RequestReader, bodyGetter BodyGetter) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewHandler(context.Background(), logger, &fakePinger{}, &fakeKeyStore{}, &fakeRefresher{}, &fakeRecordWriter{}, &fakeBodyStore{}, &fakeStorageReporter{}, &fakeDiskGuard{}, requestReader, bodyGetter, &fakeAlertStore{}, &fakeRefresher{}, nil, emptyAlertMatcher(), &fakeAlertQueue{}, config.Config{AdminToken: testAdminToken})
}

func getWithSession(handler http.Handler, target string) *httptest.ResponseRecorder {
	return serve(handler, cookieRequest(http.MethodGet, target, newTestSigner().Issue()))
}

func timeRangeQuery(from time.Time, to time.Time) string {
	return "from=" + url.QueryEscape(from.Format(time.RFC3339Nano)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339Nano))
}

func summariesAt(count int, newest time.Time) []query.RequestSummary {
	summaries := make([]query.RequestSummary, count)
	for index := range summaries {
		summaries[index] = query.RequestSummary{Timestamp: newest.Add(-time.Duration(index) * time.Second), RequestID: fmt.Sprintf("req-%03d", index)}
	}
	return summaries
}

func gzipText(t *testing.T, text string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte(text)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return buffer.Bytes()
}

func TestUIRequestRoutesRequireSession(t *testing.T) {
	for _, path := range []string{"/ui/api/requests", "/ui/api/request", "/ui/api/request/body", "/ui/api/tape", "/ui/api/facets", "/ui/api/bandwidth", "/ui/api/requests/export?format=csv"} {
		t.Run(path, func(t *testing.T) {
			recorder := serve(newRequestsHandler(&fakeRequestReader{}, &fakeBodyGetter{}), httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401", recorder.Code)
			}
			if recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("missing security headers")
			}
		})
	}
}

func TestSearchPaginatesWithCursor(t *testing.T) {
	newest := time.Date(2026, 11, 16, 14, 52, 7, 123000000, time.UTC)
	requestReader := &fakeRequestReader{searchRows: summariesAt(3, newest)}
	handler := newRequestsHandler(requestReader, nil)

	recorder := getWithSession(handler, "/ui/api/requests?limit=2")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Rows       []query.RequestSummary `json:"rows"`
		NextCursor *string                `json:"next_cursor"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(response.Rows) != 2 || response.NextCursor == nil {
		t.Fatalf("got %d rows and cursor %v", len(response.Rows), response.NextCursor)
	}

	getWithSession(handler, "/ui/api/requests?limit=2&cursor="+url.QueryEscape(*response.NextCursor))
	lastKept := response.Rows[1]
	if requestReader.lastSearch.After == nil || !requestReader.lastSearch.After.Timestamp.Equal(lastKept.Timestamp) || requestReader.lastSearch.After.RequestID != lastKept.RequestID {
		t.Fatalf("cursor position %+v does not match last row %+v", requestReader.lastSearch.After, lastKept)
	}

	requestReader.searchRows = summariesAt(1, newest)
	recorder = getWithSession(handler, "/ui/api/requests?limit=2")
	var lastPage map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &lastPage); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(lastPage["next_cursor"]) != "null" {
		t.Fatalf("next_cursor %s, want null", lastPage["next_cursor"])
	}
}

func TestSearchCapsLimit(t *testing.T) {
	tests := map[string]struct {
		limit      string
		wantStatus int
		wantLimit  int
	}{
		"default":  {limit: "", wantStatus: http.StatusOK, wantLimit: 101},
		"capped":   {limit: "10000", wantStatus: http.StatusOK, wantLimit: 501},
		"zero":     {limit: "0", wantStatus: http.StatusBadRequest},
		"negative": {limit: "-1", wantStatus: http.StatusBadRequest},
		"text":     {limit: "abc", wantStatus: http.StatusBadRequest},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			requestReader := &fakeRequestReader{}
			target := "/ui/api/requests"
			if test.limit != "" {
				target += "?limit=" + test.limit
			}
			recorder := getWithSession(newRequestsHandler(requestReader, nil), target)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status %d, want %d", recorder.Code, test.wantStatus)
			}
			if test.wantStatus == http.StatusOK && requestReader.lastSearch.Limit != test.wantLimit {
				t.Fatalf("limit %d, want %d", requestReader.lastSearch.Limit, test.wantLimit)
			}
		})
	}
}

func TestSearchAddsRetentionNoteForOldRanges(t *testing.T) {
	now := time.Now()
	tests := map[string]struct {
		from     time.Time
		wantNote bool
	}{
		"fifteen days ago": {from: now.Add(-15 * 24 * time.Hour), wantNote: true},
		"one hour ago":     {from: now.Add(-time.Hour), wantNote: false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := getWithSession(newRequestsHandler(&fakeRequestReader{}, nil), "/ui/api/requests?"+timeRangeQuery(test.from, now))
			var response map[string]json.RawMessage
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			note, hasNote := response["note"]
			if hasNote != test.wantNote {
				t.Fatalf("note present %v, want %v", hasNote, test.wantNote)
			}
			if hasNote && string(note) != `"`+retentionNote+`"` {
				t.Fatalf("unexpected note %s", note)
			}
		})
	}
}

func TestSearchRejectsInvalidInput(t *testing.T) {
	now := time.Now()
	tests := map[string]struct {
		target      string
		wantMessage string
	}{
		"unknown field":   {target: "?q=foo:bar", wantMessage: "unknown field"},
		"bad from":        {target: "?from=yesterday", wantMessage: "from and to must be RFC 3339 times"},
		"from after to":   {target: "?" + timeRangeQuery(now, now.Add(-time.Hour)), wantMessage: "from must be before to"},
		"garbage cursor":  {target: "?cursor=%25%25", wantMessage: "invalid cursor"},
		"non-json cursor": {target: "?cursor=aGVsbG8", wantMessage: "invalid cursor"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := getWithSession(newRequestsHandler(&fakeRequestReader{}, nil), "/ui/api/requests"+test.target)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", recorder.Code)
			}
			if message := errorMessage(t, recorder); !bytes.Contains([]byte(message), []byte(test.wantMessage)) {
				t.Fatalf("message %q does not contain %q", message, test.wantMessage)
			}
		})
	}
}

func TestSearchPassesCompiledRawFilter(t *testing.T) {
	requestReader := &fakeRequestReader{}
	getWithSession(newRequestsHandler(requestReader, nil), "/ui/api/requests?q=status:403")

	compiled, err := query.Compile("status:403")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(requestReader.lastSearch.Filter, compiled.Raw) {
		t.Fatalf("filter %+v, want %+v", requestReader.lastSearch.Filter, compiled.Raw)
	}
}

func TestTapeFillsRawBuckets(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	from := now.Add(-time.Hour)
	step := int64(15)
	alignedStart := time.Unix(from.Unix()/step*step, 0).UTC()
	populatedStart := alignedStart.Add(30 * time.Second)
	requestReader := &fakeRequestReader{tapeBuckets: []query.TapeBucket{{Start: populatedStart, Status4xx: 7}}}

	recorder := getWithSession(newRequestsHandler(requestReader, nil), "/ui/api/tape?"+timeRangeQuery(from, now))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		StepSeconds int64              `json:"step_seconds"`
		Buckets     []query.TapeBucket `json:"buckets"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantBuckets := int((now.Sub(alignedStart) + 15*time.Second - 1) / (15 * time.Second))
	if response.StepSeconds != step || len(response.Buckets) != wantBuckets || requestReader.lastTape.Hourly {
		t.Fatalf("step %d, %d buckets (want %d), hourly %v", response.StepSeconds, len(response.Buckets), wantBuckets, requestReader.lastTape.Hourly)
	}
	for _, bucket := range response.Buckets {
		wantCount := uint64(0)
		if bucket.Start.Equal(populatedStart) {
			wantCount = 7
		}
		if bucket.Status4xx != wantCount {
			t.Fatalf("bucket %s has %d, want %d", bucket.Start, bucket.Status4xx, wantCount)
		}
	}
}

func TestTapeUsesHourlyRollupForOldRanges(t *testing.T) {
	to := time.Now().Add(-20 * 24 * time.Hour).Truncate(time.Hour).Add(30 * time.Minute)
	from := to.Add(-24 * time.Hour)
	requestReader := &fakeRequestReader{}

	recorder := getWithSession(newRequestsHandler(requestReader, nil), "/ui/api/tape?"+timeRangeQuery(from, to))
	var response struct {
		StepSeconds int64              `json:"step_seconds"`
		Buckets     []query.TapeBucket `json:"buckets"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !requestReader.lastTape.Hourly || response.StepSeconds != 3600 || len(response.Buckets) != 25 {
		t.Fatalf("hourly %v, step %d, %d buckets", requestReader.lastTape.Hourly, response.StepSeconds, len(response.Buckets))
	}
	if requestReader.lastTape.From.Unix()%3600 != 0 {
		t.Fatalf("hourly from %s is not hour-aligned", requestReader.lastTape.From)
	}
}

func TestTapeReportsApproximateForNonHourlyTerms(t *testing.T) {
	now := time.Now()
	oldTo := now.Add(-20 * 24 * time.Hour)
	tests := map[string]struct {
		from            time.Time
		to              time.Time
		wantApproximate bool
	}{
		"old range":    {from: oldTo.Add(-time.Hour), to: oldTo, wantApproximate: true},
		"recent range": {from: now.Add(-time.Hour), to: now, wantApproximate: false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			requestReader := &fakeRequestReader{}
			recorder := getWithSession(newRequestsHandler(requestReader, nil), "/ui/api/tape?q=url~x&"+timeRangeQuery(test.from, test.to))
			var response map[string]json.RawMessage
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			_, hasApproximate := response["approximate"]
			if hasApproximate != test.wantApproximate {
				t.Fatalf("approximate present %v, want %v", hasApproximate, test.wantApproximate)
			}
			if test.wantApproximate && requestReader.lastTape.Filter.Where != "1" {
				t.Fatalf("hourly filter %q, want 1", requestReader.lastTape.Filter.Where)
			}
		})
	}
}

func TestTapeStepKeepsBucketCountInRange(t *testing.T) {
	spans := map[string]time.Duration{
		"15 minutes": 15 * time.Minute,
		"1 hour":     time.Hour,
		"6 hours":    6 * time.Hour,
		"1 day":      24 * time.Hour,
		"7 days":     7 * 24 * time.Hour,
		"60 days":    60 * 24 * time.Hour,
	}
	for name, span := range spans {
		t.Run(name, func(t *testing.T) {
			step := tapeStep(span, false)
			bucketCount := (int64(span/time.Second) + step - 1) / step
			if bucketCount < 60 || bucketCount > 240 {
				t.Fatalf("step %d gives %d buckets", step, bucketCount)
			}
		})
	}
}

func TestTapeStepIsWholeHoursForHourlyRanges(t *testing.T) {
	if step := tapeStep(time.Hour, true); step != 3600 {
		t.Fatalf("step %d, want 3600", step)
	}
}

func TestRequestDetailReturnsRecord(t *testing.T) {
	requestReader := &fakeRequestReader{detail: query.RequestDetail{RequestID: "req-1", BodyKey: "bodies/req-1.gz", HasBody: true}}
	handler := newRequestsHandler(requestReader, nil)
	target := "/ui/api/request?project=acme&domain=shop.example.com&ts=2026-11-16T14:52:07.123Z&request_id=req-1"

	recorder := getWithSession(handler, target)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if response["has_body"] != true || response["request_id"] != "req-1" {
		t.Fatalf("unexpected detail %v", response)
	}
	if requestReader.lastDetail.Project != "acme" || requestReader.lastDetail.Timestamp.UnixMilli() != time.Date(2026, 11, 16, 14, 52, 7, 123000000, time.UTC).UnixMilli() {
		t.Fatalf("unexpected key %+v", requestReader.lastDetail)
	}

	requestReader.detailError = query.ErrRequestNotFound
	if recorder := getWithSession(handler, target); recorder.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", recorder.Code)
	}
	if recorder := getWithSession(handler, "/ui/api/request?project=acme"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", recorder.Code)
	}
}

func TestRequestBodyServesGunzippedText(t *testing.T) {
	htmlBody := "<html><script>alert(1)</script></html>"
	requestReader := &fakeRequestReader{detail: query.RequestDetail{BodyKey: "bodies/req-1.gz"}}
	bodyGetter := &fakeBodyGetter{body: gzipText(t, htmlBody)}

	recorder := getWithSession(newRequestsHandler(requestReader, bodyGetter), "/ui/api/request/body?project=acme&domain=shop.example.com&ts=2026-11-16T14:52:07.123Z&request_id=req-1")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/plain; charset=utf-8" {
		t.Fatalf("content type %q", contentType)
	}
	if recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
	if recorder.Body.String() != htmlBody {
		t.Fatalf("body %q", recorder.Body.String())
	}
	if bodyGetter.lastKey != "bodies/req-1.gz" {
		t.Fatalf("fetched key %q", bodyGetter.lastKey)
	}
}

func TestRequestBodyReturnsNotFound(t *testing.T) {
	bodyTarget := "/ui/api/request/body?project=acme&domain=shop.example.com&ts=2026-11-16T14:52:07.123Z&request_id=req-1"
	tests := map[string]struct {
		bodyGetter  BodyGetter
		detail      query.RequestDetail
		wantMessage string
	}{
		"no s3":       {bodyGetter: nil, wantMessage: "Response bodies are not stored: S3 is not configured."},
		"no body key": {bodyGetter: &fakeBodyGetter{}, wantMessage: "This request has no stored response body."},
		"object gone": {bodyGetter: &fakeBodyGetter{err: bodies.ErrBodyNotFound}, detail: query.RequestDetail{BodyKey: "k"}, wantMessage: "The stored response body has expired or was deleted."},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := getWithSession(newRequestsHandler(&fakeRequestReader{detail: test.detail}, test.bodyGetter), bodyTarget)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status %d, want 404", recorder.Code)
			}
			if message := errorMessage(t, recorder); message != test.wantMessage {
				t.Fatalf("message %q, want %q", message, test.wantMessage)
			}
		})
	}
}

func TestRequestBodyReportsStoreFailure(t *testing.T) {
	requestReader := &fakeRequestReader{detail: query.RequestDetail{BodyKey: "k"}}
	bodyGetter := &fakeBodyGetter{err: errors.New("s3 down")}

	recorder := getWithSession(newRequestsHandler(requestReader, bodyGetter), "/ui/api/request/body?project=acme&domain=shop.example.com&ts=2026-11-16T14:52:07.123Z&request_id=req-1")

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", recorder.Code)
	}
}

func TestReaderTimeoutReturnsPlainMessage(t *testing.T) {
	tests := map[string]struct {
		err         error
		wantStatus  int
		wantMessage string
	}{
		"timeout": {err: query.ErrQueryTimeout, wantStatus: http.StatusGatewayTimeout, wantMessage: queryTimeoutMessage},
		"other":   {err: errors.New("connection refused"), wantStatus: http.StatusServiceUnavailable, wantMessage: "clickhouse unavailable"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := getWithSession(newRequestsHandler(&fakeRequestReader{searchError: test.err}, nil), "/ui/api/requests")
			if recorder.Code != test.wantStatus {
				t.Fatalf("status %d, want %d", recorder.Code, test.wantStatus)
			}
			if message := errorMessage(t, recorder); message != test.wantMessage {
				t.Fatalf("message %q, want %q", message, test.wantMessage)
			}
		})
	}
}

func TestFacetsPassesFieldAndPrefix(t *testing.T) {
	requestReader := &fakeRequestReader{facetValues: []query.FacetValue{{Value: "shop.example.com", Count: 3}}}
	handler := newRequestsHandler(requestReader, nil)

	recorder := getWithSession(handler, "/ui/api/facets?field=domain&prefix=sh")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if requestReader.lastFacets.Field != "domain" || requestReader.lastFacets.Prefix != "sh" {
		t.Fatalf("unexpected facet request %+v", requestReader.lastFacets)
	}

	requestReader.facetError = query.ErrUnknownFacetField
	if recorder := getWithSession(handler, "/ui/api/facets?field=url"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", recorder.Code)
	}
}

type bandwidthTestResponse struct {
	From           time.Time `json:"from"`
	To             time.Time `json:"to"`
	StepSeconds    int64     `json:"step_seconds"`
	TotalBytes     uint64    `json:"total_bytes"`
	TotalResponses uint64    `json:"total_responses"`
	Series         []struct {
		Key    string     `json:"key"`
		Points [][2]int64 `json:"points"`
	} `json:"series"`
	Groups []struct {
		Key       string `json:"key"`
		Bytes     uint64 `json:"bytes"`
		Responses uint64 `json:"responses"`
	} `json:"groups"`
}

func getBandwidth(t *testing.T, requestReader *fakeRequestReader, target string) (*httptest.ResponseRecorder, bandwidthTestResponse) {
	t.Helper()
	recorder := getWithSession(newRequestsHandler(requestReader, nil), target)
	var response bandwidthTestResponse
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	return recorder, response
}

func bandwidthTarget(from string, to string, extra string) string {
	return "/ui/api/bandwidth?from=" + url.QueryEscape(from) + "&to=" + url.QueryEscape(to) + extra
}

func TestBandwidthFoldsOtherAndSumsGroupsToTotal(t *testing.T) {
	base := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	requestReader := &fakeRequestReader{bandwidthRows: []query.BandwidthRow{
		{Start: base, Key: "small", Bytes: 10, Responses: 1},
		{Start: base, Key: "", Bytes: 500, Responses: 5},
		{Start: base, Other: true, Key: "", Bytes: 7, Responses: 2},
		{Start: base.Add(time.Hour), Key: "big", Bytes: 900, Responses: 9},
		{Start: base.Add(time.Hour), Key: "small", Bytes: 20, Responses: 2},
		{Start: base.Add(time.Hour), Other: true, Key: "", Bytes: 3, Responses: 1},
		{Start: base.Add(3 * time.Hour), Key: "Other", Bytes: 100, Responses: 4},
		{Start: base.Add(3 * time.Hour), Other: true, Key: "", Bytes: 5, Responses: 1},
	}}
	recorder, response := getBandwidth(t, requestReader, bandwidthTarget("2026-11-16T10:00:00Z", "2026-11-16T14:00:00Z", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	wantKeys := []string{"big", "", "Other", "small", "Other"}
	wantBytes := []uint64{900, 500, 100, 30, 15}
	if len(response.Groups) != len(wantKeys) {
		t.Fatalf("got %d groups, want %d", len(response.Groups), len(wantKeys))
	}
	var groupBytesSum, groupResponsesSum uint64
	for index, group := range response.Groups {
		if group.Key != wantKeys[index] || group.Bytes != wantBytes[index] {
			t.Fatalf("group %d is %q/%d, want %q/%d", index, group.Key, group.Bytes, wantKeys[index], wantBytes[index])
		}
		if response.Series[index].Key != group.Key {
			t.Fatalf("series %d key %q does not match group key %q", index, response.Series[index].Key, group.Key)
		}
		if len(response.Series[index].Points) != 4 {
			t.Fatalf("series %d has %d points, want 4", index, len(response.Series[index].Points))
		}
		groupBytesSum += group.Bytes
		groupResponsesSum += group.Responses
	}
	if groupBytesSum != response.TotalBytes || groupResponsesSum != response.TotalResponses {
		t.Fatalf("groups sum to %d/%d, totals are %d/%d", groupBytesSum, groupResponsesSum, response.TotalBytes, response.TotalResponses)
	}
	if response.TotalBytes != 1545 || response.TotalResponses != 25 {
		t.Fatalf("totals %d/%d, want 1545/25", response.TotalBytes, response.TotalResponses)
	}
	otherPoints := response.Series[4].Points
	wantOtherBytes := []int64{7, 3, 0, 5}
	for index, point := range otherPoints {
		if point[0] != base.Add(time.Duration(index)*time.Hour).Unix() || point[1] != wantOtherBytes[index] {
			t.Fatalf("other point %d is %v, want bytes %d", index, point, wantOtherBytes[index])
		}
	}
}

func TestBandwidthOmitsOtherWhenNoOtherRows(t *testing.T) {
	base := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	requestReader := &fakeRequestReader{bandwidthRows: []query.BandwidthRow{{Start: base, Key: "only", Bytes: 5, Responses: 1}}}
	_, response := getBandwidth(t, requestReader, bandwidthTarget("2026-11-16T10:00:00Z", "2026-11-16T11:00:00Z", ""))
	if len(response.Groups) != 1 || response.Groups[0].Key != "only" {
		t.Fatalf("groups %+v, want only the single key", response.Groups)
	}
}

func TestBandwidthAlignsRangeToStep(t *testing.T) {
	tests := map[string]struct {
		from        string
		to          string
		step        string
		wantFrom    string
		wantTo      string
		wantStep    int64
		wantBuckets int
	}{
		"hour":            {from: "2026-11-16T10:20:00Z", to: "2026-11-16T13:05:00Z", step: "hour", wantFrom: "2026-11-16T10:00:00Z", wantTo: "2026-11-16T14:00:00Z", wantStep: 3600, wantBuckets: 4},
		"day":             {from: "2026-11-09T05:00:00Z", to: "2026-11-11T01:00:00Z", step: "day", wantFrom: "2026-11-09T00:00:00Z", wantTo: "2026-11-12T00:00:00Z", wantStep: 86400, wantBuckets: 3},
		"already aligned": {from: "2026-11-16T10:00:00Z", to: "2026-11-16T12:00:00Z", step: "hour", wantFrom: "2026-11-16T10:00:00Z", wantTo: "2026-11-16T12:00:00Z", wantStep: 3600, wantBuckets: 2},
		"inside one hour": {from: "2026-11-16T10:10:00Z", to: "2026-11-16T10:20:00Z", step: "hour", wantFrom: "2026-11-16T10:00:00Z", wantTo: "2026-11-16T11:00:00Z", wantStep: 3600, wantBuckets: 1},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			requestReader := &fakeRequestReader{bandwidthRows: []query.BandwidthRow{{Start: time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC), Key: "a", Bytes: 1, Responses: 1}}}
			recorder, response := getBandwidth(t, requestReader, bandwidthTarget(test.from, test.to, "&step="+test.step))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
			}
			wantFrom, _ := time.Parse(time.RFC3339, test.wantFrom)
			wantTo, _ := time.Parse(time.RFC3339, test.wantTo)
			if !response.From.Equal(wantFrom) || !response.To.Equal(wantTo) || response.StepSeconds != test.wantStep {
				t.Fatalf("response range %v to %v step %d", response.From, response.To, response.StepSeconds)
			}
			last := requestReader.lastBandwidth
			if !last.From.Equal(wantFrom) || !last.To.Equal(wantTo) || last.StepSeconds != test.wantStep {
				t.Fatalf("reader range %v to %v step %d", last.From, last.To, last.StepSeconds)
			}
			if len(response.Series[0].Points) != test.wantBuckets {
				t.Fatalf("got %d points, want %d", len(response.Series[0].Points), test.wantBuckets)
			}
		})
	}
}

func TestBandwidthPassesSplitAndProject(t *testing.T) {
	tests := map[string]struct {
		extra       string
		wantSplit   string
		wantProject string
	}{
		"defaults":    {extra: "", wantSplit: "proxy", wantProject: ""},
		"domain acme": {extra: "&split=domain&project=acme", wantSplit: "domain", wantProject: "acme"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			requestReader := &fakeRequestReader{}
			recorder, _ := getBandwidth(t, requestReader, bandwidthTarget("2026-11-16T10:00:00Z", "2026-11-16T12:00:00Z", test.extra))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
			}
			if requestReader.lastBandwidth.Split != test.wantSplit || requestReader.lastBandwidth.Project != test.wantProject {
				t.Fatalf("reader got split %q project %q", requestReader.lastBandwidth.Split, requestReader.lastBandwidth.Project)
			}
		})
	}
}

func TestBandwidthRejectsInvalidInput(t *testing.T) {
	tests := map[string]struct {
		target      string
		reader      *fakeRequestReader
		wantMessage string
	}{
		"unknown step":           {target: bandwidthTarget("2026-11-16T10:00:00Z", "2026-11-16T12:00:00Z", "&step=week"), reader: &fakeRequestReader{}, wantMessage: "step must be hour or day"},
		"too many hourly points": {target: bandwidthTarget("2026-08-01T00:00:00Z", "2026-11-10T00:00:00Z", "&step=hour"), reader: &fakeRequestReader{}, wantMessage: "the range has too many points; use step=day or a shorter range"},
		"unknown split":          {target: bandwidthTarget("2026-11-16T10:00:00Z", "2026-11-16T12:00:00Z", "&split=status"), reader: &fakeRequestReader{bandwidthError: query.ErrUnknownBandwidthSplit}, wantMessage: "split must be one of project, scraper, domain, proxy"},
		"from after to":          {target: bandwidthTarget("2026-11-16T12:00:00Z", "2026-11-16T10:00:00Z", ""), reader: &fakeRequestReader{}, wantMessage: "from must be before to"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := getWithSession(newRequestsHandler(test.reader, nil), test.target)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", recorder.Code)
			}
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if response.Error != test.wantMessage {
				t.Fatalf("error %q, want %q", response.Error, test.wantMessage)
			}
		})
	}
}

func TestBandwidthReturnsEmptyArraysWithoutRows(t *testing.T) {
	recorder, _ := getBandwidth(t, &fakeRequestReader{}, bandwidthTarget("2026-11-16T10:00:00Z", "2026-11-16T12:00:00Z", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{`"series":[]`, `"groups":[]`, `"total_bytes":0`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %s lacks %s", body, want)
		}
	}
}
