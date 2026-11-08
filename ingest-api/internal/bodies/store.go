package bodies

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/ingest"
)

const (
	maxConcurrentUploads = 8
	uploadTimeout        = 10 * time.Second
	uploadPhaseTimeout   = 20 * time.Second

	warningNotConfigured = "body dropped: S3 is not configured"
	warningUploadFailed  = "body upload failed"
	warningUploadTimeout = "body upload timed out"
)

type Uploader interface {
	Put(ctx context.Context, key string, gzippedBody []byte) error
}

type Warning struct {
	Index   int    `json:"index"`
	Warning string `json:"warning"`
}

type Store struct {
	uploader Uploader
	prefix   string
	logger   *slog.Logger
}

func NewStore(uploader Uploader, prefix string, logger *slog.Logger) *Store {
	return &Store{uploader: uploader, prefix: prefix, logger: logger}
}

func (store *Store) Store(ctx context.Context, records []ingest.Record) []Warning {
	warnings := []Warning{}
	warningByRecord := make([]string, len(records))
	var uploadIndexes []int
	for index := range records {
		record := &records[index]
		switch {
		case record.BodyDropReason != "":
			warningByRecord[index] = record.BodyDropReason
		case record.Body == nil:
			continue
		case store.uploader == nil:
			warningByRecord[index] = warningNotConfigured
			record.Body = nil
		default:
			uploadIndexes = append(uploadIndexes, index)
		}
	}

	if len(uploadIndexes) > 0 {
		store.uploadAll(ctx, records, uploadIndexes, warningByRecord)
	}

	for index, warningText := range warningByRecord {
		if warningText != "" {
			warnings = append(warnings, Warning{Index: records[index].SourceIndex, Warning: warningText})
		}
	}
	return warnings
}

func (store *Store) uploadAll(ctx context.Context, records []ingest.Record, uploadIndexes []int, warningByRecord []string) {
	phaseContext, cancelPhase := context.WithTimeout(ctx, uploadPhaseTimeout)
	defer cancelPhase()

	slots := make(chan struct{}, maxConcurrentUploads)
	var workers sync.WaitGroup
	for _, index := range uploadIndexes {
		select {
		case slots <- struct{}{}:
		case <-phaseContext.Done():
			warningByRecord[index] = warningUploadTimeout
			records[index].Body = nil
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots }()
			warningByRecord[index] = store.uploadRecord(phaseContext, &records[index])
		}()
	}
	workers.Wait()
}

func (store *Store) uploadRecord(phaseContext context.Context, record *ingest.Record) string {
	body := record.Body
	record.Body = nil
	key := store.objectKey(record)

	gzippedBody, err := gzipBytes(body)
	if err == nil {
		putContext, cancelPut := context.WithTimeout(phaseContext, uploadTimeout)
		defer cancelPut()
		err = store.uploader.Put(putContext, key, gzippedBody)
	}
	if err == nil {
		record.BodyKey = key
		return ""
	}
	store.logger.Warn("uploading body failed", "error", err, "project", record.Project, "key", key)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(phaseContext.Err(), context.DeadlineExceeded) {
		return warningUploadTimeout
	}
	return warningUploadFailed
}

func (store *Store) objectKey(record *ingest.Record) string {
	return store.prefix + record.Project + "/" + record.ReceivedAt.UTC().Format("2006/01/02") + "/" + sanitizeRequestID(record.RequestID) + ".gz"
}

func sanitizeRequestID(requestID string) string {
	var sanitized strings.Builder
	for index := 0; index < len(requestID); index++ {
		character := requestID[index]
		isSafe := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-'
		if isSafe {
			sanitized.WriteByte(character)
		} else {
			sanitized.WriteByte('_')
		}
	}
	return sanitized.String()
}

func gzipBytes(body []byte) ([]byte, error) {
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	if _, err := gzipWriter.Write(body); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}
