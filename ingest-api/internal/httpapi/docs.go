package httpapi

import (
	_ "embed"
	"html/template"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
)

//go:embed docs.html
var docsPageSource string

var docsPage = template.Must(template.New("docs").Funcs(template.FuncMap{
	"mebibytes": func(byteCount int64) string {
		return strconv.FormatFloat(float64(byteCount)/(1024*1024), 'f', -1, 64)
	},
}).Parse(docsPageSource))

type docsPageData struct {
	BaseURL               string
	MaxBatchRecords       int
	MaxRequestBytes       int64
	MaxDecodedBytes       int64
	MaxBodyBytes          int64
	RecordsPerMinute      int64
	RecordsPerSecond      int64
	RedactedHeaders       []string
	BodyStorageEnabled    bool
	StreamFlushLines      int
	StreamFlushSeconds    int
	InsertRetryAfter      string
	MaxTimestampAgeDays   int
	MaxTimestampAheadHour int
}

func docsHandler(logger *slog.Logger, loadedConfig config.Config) http.HandlerFunc {
	redactedHeaders := make([]string, 0, len(loadedConfig.RedactedHeaders))
	for headerName := range loadedConfig.RedactedHeaders {
		redactedHeaders = append(redactedHeaders, headerName)
	}
	slices.Sort(redactedHeaders)

	return func(responseWriter http.ResponseWriter, request *http.Request) {
		scheme := "http"
		if request.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		pageData := docsPageData{
			BaseURL:               scheme + "://" + request.Host + request.Header.Get("X-Forwarded-Prefix"),
			MaxBatchRecords:       loadedConfig.MaxBatchRecords,
			MaxRequestBytes:       loadedConfig.MaxRequestBytes,
			MaxDecodedBytes:       decodedBodyMultiplier * loadedConfig.MaxRequestBytes,
			MaxBodyBytes:          loadedConfig.MaxBodyBytes,
			RecordsPerMinute:      loadedConfig.RateLimitRecordsPerMinute,
			RecordsPerSecond:      loadedConfig.RateLimitRecordsPerMinute / 60,
			RedactedHeaders:       redactedHeaders,
			BodyStorageEnabled:    loadedConfig.S3Endpoint != "",
			StreamFlushLines:      min(streamFlushRecordLimit, loadedConfig.MaxBatchRecords),
			StreamFlushSeconds:    int(streamFlushInterval.Seconds()),
			InsertRetryAfter:      insertRetryAfterSeconds,
			MaxTimestampAgeDays:   60,
			MaxTimestampAheadHour: 1,
		}

		responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := docsPage.Execute(responseWriter, pageData); err != nil {
			logger.Warn("docs render failed", "error", err)
		}
	}
}
