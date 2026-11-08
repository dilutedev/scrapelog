package ingest

import (
	"context"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const insertStatement = "INSERT INTO requests (received_at, ts, project, scraper, request_id, method, url, domain, status, latency_ms, proxy, http_version, tls_ja3, tls_ja4, tls_profile, request_headers, response_headers, response_size, body_key, request_body, request_body_encoding, error, tags)"

type Writer struct {
	connection driver.Conn
}

func NewWriter(connection driver.Conn) *Writer {
	return &Writer{connection: connection}
}

func (writer *Writer) Insert(ctx context.Context, records []Record) error {
	batch, err := writer.connection.PrepareBatch(ctx, insertStatement)
	if err != nil {
		return err
	}
	for _, record := range records {
		err := batch.Append(
			record.ReceivedAt, record.Timestamp, record.Project, record.Scraper, record.RequestID,
			record.Method, record.URL, record.Domain, record.Status, record.LatencyMs,
			record.Proxy, record.HTTPVersion, record.TLSJA3, record.TLSJA4, record.TLSProfile,
			record.RequestHeaders, record.ResponseHeaders, record.ResponseSize,
			record.BodyKey, record.RequestBody, record.RequestBodyEncoding, record.Error, record.Tags,
		)
		if err != nil {
			batch.Abort()
			return err
		}
	}
	return batch.Send()
}
