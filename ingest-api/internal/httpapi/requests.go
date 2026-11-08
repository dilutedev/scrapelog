package httpapi

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/dilutedev/scrapelog/ingest-api/internal/apikeys"
	"github.com/dilutedev/scrapelog/ingest-api/internal/bodies"
	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/ingest"
	"github.com/dilutedev/scrapelog/ingest-api/internal/ratelimit"
)

const (
	decodedBodyMultiplier   = 4
	insertTimeout           = 30 * time.Second
	insertRetryAfterSeconds = "5"
)

type RecordWriter interface {
	Insert(ctx context.Context, records []ingest.Record) error
}

type BodyStore interface {
	Store(ctx context.Context, records []ingest.Record) []bodies.Warning
}

type recordError struct {
	Index int    `json:"index"`
	Error string `json:"error"`
}

type ingestSummary struct {
	Accepted int              `json:"accepted"`
	Rejected int              `json:"rejected"`
	Errors   []recordError    `json:"errors"`
	Warnings []bodies.Warning `json:"warnings"`
}

var (
	errBodyTooLarge        = errors.New("body too large")
	errTooManyRecords      = errors.New("too many records")
	errNotArray            = errors.New("not an array")
	errUnsupportedEncoding = errors.New("unsupported content encoding")
	errInvalidGzipBody     = errors.New("invalid gzip body")
)

type limitedReader struct {
	reader    io.Reader
	remaining int64
	exceeded  bool
}

func newLimitedReader(reader io.Reader, limit int64) *limitedReader {
	return &limitedReader{reader: reader, remaining: limit + 1}
}

func (limited *limitedReader) Read(buffer []byte) (int, error) {
	if limited.exceeded {
		return 0, errBodyTooLarge
	}
	if int64(len(buffer)) > limited.remaining {
		buffer = buffer[:limited.remaining]
	}
	bytesRead, err := limited.reader.Read(buffer)
	limited.remaining -= int64(bytesRead)
	if limited.remaining == 0 {
		limited.exceeded = true
		return bytesRead - 1, errBodyTooLarge
	}
	return bytesRead, err
}

func decodedBody(contentEncoding string, body io.Reader, maxDecodedBytes int64) (io.Reader, func(), error) {
	noop := func() {}
	switch strings.ToLower(strings.TrimSpace(contentEncoding)) {
	case "", "identity":
		return body, noop, nil
	case "gzip":
		gzipReader, err := gzip.NewReader(body)
		if err != nil {
			return nil, noop, errInvalidGzipBody
		}
		return gzipReader, noop, nil
	case "zstd":
		decoder, err := zstd.NewReader(body,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderLowmem(true),
			zstd.WithDecoderMaxMemory(uint64(maxDecodedBytes)),
		)
		if err != nil {
			return nil, noop, err
		}
		return decoder, decoder.Close, nil
	}
	return nil, noop, errUnsupportedEncoding
}

func decodeRecordArray(reader io.Reader, maxRecords int, eachRecord func(index int, rawRecord json.RawMessage)) error {
	decoder := json.NewDecoder(reader)
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('[') {
		return errNotArray
	}
	for index := 0; decoder.More(); index++ {
		if index == maxRecords {
			return errTooManyRecords
		}
		var rawRecord json.RawMessage
		if err := decoder.Decode(&rawRecord); err != nil {
			return err
		}
		eachRecord(index, rawRecord)
	}
	_, err = decoder.Token()
	return err
}

func setRetryAfter(responseWriter http.ResponseWriter, retryAfter time.Duration) {
	retryAfterSeconds := max(1, int(math.Ceil(retryAfter.Seconds())))
	responseWriter.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
}

func writeRateLimited(responseWriter http.ResponseWriter, retryAfter time.Duration) {
	setRetryAfter(responseWriter, retryAfter)
	writeError(responseWriter, http.StatusTooManyRequests, "rate limit exceeded")
}

func ingestRequestsHandler(logger *slog.Logger, recordWriter RecordWriter, bodyStore BodyStore, recordLimiter *ratelimit.Limiter, diskGuard DiskGuard, loadedConfig config.Config) func(http.ResponseWriter, *http.Request, apikeys.Identity) {
	return func(responseWriter http.ResponseWriter, request *http.Request, identity apikeys.Identity) {
		usage := diskGuard.Usage()
		if usage.OverBudget() {
			writeError(responseWriter, http.StatusInsufficientStorage, "storage budget exceeded")
			return
		}
		if allowed, retryAfter := recordLimiter.HasToken(identity.KeyID); !allowed {
			writeRateLimited(responseWriter, retryAfter)
			return
		}

		wireLimit := newLimitedReader(request.Body, loadedConfig.MaxRequestBytes)
		decodedLimitBytes := decodedBodyMultiplier * loadedConfig.MaxRequestBytes
		bodyReader, closeBody, err := decodedBody(request.Header.Get("Content-Encoding"), wireLimit, decodedLimitBytes)
		defer closeBody()
		if err != nil {
			switch {
			case wireLimit.exceeded:
				writeError(responseWriter, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body exceeds MAX_REQUEST_BYTES (%d bytes)", loadedConfig.MaxRequestBytes))
			case errors.Is(err, errUnsupportedEncoding):
				writeError(responseWriter, http.StatusUnsupportedMediaType, "unsupported Content-Encoding; use gzip, zstd or none")
			case errors.Is(err, errInvalidGzipBody):
				writeError(responseWriter, http.StatusBadRequest, "invalid gzip body")
			default:
				writeError(responseWriter, http.StatusBadRequest, "invalid request body")
			}
			return
		}
		decodedLimit := newLimitedReader(bodyReader, decodedLimitBytes)

		receivedAt := time.Now().UTC()
		records := make([]ingest.Record, 0)
		summary := ingestSummary{Errors: []recordError{}, Warnings: []bodies.Warning{}}

		err = decodeRecordArray(decodedLimit, loadedConfig.MaxBatchRecords, func(index int, rawRecord json.RawMessage) {
			record, parseError := ingest.ParseRecord(rawRecord, identity.Project, receivedAt, loadedConfig.RedactedHeaders, loadedConfig.MaxBodyBytes)
			if parseError != nil {
				summary.Errors = append(summary.Errors, recordError{Index: index, Error: parseError.Error()})
				return
			}
			record.SourceIndex = index
			records = append(records, record)
		})
		switch {
		case wireLimit.exceeded:
			writeError(responseWriter, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body exceeds MAX_REQUEST_BYTES (%d bytes)", loadedConfig.MaxRequestBytes))
			return
		case decodedLimit.exceeded, errors.Is(err, zstd.ErrDecoderSizeExceeded), errors.Is(err, zstd.ErrWindowSizeExceeded):
			writeError(responseWriter, http.StatusRequestEntityTooLarge, fmt.Sprintf("decoded request body exceeds %d bytes", decodedLimitBytes))
			return
		case errors.Is(err, errTooManyRecords):
			writeError(responseWriter, http.StatusRequestEntityTooLarge, fmt.Sprintf("batch exceeds MAX_BATCH_RECORDS (%d)", loadedConfig.MaxBatchRecords))
			return
		case errors.Is(err, errNotArray):
			writeError(responseWriter, http.StatusBadRequest, "body must be a JSON array of records; wrap a single record in [ ]")
			return
		case err != nil:
			writeError(responseWriter, http.StatusBadRequest, "invalid request body")
			return
		}

		summary.Accepted = len(records)
		summary.Rejected = len(summary.Errors)
		if len(records) == 0 {
			writeJSON(responseWriter, http.StatusBadRequest, summary)
			return
		}
		if allowed, retryAfter := recordLimiter.Take(identity.KeyID, len(records)); !allowed {
			writeRateLimited(responseWriter, retryAfter)
			return
		}

		if usage.NearBudget() {
			logger.Warn("clickhouse disk usage above 90% of budget", "used_bytes", usage.UsedBytes, "budget_bytes", usage.BudgetBytes)
		}
		summary.Warnings = bodyStore.Store(request.Context(), records)
		insertContext, cancel := context.WithTimeout(request.Context(), insertTimeout)
		defer cancel()
		if err := recordWriter.Insert(insertContext, records); err != nil {
			logger.Warn("inserting records failed", "error", err, "project", identity.Project, "records", len(records))
			responseWriter.Header().Set("Retry-After", insertRetryAfterSeconds)
			writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
			return
		}
		writeJSON(responseWriter, http.StatusOK, summary)
	}
}
