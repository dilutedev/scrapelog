package httpapi

import (
	"encoding/csv"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/query"
)

type failingAfterBytesReader struct {
	remaining []byte
}

func (reader *failingAfterBytesReader) Read(destination []byte) (int, error) {
	if len(reader.remaining) == 0 {
		return 0, errors.New("connection reset")
	}
	copiedCount := copy(destination, reader.remaining)
	reader.remaining = reader.remaining[copiedCount:]
	return copiedCount, nil
}

func (reader *failingAfterBytesReader) Close() error { return nil }

var exportRange = timeRangeQuery(time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 16, 11, 30, 15, 0, time.UTC))

func TestExportUsesSameFilterAsSearch(t *testing.T) {
	requestReader := &fakeRequestReader{}
	handler := newRequestsHandler(requestReader, nil)
	parameters := "q=status:403+domain:shop.example.com&" + exportRange

	getWithSession(handler, "/ui/api/requests?"+parameters)
	getWithSession(handler, "/ui/api/requests/export?format=ndjson&"+parameters)

	if !reflect.DeepEqual(requestReader.lastSearch.Filter, requestReader.lastExport.Filter) {
		t.Fatalf("export filter %+v differs from search filter %+v", requestReader.lastExport.Filter, requestReader.lastSearch.Filter)
	}
	if !requestReader.lastSearch.From.Equal(requestReader.lastExport.From) || !requestReader.lastSearch.To.Equal(requestReader.lastExport.To) {
		t.Fatal("export range differs from search range")
	}
}

func TestExportSetsFormatHeaders(t *testing.T) {
	tests := map[string]struct {
		format             string
		wantClickHouse     string
		wantContentType    string
		wantContentDispose string
	}{
		"ndjson": {"ndjson", "JSONEachRow", "application/x-ndjson", `attachment; filename="scrapelog-requests-20261116T100000Z-20261116T113015Z.ndjson"`},
		"csv":    {"csv", "CSVWithNames", "text/csv; charset=utf-8", `attachment; filename="scrapelog-requests-20261116T100000Z-20261116T113015Z.csv"`},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			requestReader := &fakeRequestReader{exportBody: "a\n"}
			recorder := getWithSession(newRequestsHandler(requestReader, nil), "/ui/api/requests/export?format="+test.format+"&"+exportRange)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
			}
			if requestReader.lastExportFormat != test.wantClickHouse {
				t.Fatalf("clickhouse format %q, want %q", requestReader.lastExportFormat, test.wantClickHouse)
			}
			if got := recorder.Header().Get("Content-Type"); got != test.wantContentType {
				t.Fatalf("content type %q, want %q", got, test.wantContentType)
			}
			if got := recorder.Header().Get("Content-Disposition"); got != test.wantContentDispose {
				t.Fatalf("content disposition %q, want %q", got, test.wantContentDispose)
			}
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("cache control %q, want no-store", got)
			}
		})
	}
}

func TestExportRejectsUnknownFormat(t *testing.T) {
	for _, target := range []string{"/ui/api/requests/export", "/ui/api/requests/export?format=xml"} {
		recorder := getWithSession(newRequestsHandler(&fakeRequestReader{}, nil), target)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", target, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "format must be ndjson or csv") {
			t.Fatalf("%s: body %s", target, recorder.Body.String())
		}
	}
}

func TestExportReaderErrorReturnsJSONWithoutAttachment(t *testing.T) {
	tests := map[string]struct {
		readerError error
		wantStatus  int
		wantBody    string
	}{
		"timeout": {query.ErrQueryTimeout, http.StatusGatewayTimeout, queryTimeoutMessage},
		"generic": {errors.New("boom"), http.StatusServiceUnavailable, "clickhouse unavailable"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := getWithSession(newRequestsHandler(&fakeRequestReader{exportError: test.readerError}, nil), "/ui/api/requests/export?format=csv")
			if recorder.Code != test.wantStatus {
				t.Fatalf("status %d, want %d", recorder.Code, test.wantStatus)
			}
			if !strings.Contains(recorder.Body.String(), test.wantBody) {
				t.Fatalf("body %s", recorder.Body.String())
			}
			if recorder.Header().Get("Content-Disposition") != "" {
				t.Fatal("error response must not carry Content-Disposition")
			}
			if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("content type %q", recorder.Header().Get("Content-Type"))
			}
		})
	}
}

func TestExportNeutralizesCSVFormulaFields(t *testing.T) {
	body := "id,note\n" +
		`"=1+1","+x"` + "\n" +
		`"-y","@z"` + "\n" +
		"\"\tq\",\"\rr\"\n" +
		`"safe","has, comma and ""quote"""` + "\n"
	recorder := getWithSession(newRequestsHandler(&fakeRequestReader{exportBody: body}, nil), "/ui/api/requests/export?format=csv")
	records, err := csv.NewReader(recorder.Body).ReadAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := [][]string{
		{"id", "note"},
		{"'=1+1", "'+x"},
		{"'-y", "'@z"},
		{"'\tq", "'\rr"},
		{"safe", `has, comma and "quote"`},
	}
	if !reflect.DeepEqual(records, want) {
		t.Fatalf("records %q, want %q", records, want)
	}
}

func TestExportStreamsNDJSONVerbatim(t *testing.T) {
	body := "{\"error\":\"x\"}\n=SUM(1)\n{\"error\":\"-1\"}\n"
	recorder := getWithSession(newRequestsHandler(&fakeRequestReader{exportBody: body}, nil), "/ui/api/requests/export?format=ndjson")
	if recorder.Body.String() != body {
		t.Fatalf("body %q, want %q", recorder.Body.String(), body)
	}
}

func TestExportAbortsOnMidStreamError(t *testing.T) {
	requestReader := &fakeRequestReader{exportStream: &failingAfterBytesReader{remaining: []byte("{\"a\":1}\n")}}
	handler := exportRequestsHandler(slog.New(slog.NewJSONHandler(io.Discard, nil)), requestReader)
	request := httptest.NewRequest(http.MethodGet, "/ui/api/requests/export?format=ndjson", nil)

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		handler(httptest.NewRecorder(), request)
	}()

	if recovered != http.ErrAbortHandler {
		t.Fatalf("recovered %v, want http.ErrAbortHandler", recovered)
	}
}
