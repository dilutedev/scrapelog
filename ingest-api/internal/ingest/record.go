package ingest

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"uuid"
)

const (
	maxTimestampAge   = 60 * 24 * time.Hour
	maxTimestampAhead = time.Hour
	redactedValue     = "[REDACTED]"

	bodyEncodingUTF8   = "utf8"
	bodyEncodingBase64 = "base64"
)

type recordInput struct {
	Timestamp       time.Time         `json:"ts"`
	Scraper         string            `json:"scraper"`
	RequestID       string            `json:"request_id"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	Domain          string            `json:"domain"`
	Status          int               `json:"status"`
	LatencyMs       uint32            `json:"latency_ms"`
	Proxy           string            `json:"proxy"`
	HTTPVersion     string            `json:"http_version"`
	TLSJA3          string            `json:"tls_ja3"`
	TLSJA4          string            `json:"tls_ja4"`
	TLSProfile      string            `json:"tls_profile"`
	RequestHeaders  map[string]string `json:"request_headers"`
	ResponseHeaders map[string]string `json:"response_headers"`
	ResponseSize    uint32            `json:"response_size"`
	Error           string            `json:"error"`
	Tags            map[string]string `json:"tags"`

	RequestBody         string `json:"request_body"`
	RequestBodyEncoding string `json:"request_body_encoding"`
}

type Record struct {
	ReceivedAt      time.Time
	Timestamp       time.Time
	Project         string
	Scraper         string
	RequestID       string
	Method          string
	URL             string
	Domain          string
	Status          uint16
	LatencyMs       uint32
	Proxy           string
	HTTPVersion     string
	TLSJA3          string
	TLSJA4          string
	TLSProfile      string
	RequestHeaders  map[string]string
	ResponseHeaders map[string]string
	ResponseSize    uint32
	BodyKey         string
	Error           string
	Tags            map[string]string

	RequestBody         string
	RequestBodyEncoding string
}

func ParseRecord(rawRecord []byte, project string, receivedAt time.Time, redactedHeaders map[string]bool, maxBodyBytes int64) (Record, error) {
	var input recordInput
	if err := json.Unmarshal(rawRecord, &input); err != nil {
		var syntaxError *json.SyntaxError
		if errors.As(err, &syntaxError) {
			return Record{}, errors.New("record is not valid JSON")
		}
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) {
			if typeError.Field == "" {
				return Record{}, errors.New("record must be a JSON object")
			}
			return Record{}, fmt.Errorf("%s must be a %s", typeError.Field, typeError.Type)
		}
		return Record{}, errors.New("ts must be an RFC 3339 timestamp")
	}

	if strings.TrimSpace(input.URL) == "" {
		return Record{}, errors.New("url is required")
	}
	parsedURL, err := url.Parse(input.URL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return Record{}, errors.New("url must be an absolute URL")
	}
	if strings.TrimSpace(input.Method) == "" {
		return Record{}, errors.New("method is required")
	}
	if input.Status < 0 || input.Status > 999 {
		return Record{}, errors.New("status must be between 0 and 999")
	}

	requestBodyEncoding, err := validateRequestBody(input.RequestBody, input.RequestBodyEncoding, maxBodyBytes)
	if err != nil {
		return Record{}, err
	}

	timestamp := input.Timestamp
	if timestamp.IsZero() {
		timestamp = receivedAt
	}
	if timestamp.Before(receivedAt.Add(-maxTimestampAge)) {
		return Record{}, errors.New("ts is more than 60 days in the past")
	}
	if timestamp.After(receivedAt.Add(maxTimestampAhead)) {
		return Record{}, errors.New("ts is more than 1 hour in the future")
	}

	domain := strings.ToLower(input.Domain)
	if domain == "" {
		domain = strings.ToLower(parsedURL.Hostname())
	}
	requestID := input.RequestID
	if requestID == "" {
		requestID = uuid.NewV7().String()
	}

	return Record{
		ReceivedAt:      receivedAt,
		Timestamp:       timestamp.UTC(),
		Project:         project,
		Scraper:         input.Scraper,
		RequestID:       requestID,
		Method:          input.Method,
		URL:             stripURLCredentials(parsedURL, input.URL),
		Domain:          domain,
		Status:          uint16(input.Status),
		LatencyMs:       input.LatencyMs,
		Proxy:           stripProxyCredentials(input.Proxy),
		HTTPVersion:     input.HTTPVersion,
		TLSJA3:          input.TLSJA3,
		TLSJA4:          input.TLSJA4,
		TLSProfile:      input.TLSProfile,
		RequestHeaders:  redactHeaders(input.RequestHeaders, redactedHeaders),
		ResponseHeaders: redactHeaders(input.ResponseHeaders, redactedHeaders),
		ResponseSize:    input.ResponseSize,
		Error:           input.Error,
		Tags:            copyMap(input.Tags),

		RequestBody:         input.RequestBody,
		RequestBodyEncoding: requestBodyEncoding,
	}, nil
}

func validateRequestBody(requestBody string, encoding string, maxBodyBytes int64) (string, error) {
	if encoding == "" {
		encoding = bodyEncodingUTF8
	}
	bodySize := int64(len(requestBody))
	switch encoding {
	case bodyEncodingUTF8:
	case bodyEncodingBase64:
		decodedBody, err := base64.StdEncoding.DecodeString(requestBody)
		if err != nil {
			return "", errors.New("request_body is not valid base64")
		}
		bodySize = int64(len(decodedBody))
	default:
		return "", errors.New("request_body_encoding must be utf8 or base64")
	}
	if bodySize > maxBodyBytes {
		return "", fmt.Errorf("request_body exceeds MAX_BODY_BYTES (%d bytes)", maxBodyBytes)
	}
	return encoding, nil
}

func stripURLCredentials(parsedURL *url.URL, original string) string {
	if parsedURL.User == nil {
		return original
	}
	parsedURL.User = nil
	return parsedURL.String()
}

func stripProxyCredentials(proxy string) string {
	prefix := ""
	rest := proxy
	if index := strings.Index(proxy, "://"); index >= 0 {
		prefix = proxy[:index+3]
		rest = proxy[index+3:]
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		return prefix + rest[at+1:]
	}
	return proxy
}

func redactHeaders(headers map[string]string, redactedHeaders map[string]bool) map[string]string {
	result := make(map[string]string, len(headers))
	for name, value := range headers {
		if redactedHeaders[strings.ToLower(name)] {
			value = redactedValue
		}
		result[name] = value
	}
	return result
}

func copyMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
