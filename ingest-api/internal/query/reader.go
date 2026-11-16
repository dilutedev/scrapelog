package query

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const clickHouseTimeoutExceededCode = 159

var (
	ErrRequestNotFound   = errors.New("request not found")
	ErrQueryTimeout      = errors.New("clickhouse query timed out")
	ErrUnknownFacetField = errors.New("unknown facet field")
)

var facetColumns = map[string]bool{
	"domain":      true,
	"scraper":     true,
	"project":     true,
	"proxy":       true,
	"tls_profile": true,
	"method":      true,
}

type Reader struct {
	connection driver.Conn
}

func NewReader(connection driver.Conn) *Reader {
	return &Reader{connection: connection}
}

type Position struct {
	Timestamp time.Time
	RequestID string
}

type SearchRequest struct {
	Filter Filter
	From   time.Time
	To     time.Time
	After  *Position
	Limit  int
}

type RequestSummary struct {
	Timestamp    time.Time `json:"ts"`
	Project      string    `json:"project"`
	Scraper      string    `json:"scraper"`
	RequestID    string    `json:"request_id"`
	Method       string    `json:"method"`
	URL          string    `json:"url"`
	Domain       string    `json:"domain"`
	Status       uint16    `json:"status"`
	LatencyMs    uint32    `json:"latency_ms"`
	ResponseSize uint32    `json:"response_size"`
	Proxy        string    `json:"proxy"`
	TLSProfile   string    `json:"tls_profile"`
	Error        string    `json:"error"`
}

type RequestKey struct {
	Project   string
	Domain    string
	Timestamp time.Time
	RequestID string
}

type RequestDetail struct {
	ReceivedAt          time.Time         `json:"received_at"`
	Timestamp           time.Time         `json:"ts"`
	Project             string            `json:"project"`
	Scraper             string            `json:"scraper"`
	RequestID           string            `json:"request_id"`
	Method              string            `json:"method"`
	URL                 string            `json:"url"`
	Domain              string            `json:"domain"`
	Status              uint16            `json:"status"`
	LatencyMs           uint32            `json:"latency_ms"`
	Proxy               string            `json:"proxy"`
	HTTPVersion         string            `json:"http_version"`
	TLSJA3              string            `json:"tls_ja3"`
	TLSJA4              string            `json:"tls_ja4"`
	TLSProfile          string            `json:"tls_profile"`
	RequestHeaders      map[string]string `json:"request_headers"`
	ResponseHeaders     map[string]string `json:"response_headers"`
	ResponseSize        uint32            `json:"response_size"`
	BodyKey             string            `json:"body_key"`
	RequestBody         string            `json:"request_body"`
	RequestBodyEncoding string            `json:"request_body_encoding"`
	Error               string            `json:"error"`
	Tags                map[string]string `json:"tags"`
	HasBody             bool              `json:"has_body"`
}

type TapeRequest struct {
	Filter      Filter
	From        time.Time
	To          time.Time
	StepSeconds int64
	Hourly      bool
}

type TapeBucket struct {
	Start     time.Time `json:"t"`
	Status2xx uint64    `json:"s2xx"`
	Status3xx uint64    `json:"s3xx"`
	Status4xx uint64    `json:"s4xx"`
	Status5xx uint64    `json:"s5xx"`
	Status0   uint64    `json:"s0"`
}

type FacetRequest struct {
	Field  string
	Prefix string
	From   time.Time
	To     time.Time
}

type FacetValue struct {
	Value string `json:"value"`
	Count uint64 `json:"count"`
}

func classifyError(err error) error {
	var exception *clickhouse.Exception
	if errors.As(err, &exception) && exception.Code == clickHouseTimeoutExceededCode {
		return ErrQueryTimeout
	}
	return err
}

func rangeCondition(filter Filter, from time.Time, to time.Time) (string, []any) {
	condition := "ts >= {range_from:DateTime64(3)} AND ts < {range_to:DateTime64(3)} AND (" + filter.Where + ")"
	args := append([]any{}, filter.Args...)
	args = append(args, clickhouse.Named("range_from", from), clickhouse.Named("range_to", to))
	return condition, args
}

const searchSelect = `SELECT ts, project, scraper, request_id, method, url, domain, status, latency_ms, response_size, proxy, tls_profile, error
FROM requests
WHERE `

const searchCursorClause = `
  AND (ts < {after_ts:DateTime64(3)} OR (ts = {after_ts:DateTime64(3)} AND request_id < {after_request_id:String}))`

const searchOrder = `
ORDER BY ts DESC, request_id DESC
LIMIT {limit:UInt32}`

func (reader *Reader) Search(ctx context.Context, searchRequest SearchRequest) ([]RequestSummary, error) {
	condition, args := rangeCondition(searchRequest.Filter, searchRequest.From, searchRequest.To)
	statement := searchSelect + condition
	if searchRequest.After != nil {
		statement += searchCursorClause
		args = append(args, clickhouse.Named("after_ts", searchRequest.After.Timestamp), clickhouse.Named("after_request_id", searchRequest.After.RequestID))
	}
	statement += searchOrder
	args = append(args, clickhouse.Named("limit", uint32(searchRequest.Limit)))

	rows, err := reader.connection.Query(ctx, statement, args...)
	if err != nil {
		return nil, classifyError(err)
	}
	defer rows.Close()

	summaries := []RequestSummary{}
	for rows.Next() {
		var summary RequestSummary
		if err := rows.Scan(&summary.Timestamp, &summary.Project, &summary.Scraper, &summary.RequestID, &summary.Method, &summary.URL, &summary.Domain, &summary.Status, &summary.LatencyMs, &summary.ResponseSize, &summary.Proxy, &summary.TLSProfile, &summary.Error); err != nil {
			return nil, classifyError(err)
		}
		summary.Timestamp = summary.Timestamp.UTC()
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyError(err)
	}
	return summaries, nil
}

const detailQuery = `SELECT received_at, ts, project, scraper, request_id, method, url, domain, status, latency_ms, proxy, http_version, tls_ja3, tls_ja4, tls_profile, request_headers, response_headers, response_size, body_key, request_body, request_body_encoding, error, tags
FROM requests
WHERE project = {project:String} AND domain = {domain:String} AND ts = {ts:DateTime64(3)} AND request_id = {request_id:String}
LIMIT 1`

func (reader *Reader) Detail(ctx context.Context, key RequestKey) (RequestDetail, error) {
	rows, err := reader.connection.Query(ctx, detailQuery,
		clickhouse.Named("project", key.Project),
		clickhouse.Named("domain", key.Domain),
		clickhouse.Named("ts", key.Timestamp),
		clickhouse.Named("request_id", key.RequestID),
	)
	if err != nil {
		return RequestDetail{}, classifyError(err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return RequestDetail{}, classifyError(err)
		}
		return RequestDetail{}, ErrRequestNotFound
	}
	var detail RequestDetail
	if err := rows.Scan(&detail.ReceivedAt, &detail.Timestamp, &detail.Project, &detail.Scraper, &detail.RequestID, &detail.Method, &detail.URL, &detail.Domain, &detail.Status, &detail.LatencyMs, &detail.Proxy, &detail.HTTPVersion, &detail.TLSJA3, &detail.TLSJA4, &detail.TLSProfile, &detail.RequestHeaders, &detail.ResponseHeaders, &detail.ResponseSize, &detail.BodyKey, &detail.RequestBody, &detail.RequestBodyEncoding, &detail.Error, &detail.Tags); err != nil {
		return RequestDetail{}, classifyError(err)
	}
	detail.ReceivedAt = detail.ReceivedAt.UTC()
	detail.Timestamp = detail.Timestamp.UTC()
	detail.HasBody = detail.BodyKey != ""
	return detail, nil
}

const rawTapeSelect = `SELECT intDiv(toUnixTimestamp(ts), {step_seconds:UInt32}) * {step_seconds:UInt32} AS bucket,
       countIf(status BETWEEN 200 AND 299), countIf(status BETWEEN 300 AND 399),
       countIf(status BETWEEN 400 AND 499), countIf(status BETWEEN 500 AND 599), countIf(status = 0)
FROM requests
WHERE `

const rawTapeGroup = `
GROUP BY bucket ORDER BY bucket`

const hourlyTapeQuery = `SELECT bucket, sumIf(request_total, status BETWEEN 200 AND 299), sumIf(request_total, status BETWEEN 300 AND 399),
       sumIf(request_total, status BETWEEN 400 AND 499), sumIf(request_total, status BETWEEN 500 AND 599), sumIf(request_total, status = 0)
FROM (
  SELECT intDiv(toUnixTimestamp(hour), {step_seconds:UInt32}) * {step_seconds:UInt32} AS bucket, status, sumMerge(request_count) AS request_total
  FROM requests_hourly
  WHERE hour >= {range_from:DateTime64(3)} AND hour < {range_to:DateTime64(3)} AND (%s)
  GROUP BY bucket, status
)
GROUP BY bucket ORDER BY bucket`

func (reader *Reader) Tape(ctx context.Context, tapeRequest TapeRequest) ([]TapeBucket, error) {
	var statement string
	var args []any
	if tapeRequest.Hourly {
		statement = fmt.Sprintf(hourlyTapeQuery, tapeRequest.Filter.Where)
		args = append(args, tapeRequest.Filter.Args...)
		args = append(args, clickhouse.Named("range_from", tapeRequest.From), clickhouse.Named("range_to", tapeRequest.To))
	} else {
		var condition string
		condition, args = rangeCondition(tapeRequest.Filter, tapeRequest.From, tapeRequest.To)
		statement = rawTapeSelect + condition + rawTapeGroup
	}
	args = append(args, clickhouse.Named("step_seconds", uint32(tapeRequest.StepSeconds)))

	rows, err := reader.connection.Query(ctx, statement, args...)
	if err != nil {
		return nil, classifyError(err)
	}
	defer rows.Close()

	buckets := []TapeBucket{}
	for rows.Next() {
		var bucketSeconds uint64
		var bucket TapeBucket
		if err := rows.Scan(&bucketSeconds, &bucket.Status2xx, &bucket.Status3xx, &bucket.Status4xx, &bucket.Status5xx, &bucket.Status0); err != nil {
			return nil, classifyError(err)
		}
		bucket.Start = time.Unix(int64(bucketSeconds), 0).UTC()
		buckets = append(buckets, bucket)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyError(err)
	}
	return buckets, nil
}

func (reader *Reader) Facets(ctx context.Context, facetRequest FacetRequest) ([]FacetValue, error) {
	if !facetColumns[facetRequest.Field] {
		return nil, ErrUnknownFacetField
	}
	column := facetRequest.Field
	statement := "SELECT " + column + " AS value, count() AS requests\n" +
		"FROM requests\n" +
		"WHERE ts >= {range_from:DateTime64(3)} AND ts < {range_to:DateTime64(3)} AND " + column + " != '' AND startsWith(lower(" + column + "), lower({prefix:String}))\n" +
		"GROUP BY value ORDER BY requests DESC, value LIMIT 20"

	rows, err := reader.connection.Query(ctx, statement,
		clickhouse.Named("range_from", facetRequest.From),
		clickhouse.Named("range_to", facetRequest.To),
		clickhouse.Named("prefix", facetRequest.Prefix),
	)
	if err != nil {
		return nil, classifyError(err)
	}
	defer rows.Close()

	values := []FacetValue{}
	for rows.Next() {
		var value FacetValue
		if err := rows.Scan(&value.Value, &value.Count); err != nil {
			return nil, classifyError(err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyError(err)
	}
	return values, nil
}
