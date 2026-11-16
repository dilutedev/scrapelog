package httpapi

import (
	"encoding/csv"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/query"
)

type exportFormat struct {
	clickHouseFormat string
	contentType      string
	extension        string
}

var exportFormats = map[string]exportFormat{
	"ndjson": {"JSONEachRow", "application/x-ndjson", "ndjson"},
	"csv":    {"CSVWithNames", "text/csv; charset=utf-8", "csv"},
}

const exportFileTimeLayout = "20060102T150405Z"

func exportRequestsHandler(logger *slog.Logger, requestReader RequestReader) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		chosenFormat, known := exportFormats[request.URL.Query().Get("format")]
		if !known {
			writeError(responseWriter, http.StatusBadRequest, "format must be ndjson or csv")
			return
		}
		rangeStart, rangeEnd, err := parseTimeRange(request, time.Now())
		if err != nil {
			writeError(responseWriter, http.StatusBadRequest, err.Error())
			return
		}
		compiled, err := query.Compile(request.URL.Query().Get("q"))
		if err != nil {
			writeError(responseWriter, http.StatusBadRequest, err.Error())
			return
		}
		restoreID, err := parseRestoreID(request)
		if err != nil {
			writeError(responseWriter, http.StatusBadRequest, err.Error())
			return
		}
		stream, err := requestReader.Export(request.Context(), query.ExportRequest{Filter: compiled.Raw, From: rangeStart, To: rangeEnd, RestoreID: restoreID}, chosenFormat.clickHouseFormat)
		if err != nil {
			writeReaderError(logger, responseWriter, err, "exporting requests failed")
			return
		}
		defer stream.Close()
		fileName := "scrapelog-requests-" + rangeStart.UTC().Format(exportFileTimeLayout) + "-" + rangeEnd.UTC().Format(exportFileTimeLayout) + "." + chosenFormat.extension
		responseWriter.Header().Set("Content-Type", chosenFormat.contentType)
		responseWriter.Header().Set("Content-Disposition", `attachment; filename="`+fileName+`"`)
		responseWriter.Header().Set("Cache-Control", "no-store")
		responseWriter.WriteHeader(http.StatusOK)
		if chosenFormat.extension == "csv" {
			err = writeNeutralizedCSV(responseWriter, stream)
		} else {
			_, err = io.Copy(responseWriter, stream)
		}
		if err != nil {
			abortExport(logger, request, err)
		}
	}
}

func writeNeutralizedCSV(destination io.Writer, source io.Reader) error {
	csvReader := csv.NewReader(source)
	csvReader.ReuseRecord = true
	csvWriter := csv.NewWriter(destination)
	for {
		record, err := csvReader.Read()
		if errors.Is(err, io.EOF) {
			csvWriter.Flush()
			return csvWriter.Error()
		}
		if err != nil {
			return err
		}
		for index, field := range record {
			if field != "" && strings.ContainsRune("=+-@\t\r", rune(field[0])) {
				record[index] = "'" + field
			}
		}
		if err := csvWriter.Write(record); err != nil {
			return err
		}
	}
}

func abortExport(logger *slog.Logger, request *http.Request, err error) {
	if request.Context().Err() == nil {
		logger.Error("exporting requests failed", "error", err)
	}
	panic(http.ErrAbortHandler)
}
