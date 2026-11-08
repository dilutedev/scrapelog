package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/apikeys"
	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/ingest"
	"github.com/dilutedev/scrapelog/ingest-api/internal/ratelimit"
)

const (
	streamFlushInterval        = 2 * time.Second
	streamFlushRecordLimit     = 2000
	streamScannerInitialBuffer = 64 * 1024
)

type streamLine struct {
	index      int
	lineBytes  int
	record     ingest.Record
	parseError error
}

type streamResponse struct {
	ingestSummary
	Error string `json:"error,omitempty"`
}

type streamFailure struct {
	status  int
	message string
}

func readStreamLines(contentEncoding string, body io.Reader, loadedConfig config.Config, project string, lines chan<- streamLine, stopReading <-chan struct{}) error {
	bodyReader, closeBody, err := decodedBody(contentEncoding, body, decodedBodyMultiplier*loadedConfig.MaxRequestBytes)
	defer closeBody()
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(bodyReader)
	maxLineBytes := int(loadedConfig.MaxRequestBytes) + 1
	scanner.Buffer(make([]byte, 0, min(streamScannerInitialBuffer, maxLineBytes)), maxLineBytes)
	index := 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		record, parseError := ingest.ParseRecord(line, project, time.Now().UTC(), loadedConfig.RedactedHeaders, loadedConfig.MaxBodyBytes)
		select {
		case lines <- streamLine{index: index, lineBytes: len(line), record: record, parseError: parseError}:
		case <-stopReading:
			return nil
		}
		index++
	}
	return scanner.Err()
}

func streamRequestsHandler(logger *slog.Logger, recordWriter RecordWriter, recordLimiter *ratelimit.Limiter, diskGuard DiskGuard, shutdownContext context.Context, loadedConfig config.Config, flushInterval time.Duration) func(http.ResponseWriter, *http.Request, apikeys.Identity) {
	flushRecordLimit := min(streamFlushRecordLimit, loadedConfig.MaxBatchRecords)
	flushByteLimit := int(decodedBodyMultiplier * loadedConfig.MaxRequestBytes)
	return func(responseWriter http.ResponseWriter, request *http.Request, identity apikeys.Identity) {
		if diskGuard.Usage().OverBudget() {
			writeError(responseWriter, http.StatusInsufficientStorage, "storage budget exceeded")
			return
		}
		if allowed, retryAfter := recordLimiter.HasToken(identity.KeyID); !allowed {
			writeRateLimited(responseWriter, retryAfter)
			return
		}

		committed := ingestSummary{Errors: []recordError{}}
		var pendingRecords []ingest.Record
		var pendingErrors []recordError
		pendingLineCount := 0
		pendingLineBytes := 0

		flush := func() *streamFailure {
			recordsToInsert := pendingRecords
			errorsToCommit := pendingErrors
			pendingRecords = nil
			pendingErrors = nil
			pendingLineCount = 0
			pendingLineBytes = 0
			if len(recordsToInsert) > 0 {
				usage := diskGuard.Usage()
				if usage.OverBudget() {
					return &streamFailure{http.StatusInsufficientStorage, "storage budget exceeded"}
				}
				if allowed, retryAfter := recordLimiter.Take(identity.KeyID, len(recordsToInsert)); !allowed {
					setRetryAfter(responseWriter, retryAfter)
					return &streamFailure{http.StatusTooManyRequests, "rate limit exceeded"}
				}
				if usage.NearBudget() {
					logger.Warn("clickhouse disk usage above 90% of budget", "used_bytes", usage.UsedBytes, "budget_bytes", usage.BudgetBytes)
				}
				insertContext, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), insertTimeout)
				defer cancel()
				if err := recordWriter.Insert(insertContext, recordsToInsert); err != nil {
					logger.Warn("inserting records failed", "error", err, "project", identity.Project, "records", len(recordsToInsert))
					responseWriter.Header().Set("Retry-After", insertRetryAfterSeconds)
					return &streamFailure{http.StatusServiceUnavailable, "clickhouse unavailable"}
				}
			}
			committed.Accepted += len(recordsToInsert)
			committed.Rejected += len(errorsToCommit)
			for _, rejection := range errorsToCommit {
				if len(committed.Errors) >= loadedConfig.MaxBatchRecords {
					break
				}
				committed.Errors = append(committed.Errors, rejection)
			}
			return nil
		}

		lines := make(chan streamLine)
		stopReading := make(chan struct{})
		var readError error
		go func() {
			defer close(lines)
			readError = readStreamLines(request.Header.Get("Content-Encoding"), request.Body, loadedConfig, identity.Project, lines, stopReading)
		}()
		stopStream := func() {
			close(stopReading)
			http.NewResponseController(responseWriter).SetReadDeadline(time.Now())
			for range lines {
			}
		}

		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()

		var failure *streamFailure
		shuttingDown := false
	readLoop:
		for {
			select {
			case line, open := <-lines:
				if !open {
					break readLoop
				}
				pendingLineCount++
				pendingLineBytes += line.lineBytes
				if line.parseError != nil {
					pendingErrors = append(pendingErrors, recordError{Index: line.index, Error: line.parseError.Error()})
				} else {
					pendingRecords = append(pendingRecords, line.record)
				}
				if pendingLineCount >= flushRecordLimit || pendingLineBytes >= flushByteLimit {
					failure = flush()
					ticker.Reset(flushInterval)
					if failure != nil {
						stopStream()
						break readLoop
					}
				}
			case <-ticker.C:
				if pendingLineCount == 0 {
					continue
				}
				if failure = flush(); failure != nil {
					stopStream()
					break readLoop
				}
			case <-shutdownContext.Done():
				shuttingDown = true
				stopStream()
				break readLoop
			}
		}

		if failure == nil {
			failure = flush()
		}
		switch {
		case failure != nil:
			writeJSON(responseWriter, failure.status, streamResponse{committed, failure.message})
		case shuttingDown:
			responseWriter.Header().Set("Retry-After", insertRetryAfterSeconds)
			writeJSON(responseWriter, http.StatusServiceUnavailable, streamResponse{committed, "server shutting down"})
		case errors.Is(readError, bufio.ErrTooLong):
			writeJSON(responseWriter, http.StatusRequestEntityTooLarge, streamResponse{committed, fmt.Sprintf("line exceeds MAX_REQUEST_BYTES (%d bytes)", loadedConfig.MaxRequestBytes)})
		case errors.Is(readError, errUnsupportedEncoding):
			writeJSON(responseWriter, http.StatusUnsupportedMediaType, streamResponse{committed, "unsupported Content-Encoding; use gzip, zstd or none"})
		case errors.Is(readError, errInvalidGzipBody):
			writeJSON(responseWriter, http.StatusBadRequest, streamResponse{committed, "invalid gzip body"})
		case readError != nil:
			writeJSON(responseWriter, http.StatusBadRequest, streamResponse{committed, "reading request body failed"})
		case committed.Accepted == 0:
			writeJSON(responseWriter, http.StatusBadRequest, streamResponse{ingestSummary: committed})
		default:
			writeJSON(responseWriter, http.StatusOK, streamResponse{ingestSummary: committed})
		}
	}
}
