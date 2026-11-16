package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/bodies"
	"github.com/dilutedev/scrapelog/ingest-api/internal/query"
)

type RequestReader interface {
	Search(ctx context.Context, searchRequest query.SearchRequest) ([]query.RequestSummary, error)
	Detail(ctx context.Context, key query.RequestKey) (query.RequestDetail, error)
	Tape(ctx context.Context, tapeRequest query.TapeRequest) ([]query.TapeBucket, error)
	Facets(ctx context.Context, facetRequest query.FacetRequest) ([]query.FacetValue, error)
	Bandwidth(ctx context.Context, bandwidthRequest query.BandwidthRequest) ([]query.BandwidthRow, error)
}

type BodyGetter interface {
	Get(ctx context.Context, key string) ([]byte, error)
}

const (
	bodyFetchTimeout    = 10 * time.Second
	defaultSearchLimit  = 100
	maxSearchLimit      = 500
	rawRetention        = 14 * 24 * time.Hour
	maxTapeBuckets      = 240
	defaultRange        = time.Hour
	hourSeconds         = 3600
	daySeconds          = 86400
	retentionNote       = "Successful (2xx/3xx) requests are kept for 14 days; older results show only failures."
	queryTimeoutMessage = "ClickHouse took longer than 60 s. Narrow the time range or add a domain filter."
	bodyReadFailMessage = "reading the stored body failed"

	maxBandwidthBuckets = 2400
	otherGroupKey       = "Other"
)

var tapeStepLadder = []int64{1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400}

type searchResponse struct {
	Rows       []query.RequestSummary `json:"rows"`
	NextCursor *string                `json:"next_cursor"`
	Note       string                 `json:"note,omitempty"`
}

type tapeResponse struct {
	StepSeconds int64              `json:"step_seconds"`
	Buckets     []query.TapeBucket `json:"buckets"`
	Approximate bool               `json:"approximate,omitempty"`
}

type bandwidthSeries struct {
	Key    string     `json:"key"`
	Points [][2]int64 `json:"points"`
}

type bandwidthGroup struct {
	Key       string `json:"key"`
	Bytes     uint64 `json:"bytes"`
	Responses uint64 `json:"responses"`
}

type bandwidthResponse struct {
	From           time.Time         `json:"from"`
	To             time.Time         `json:"to"`
	StepSeconds    int64             `json:"step_seconds"`
	TotalBytes     uint64            `json:"total_bytes"`
	TotalResponses uint64            `json:"total_responses"`
	Series         []bandwidthSeries `json:"series"`
	Groups         []bandwidthGroup  `json:"groups"`
}

type facetsResponse struct {
	Values []query.FacetValue `json:"values"`
}

type cursorPayload struct {
	Milliseconds int64  `json:"t"`
	RequestID    string `json:"r"`
}

func parseTimestamp(text string) (time.Time, error) {
	parsedTime, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, err
	}
	return parsedTime.UTC().Truncate(time.Millisecond), nil
}

func parseTimeRange(request *http.Request, now time.Time) (time.Time, time.Time, error) {
	rangeEnd := now.UTC().Truncate(time.Millisecond)
	if toText := request.URL.Query().Get("to"); toText != "" {
		parsedEnd, err := parseTimestamp(toText)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("from and to must be RFC 3339 times")
		}
		rangeEnd = parsedEnd
	}
	rangeStart := rangeEnd.Add(-defaultRange)
	if fromText := request.URL.Query().Get("from"); fromText != "" {
		parsedStart, err := parseTimestamp(fromText)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("from and to must be RFC 3339 times")
		}
		rangeStart = parsedStart
	}
	if !rangeStart.Before(rangeEnd) {
		return time.Time{}, time.Time{}, errors.New("from must be before to")
	}
	return rangeStart, rangeEnd, nil
}

func encodeCursor(position query.Position) string {
	encoded, _ := json.Marshal(cursorPayload{Milliseconds: position.Timestamp.UnixMilli(), RequestID: position.RequestID})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeCursor(cursor string) (query.Position, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return query.Position{}, errors.New("invalid cursor")
	}
	var payload cursorPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return query.Position{}, errors.New("invalid cursor")
	}
	return query.Position{Timestamp: time.UnixMilli(payload.Milliseconds).UTC(), RequestID: payload.RequestID}, nil
}

func tapeStep(span time.Duration, hourly bool) int64 {
	spanSeconds := int64((span + time.Second - 1) / time.Second)
	step := int64(0)
	for _, candidate := range tapeStepLadder {
		if (spanSeconds+candidate-1)/candidate <= maxTapeBuckets {
			step = candidate
			break
		}
	}
	if step == 0 {
		rawStep := (spanSeconds + maxTapeBuckets - 1) / maxTapeBuckets
		step = (rawStep + daySeconds - 1) / daySeconds * daySeconds
	}
	if hourly && step < hourSeconds {
		step = hourSeconds
	}
	return step
}

func writeReaderError(logger *slog.Logger, responseWriter http.ResponseWriter, err error, logMessage string) {
	if errors.Is(err, query.ErrQueryTimeout) {
		writeError(responseWriter, http.StatusGatewayTimeout, queryTimeoutMessage)
		return
	}
	logger.Error(logMessage, "error", err)
	writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
}

func parseRequestKey(request *http.Request) (query.RequestKey, error) {
	values := request.URL.Query()
	project, domain, timestampText, requestID := values.Get("project"), values.Get("domain"), values.Get("ts"), values.Get("request_id")
	if project == "" || domain == "" || timestampText == "" || requestID == "" {
		return query.RequestKey{}, errors.New("project, domain, ts and request_id are required")
	}
	timestamp, err := parseTimestamp(timestampText)
	if err != nil {
		return query.RequestKey{}, errors.New("ts must be an RFC 3339 time")
	}
	return query.RequestKey{Project: project, Domain: domain, Timestamp: timestamp, RequestID: requestID}, nil
}

func searchRequestsHandler(logger *slog.Logger, requestReader RequestReader) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		now := time.Now()
		rangeStart, rangeEnd, err := parseTimeRange(request, now)
		if err != nil {
			writeError(responseWriter, http.StatusBadRequest, err.Error())
			return
		}
		compiled, err := query.Compile(request.URL.Query().Get("q"))
		if err != nil {
			writeError(responseWriter, http.StatusBadRequest, err.Error())
			return
		}
		limit := defaultSearchLimit
		if limitText := request.URL.Query().Get("limit"); limitText != "" {
			parsedLimit, err := strconv.Atoi(limitText)
			if err != nil || parsedLimit < 1 {
				writeError(responseWriter, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			limit = min(parsedLimit, maxSearchLimit)
		}
		searchRequest := query.SearchRequest{Filter: compiled.Raw, From: rangeStart, To: rangeEnd, Limit: limit + 1}
		if cursorText := request.URL.Query().Get("cursor"); cursorText != "" {
			position, err := decodeCursor(cursorText)
			if err != nil {
				writeError(responseWriter, http.StatusBadRequest, err.Error())
				return
			}
			searchRequest.After = &position
		}

		rows, err := requestReader.Search(request.Context(), searchRequest)
		if err != nil {
			writeReaderError(logger, responseWriter, err, "searching requests failed")
			return
		}
		response := searchResponse{Rows: rows}
		if len(rows) > limit {
			response.Rows = rows[:limit]
			nextCursor := encodeCursor(query.Position{Timestamp: rows[limit-1].Timestamp, RequestID: rows[limit-1].RequestID})
			response.NextCursor = &nextCursor
		}
		if rangeStart.Before(now.Add(-rawRetention)) {
			response.Note = retentionNote
		}
		writeJSON(responseWriter, http.StatusOK, response)
	}
}

func requestDetailHandler(logger *slog.Logger, requestReader RequestReader) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		key, err := parseRequestKey(request)
		if err != nil {
			writeError(responseWriter, http.StatusBadRequest, err.Error())
			return
		}
		detail, err := requestReader.Detail(request.Context(), key)
		if errors.Is(err, query.ErrRequestNotFound) {
			writeError(responseWriter, http.StatusNotFound, "request not found")
			return
		}
		if err != nil {
			writeReaderError(logger, responseWriter, err, "reading request detail failed")
			return
		}
		writeJSON(responseWriter, http.StatusOK, detail)
	}
}

func requestBodyHandler(logger *slog.Logger, requestReader RequestReader, bodyGetter BodyGetter) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if bodyGetter == nil {
			writeError(responseWriter, http.StatusNotFound, "Response bodies are not stored: S3 is not configured.")
			return
		}
		key, err := parseRequestKey(request)
		if err != nil {
			writeError(responseWriter, http.StatusBadRequest, err.Error())
			return
		}
		detail, err := requestReader.Detail(request.Context(), key)
		if errors.Is(err, query.ErrRequestNotFound) {
			writeError(responseWriter, http.StatusNotFound, "request not found")
			return
		}
		if err != nil {
			writeReaderError(logger, responseWriter, err, "reading request detail failed")
			return
		}
		if detail.BodyKey == "" {
			writeError(responseWriter, http.StatusNotFound, "This request has no stored response body.")
			return
		}
		fetchContext, cancelFetch := context.WithTimeout(request.Context(), bodyFetchTimeout)
		defer cancelFetch()
		gzippedBody, err := bodyGetter.Get(fetchContext, detail.BodyKey)
		if errors.Is(err, bodies.ErrBodyNotFound) {
			writeError(responseWriter, http.StatusNotFound, "The stored response body has expired or was deleted.")
			return
		}
		if err != nil {
			logger.Error("fetching stored body failed", "error", err)
			writeError(responseWriter, http.StatusBadGateway, bodyReadFailMessage)
			return
		}
		plainBody, err := gunzipBytes(gzippedBody)
		if err != nil {
			logger.Error("decompressing stored body failed", "error", err)
			writeError(responseWriter, http.StatusBadGateway, bodyReadFailMessage)
			return
		}
		responseWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")
		responseWriter.Write(plainBody)
	}
}

func gunzipBytes(gzippedBody []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(gzippedBody))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func tapeHandler(logger *slog.Logger, requestReader RequestReader) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		now := time.Now()
		rangeStart, rangeEnd, err := parseTimeRange(request, now)
		if err != nil {
			writeError(responseWriter, http.StatusBadRequest, err.Error())
			return
		}
		compiled, err := query.Compile(request.URL.Query().Get("q"))
		if err != nil {
			writeError(responseWriter, http.StatusBadRequest, err.Error())
			return
		}
		hourly := rangeStart.Before(now.Add(-rawRetention))
		step := tapeStep(rangeEnd.Sub(rangeStart), hourly)
		alignedStartSeconds := rangeStart.Unix() / step * step
		alignedStart := time.Unix(alignedStartSeconds, 0).UTC()
		tapeRequest := query.TapeRequest{Hourly: hourly, StepSeconds: step, To: rangeEnd}
		if hourly {
			tapeRequest.Filter = compiled.Hourly
			tapeRequest.From = alignedStart
		} else {
			tapeRequest.Filter = compiled.Raw
			tapeRequest.From = rangeStart
		}

		fetchedBuckets, err := requestReader.Tape(request.Context(), tapeRequest)
		if err != nil {
			writeReaderError(logger, responseWriter, err, "reading tape failed")
			return
		}
		bucketsByStart := make(map[int64]query.TapeBucket, len(fetchedBuckets))
		for _, fetchedBucket := range fetchedBuckets {
			bucketsByStart[fetchedBucket.Start.Unix()] = fetchedBucket
		}
		filledBuckets := []query.TapeBucket{}
		for bucketStart := alignedStart; bucketStart.Before(rangeEnd); bucketStart = bucketStart.Add(time.Duration(step) * time.Second) {
			bucket := bucketsByStart[bucketStart.Unix()]
			bucket.Start = bucketStart
			filledBuckets = append(filledBuckets, bucket)
		}
		writeJSON(responseWriter, http.StatusOK, tapeResponse{StepSeconds: step, Buckets: filledBuckets, Approximate: hourly && compiled.Approximate})
	}
}

func facetsHandler(logger *slog.Logger, requestReader RequestReader) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		rangeStart, rangeEnd, err := parseTimeRange(request, time.Now())
		if err != nil {
			writeError(responseWriter, http.StatusBadRequest, err.Error())
			return
		}
		values, err := requestReader.Facets(request.Context(), query.FacetRequest{
			Field:  request.URL.Query().Get("field"),
			Prefix: request.URL.Query().Get("prefix"),
			From:   rangeStart,
			To:     rangeEnd,
		})
		if errors.Is(err, query.ErrUnknownFacetField) {
			writeError(responseWriter, http.StatusBadRequest, "field must be one of domain, scraper, project, proxy, tls_profile, method")
			return
		}
		if err != nil {
			writeReaderError(logger, responseWriter, err, "reading facets failed")
			return
		}
		writeJSON(responseWriter, http.StatusOK, facetsResponse{Values: values})
	}
}

type bandwidthAccumulator struct {
	key            string
	bytes          uint64
	responses      uint64
	bytesPerBucket []int64
}

func buildBandwidthResponse(rows []query.BandwidthRow, alignedFrom, alignedTo time.Time, stepSeconds int64) bandwidthResponse {
	bucketCount := int((alignedTo.Unix() - alignedFrom.Unix()) / stepSeconds)
	response := bandwidthResponse{From: alignedFrom, To: alignedTo, StepSeconds: stepSeconds, Series: []bandwidthSeries{}, Groups: []bandwidthGroup{}}

	accumulatorsByKey := map[string]*bandwidthAccumulator{}
	var otherAccumulator *bandwidthAccumulator
	orderedAccumulators := []*bandwidthAccumulator{}
	for _, row := range rows {
		response.TotalBytes += row.Bytes
		response.TotalResponses += row.Responses
		var accumulator *bandwidthAccumulator
		if row.Other {
			if otherAccumulator == nil {
				otherAccumulator = &bandwidthAccumulator{key: otherGroupKey, bytesPerBucket: make([]int64, bucketCount)}
			}
			accumulator = otherAccumulator
		} else {
			accumulator = accumulatorsByKey[row.Key]
			if accumulator == nil {
				accumulator = &bandwidthAccumulator{key: row.Key, bytesPerBucket: make([]int64, bucketCount)}
				accumulatorsByKey[row.Key] = accumulator
				orderedAccumulators = append(orderedAccumulators, accumulator)
			}
		}
		accumulator.bytes += row.Bytes
		accumulator.responses += row.Responses
		bucketOffset := row.Start.Unix() - alignedFrom.Unix()
		if bucketOffset >= 0 && bucketOffset/stepSeconds < int64(bucketCount) {
			accumulator.bytesPerBucket[bucketOffset/stepSeconds] += int64(row.Bytes)
		}
	}
	sort.Slice(orderedAccumulators, func(first, second int) bool {
		if orderedAccumulators[first].bytes != orderedAccumulators[second].bytes {
			return orderedAccumulators[first].bytes > orderedAccumulators[second].bytes
		}
		return orderedAccumulators[first].key < orderedAccumulators[second].key
	})
	if otherAccumulator != nil {
		orderedAccumulators = append(orderedAccumulators, otherAccumulator)
	}

	for _, accumulator := range orderedAccumulators {
		points := make([][2]int64, bucketCount)
		for bucketIndex := range points {
			points[bucketIndex] = [2]int64{alignedFrom.Unix() + int64(bucketIndex)*stepSeconds, accumulator.bytesPerBucket[bucketIndex]}
		}
		response.Series = append(response.Series, bandwidthSeries{Key: accumulator.key, Points: points})
		response.Groups = append(response.Groups, bandwidthGroup{Key: accumulator.key, Bytes: accumulator.bytes, Responses: accumulator.responses})
	}
	return response
}

func bandwidthHandler(logger *slog.Logger, requestReader RequestReader) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		rangeStart, rangeEnd, err := parseTimeRange(request, time.Now())
		if err != nil {
			writeError(responseWriter, http.StatusBadRequest, err.Error())
			return
		}
		var stepSeconds int64
		switch request.URL.Query().Get("step") {
		case "", "hour":
			stepSeconds = hourSeconds
		case "day":
			stepSeconds = daySeconds
		default:
			writeError(responseWriter, http.StatusBadRequest, "step must be hour or day")
			return
		}
		split := request.URL.Query().Get("split")
		if split == "" {
			split = "proxy"
		}
		alignedFromSeconds := rangeStart.Unix() / stepSeconds * stepSeconds
		alignedToSeconds := (rangeEnd.Unix() + stepSeconds - 1) / stepSeconds * stepSeconds
		if (alignedToSeconds-alignedFromSeconds)/stepSeconds > maxBandwidthBuckets {
			writeError(responseWriter, http.StatusBadRequest, "the range has too many points; use step=day or a shorter range")
			return
		}
		alignedFrom := time.Unix(alignedFromSeconds, 0).UTC()
		alignedTo := time.Unix(alignedToSeconds, 0).UTC()

		rows, err := requestReader.Bandwidth(request.Context(), query.BandwidthRequest{
			Split:       split,
			Project:     request.URL.Query().Get("project"),
			From:        alignedFrom,
			To:          alignedTo,
			StepSeconds: stepSeconds,
		})
		if errors.Is(err, query.ErrUnknownBandwidthSplit) {
			writeError(responseWriter, http.StatusBadRequest, "split must be one of project, scraper, domain, proxy")
			return
		}
		if err != nil {
			writeReaderError(logger, responseWriter, err, "reading bandwidth failed")
			return
		}
		writeJSON(responseWriter, http.StatusOK, buildBandwidthResponse(rows, alignedFrom, alignedTo, stepSeconds))
	}
}
