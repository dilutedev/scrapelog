package ingest

import (
	"fmt"
	"testing"
	"time"
	"uuid"
)

var defaultRedactedHeaders = map[string]bool{"authorization": true, "cookie": true, "proxy-authorization": true}

var testReceivedAt = time.Date(2026, 11, 8, 12, 0, 0, 123000000, time.UTC)

func parse(rawRecord string) (Record, error) {
	return ParseRecord([]byte(rawRecord), "acme", testReceivedAt, defaultRedactedHeaders)
}

func TestParseRecordRejectsInvalidRecords(t *testing.T) {
	tests := map[string]struct {
		rawRecord string
		message   string
	}{
		"missing url":          {`{"method":"GET"}`, "url is required"},
		"blank url":            {`{"method":"GET","url":"  "}`, "url is required"},
		"relative url":         {`{"method":"GET","url":"/path"}`, "url must be an absolute URL"},
		"missing method":       {`{"url":"https://a.com"}`, "method is required"},
		"blank method":         {`{"url":"https://a.com","method":"  "}`, "method is required"},
		"status too high":      {`{"url":"https://a.com","method":"GET","status":1000}`, "status must be between 0 and 999"},
		"status negative":      {`{"url":"https://a.com","method":"GET","status":-1}`, "status must be between 0 and 999"},
		"status as string":     {`{"url":"https://a.com","method":"GET","status":"200"}`, "status must be a int"},
		"non-string header":    {`{"url":"https://a.com","method":"GET","request_headers":{"accept":5}}`, "request_headers.accept must be a string"},
		"string element":       {`"x"`, "record must be a JSON object"},
		"number element":       {`5`, "record must be a JSON object"},
		"ts too old":           {`{"url":"https://a.com","method":"GET","ts":"2026-09-08T11:59:59Z"}`, "ts is more than 60 days in the past"},
		"ts too far ahead":     {`{"url":"https://a.com","method":"GET","ts":"2026-11-08T14:00:00Z"}`, "ts is more than 1 hour in the future"},
		"ts not rfc 3339":      {`{"url":"https://a.com","method":"GET","ts":"yesterday"}`, "ts must be an RFC 3339 timestamp"},
		"ts without time zone": {`{"url":"https://a.com","method":"GET","ts":"2026-11-08T12:00:00"}`, "ts must be an RFC 3339 timestamp"},
	}
	for name, test := range tests {
		_, err := parse(test.rawRecord)
		if err == nil || err.Error() != test.message {
			t.Errorf("%s: got %v, want %q", name, err, test.message)
		}
	}
}

func TestParseRecordAcceptsBoundaries(t *testing.T) {
	for name, rawRecord := range map[string]string{
		"status 0":    `{"url":"https://a.com","method":"GET","status":0}`,
		"status 999":  `{"url":"https://a.com","method":"GET","status":999}`,
		"oldest ts":   fmt.Sprintf(`{"url":"https://a.com","method":"GET","ts":%q}`, testReceivedAt.Add(-maxTimestampAge).Format(time.RFC3339Nano)),
		"furthest ts": fmt.Sprintf(`{"url":"https://a.com","method":"GET","ts":%q}`, testReceivedAt.Add(maxTimestampAhead).Format(time.RFC3339Nano)),
		"null ts":     `{"url":"https://a.com","method":"GET","ts":null}`,
	} {
		if _, err := parse(rawRecord); err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		}
	}
}

func TestParseRecordDefaults(t *testing.T) {
	record, err := parse(`{"url":"https://a.com","method":"GET"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Timestamp.Equal(testReceivedAt) {
		t.Errorf("ts %v, want received_at", record.Timestamp)
	}
	parsedID, err := uuid.Parse(record.RequestID)
	if err != nil || parsedID[6]>>4 != 7 {
		t.Errorf("request id %q is not a UUIDv7", record.RequestID)
	}
	if record.RequestHeaders == nil || record.ResponseHeaders == nil || record.Tags == nil {
		t.Error("nil maps must be empty maps")
	}

	record, err = parse(`{"url":"https://a.com","method":"GET","request_id":"mine"}`)
	if err != nil || record.RequestID != "mine" {
		t.Errorf("supplied request id not kept: %q %v", record.RequestID, err)
	}
}

func TestParseRecordDerivesLowercaseDomain(t *testing.T) {
	tests := []struct {
		rawRecord string
		domain    string
	}{
		{`{"url":"https://Sub.Example.COM:8443/x","method":"GET"}`, "sub.example.com"},
		{`{"url":"https://a.com","method":"GET","domain":"Example.ORG"}`, "example.org"},
		{`{"url":"http://[::1]:80/","method":"GET"}`, "::1"},
	}
	for _, test := range tests {
		record, err := parse(test.rawRecord)
		if err != nil || record.Domain != test.domain {
			t.Errorf("%s: domain %q, err %v, want %q", test.rawRecord, record.Domain, err, test.domain)
		}
	}
}

func TestParseRecordStripsURLCredentials(t *testing.T) {
	tests := map[string]string{
		"https://user:pass@example.com/p?q=1": "https://example.com/p?q=1",
		"https://example.com/a%20b?x=%7e":     "https://example.com/a%20b?x=%7e",
	}
	for original, expected := range tests {
		record, err := parse(fmt.Sprintf(`{"url":%q,"method":"GET"}`, original))
		if err != nil || record.URL != expected {
			t.Errorf("%s: got %q, err %v, want %q", original, record.URL, err, expected)
		}
	}
}

func TestStripProxyCredentials(t *testing.T) {
	tests := map[string]string{
		"http://u:p@1.2.3.4:8080": "http://1.2.3.4:8080",
		"u:p@host:1":              "host:1",
		"u:p@ss@host:1":           "host:1",
		"residential-1":           "residential-1",
		"":                        "",
	}
	for proxy, expected := range tests {
		if got := stripProxyCredentials(proxy); got != expected {
			t.Errorf("%q: got %q, want %q", proxy, got, expected)
		}
	}
}

func TestParseRecordRedactsHeadersInBothMaps(t *testing.T) {
	record, err := parse(`{"url":"https://a.com","method":"GET",
		"request_headers":{"Authorization":"Bearer t","COOKIE":"c","proxy-authorization":"p","Accept":"*/*"},
		"response_headers":{"Cookie":"c","Content-Type":"text/html"}}`)
	if err != nil {
		t.Fatal(err)
	}
	expectedRequest := map[string]string{"Authorization": "[REDACTED]", "COOKIE": "[REDACTED]", "proxy-authorization": "[REDACTED]", "Accept": "*/*"}
	for name, value := range expectedRequest {
		if record.RequestHeaders[name] != value {
			t.Errorf("request header %s: got %q, want %q", name, record.RequestHeaders[name], value)
		}
	}
	if record.ResponseHeaders["Cookie"] != "[REDACTED]" || record.ResponseHeaders["Content-Type"] != "text/html" {
		t.Errorf("unexpected response headers %v", record.ResponseHeaders)
	}
}

func TestParseRecordProjectComesFromArgument(t *testing.T) {
	record, err := parse(`{"url":"https://a.com","method":"GET","project":"other"}`)
	if err != nil || record.Project != "acme" {
		t.Fatalf("project %q, err %v", record.Project, err)
	}
}
