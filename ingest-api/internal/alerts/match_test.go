package alerts

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dilutedev/scrapelog/ingest-api/internal/ingest"
)

func sampleRecord() ingest.Record {
	return ingest.Record{
		Timestamp:       time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC),
		Project:         "shop",
		Scraper:         "crawler-1",
		RequestID:       "req-1",
		Method:          "GET",
		URL:             "https://example.com/products?page=2",
		Domain:          "example.com",
		Status:          403,
		LatencyMs:       1500,
		Proxy:           "proxy-a",
		TLSProfile:      "chrome",
		RequestHeaders:  map[string]string{"Accept": "text/html"},
		ResponseHeaders: map[string]string{"X-Cache": "MISS"},
		ResponseSize:    2048,
		Error:           "connection reset",
		Tags:            map[string]string{"batch": "nightly"},
		Body:            []byte("<html>Please solve the Captcha to continue</html>"),
	}
}

func compileSingle(t *testing.T, condition Condition) CompiledRule {
	t.Helper()
	compiled, err := Compile(validDiscordRule(condition))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return compiled
}

func TestMatchEvaluatesEachFieldAndOperator(t *testing.T) {
	tests := map[string]struct {
		condition Condition
		matches   bool
	}{
		"status equals hit":            {Condition{Field: "status", Operator: "equals", Value: "403"}, true},
		"status equals miss":           {Condition{Field: "status", Operator: "equals", Value: "200"}, false},
		"status not_equals hit":        {Condition{Field: "status", Operator: "not_equals", Value: "200"}, true},
		"status not_equals miss":       {Condition{Field: "status", Operator: "not_equals", Value: "403"}, false},
		"status between hit":           {Condition{Field: "status", Operator: "between", Value: "400..403"}, true},
		"status between miss":          {Condition{Field: "status", Operator: "between", Value: "200..299"}, false},
		"status greater_than hit":      {Condition{Field: "status", Operator: "greater_than", Value: "402"}, true},
		"status greater_than miss":     {Condition{Field: "status", Operator: "greater_than", Value: "403"}, false},
		"status less_than hit":         {Condition{Field: "status", Operator: "less_than", Value: "404"}, true},
		"status less_than miss":        {Condition{Field: "status", Operator: "less_than", Value: "403"}, false},
		"status class hit":             {Condition{Field: "status", Operator: "class", Value: "4xx"}, true},
		"status class miss":            {Condition{Field: "status", Operator: "class", Value: "5xx"}, false},
		"latency equals hit":           {Condition{Field: "latency_ms", Operator: "equals", Value: "1500"}, true},
		"latency equals miss":          {Condition{Field: "latency_ms", Operator: "equals", Value: "1"}, false},
		"latency between hit":          {Condition{Field: "latency_ms", Operator: "between", Value: "1000..2000"}, true},
		"latency between miss":         {Condition{Field: "latency_ms", Operator: "between", Value: "1..10"}, false},
		"latency greater_than hit":     {Condition{Field: "latency_ms", Operator: "greater_than", Value: "1000"}, true},
		"latency less_than miss":       {Condition{Field: "latency_ms", Operator: "less_than", Value: "1000"}, false},
		"response_size equals hit":     {Condition{Field: "response_size", Operator: "equals", Value: "2048"}, true},
		"response_size greater_than":   {Condition{Field: "response_size", Operator: "greater_than", Value: "4096"}, false},
		"response_size between hit":    {Condition{Field: "response_size", Operator: "between", Value: "2048..2048"}, true},
		"response_size less_than hit":  {Condition{Field: "response_size", Operator: "less_than", Value: "4096"}, true},
		"method equals hit":            {Condition{Field: "method", Operator: "equals", Value: "get"}, true},
		"method not_equals hit":        {Condition{Field: "method", Operator: "not_equals", Value: "POST"}, true},
		"method not_equals miss":       {Condition{Field: "method", Operator: "not_equals", Value: "GET"}, false},
		"domain equals hit":            {Condition{Field: "domain", Operator: "equals", Value: "example.com"}, true},
		"domain equals miss":           {Condition{Field: "domain", Operator: "equals", Value: "other.com"}, false},
		"scraper equals hit":           {Condition{Field: "scraper", Operator: "equals", Value: "crawler-1"}, true},
		"scraper not_equals hit":       {Condition{Field: "scraper", Operator: "not_equals", Value: "crawler-2"}, true},
		"proxy equals hit":             {Condition{Field: "proxy", Operator: "equals", Value: "proxy-a"}, true},
		"proxy equals miss":            {Condition{Field: "proxy", Operator: "equals", Value: "proxy-b"}, false},
		"tls_profile equals hit":       {Condition{Field: "tls_profile", Operator: "equals", Value: "chrome"}, true},
		"tls_profile not_equals miss":  {Condition{Field: "tls_profile", Operator: "not_equals", Value: "chrome"}, false},
		"url contains hit":             {Condition{Field: "url", Operator: "contains", Value: "page=2"}, true},
		"url contains miss":            {Condition{Field: "url", Operator: "contains", Value: "page=3"}, false},
		"url not_contains hit":         {Condition{Field: "url", Operator: "not_contains", Value: "login"}, true},
		"url not_contains miss":        {Condition{Field: "url", Operator: "not_contains", Value: "products"}, false},
		"url regex hit":                {Condition{Field: "url", Operator: "regex", Value: `page=\d+$`}, true},
		"url regex miss":               {Condition{Field: "url", Operator: "regex", Value: `^http://`}, false},
		"error contains hit":           {Condition{Field: "error", Operator: "contains", Value: "reset"}, true},
		"error not_contains miss":      {Condition{Field: "error", Operator: "not_contains", Value: "reset"}, false},
		"error regex hit":              {Condition{Field: "error", Operator: "regex", Value: "conn.*reset"}, true},
		"response_header equals hit":   {Condition{Field: "response_header", Name: "x-cache", Operator: "equals", Value: "miss"}, true},
		"response_header equals miss":  {Condition{Field: "response_header", Name: "x-cache", Operator: "equals", Value: "hit"}, false},
		"response_header contains hit": {Condition{Field: "response_header", Name: "X-Cache", Operator: "contains", Value: "mi"}, true},
		"response_header not_contains": {Condition{Field: "response_header", Name: "X-Cache", Operator: "not_contains", Value: "mi"}, false},
		"response_header regex hit":    {Condition{Field: "response_header", Name: "X-Cache", Operator: "regex", Value: "^MISS$"}, true},
		"response_header exists hit":   {Condition{Field: "response_header", Name: "X-Cache", Operator: "exists"}, true},
		"response_header missing miss": {Condition{Field: "response_header", Name: "X-Cache", Operator: "missing"}, false},
		"request_header equals hit":    {Condition{Field: "request_header", Name: "accept", Operator: "equals", Value: "text/html"}, true},
		"request_header contains miss": {Condition{Field: "request_header", Name: "accept", Operator: "contains", Value: "json"}, false},
		"request_header not_contains":  {Condition{Field: "request_header", Name: "accept", Operator: "not_contains", Value: "json"}, true},
		"request_header regex hit":     {Condition{Field: "request_header", Name: "accept", Operator: "regex", Value: "html"}, true},
		"request_header exists miss":   {Condition{Field: "request_header", Name: "cookie", Operator: "exists"}, false},
		"request_header missing hit":   {Condition{Field: "request_header", Name: "cookie", Operator: "missing"}, true},
		"tag equals hit":               {Condition{Field: "tag", Name: "batch", Operator: "equals", Value: "nightly"}, true},
		"tag equals wrong name case":   {Condition{Field: "tag", Name: "Batch", Operator: "equals", Value: "nightly"}, false},
		"tag contains hit":             {Condition{Field: "tag", Name: "batch", Operator: "contains", Value: "night"}, true},
		"tag not_contains miss":        {Condition{Field: "tag", Name: "batch", Operator: "not_contains", Value: "night"}, false},
		"tag regex hit":                {Condition{Field: "tag", Name: "batch", Operator: "regex", Value: "^night"}, true},
		"tag exists hit":               {Condition{Field: "tag", Name: "batch", Operator: "exists"}, true},
		"tag missing hit":              {Condition{Field: "tag", Name: "other", Operator: "missing"}, true},
		"body contains hit":            {Condition{Field: "body", Operator: "contains", Value: "captcha"}, true},
		"body contains miss":           {Condition{Field: "body", Operator: "contains", Value: "welcome"}, false},
		"body not_contains hit":        {Condition{Field: "body", Operator: "not_contains", Value: "welcome"}, true},
		"body not_contains miss":       {Condition{Field: "body", Operator: "not_contains", Value: "captcha"}, false},
		"body regex hit":               {Condition{Field: "body", Operator: "regex", Value: "solve .* Captcha"}, true},
		"body regex miss":              {Condition{Field: "body", Operator: "regex", Value: "^Please"}, false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, matched := compileSingle(t, test.condition).Match(sampleRecord())
			if matched != test.matches {
				t.Fatalf("matched = %v, want %v", matched, test.matches)
			}
		})
	}
}

func TestMatchIsCaseInsensitiveExceptRegex(t *testing.T) {
	tests := map[string]struct {
		condition Condition
		matches   bool
	}{
		"equals":              {Condition{Field: "method", Operator: "equals", Value: "get"}, true},
		"contains":            {Condition{Field: "url", Operator: "contains", Value: "PRODUCTS"}, true},
		"regex case mismatch": {Condition{Field: "body", Operator: "regex", Value: "captcha"}, false},
		"regex exact case":    {Condition{Field: "body", Operator: "regex", Value: "Captcha"}, true},
		"regex inline flag":   {Condition{Field: "body", Operator: "regex", Value: "(?i)CAPTCHA"}, true},
		"header name":         {Condition{Field: "response_header", Name: "X-CACHE", Operator: "exists"}, true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, matched := compileSingle(t, test.condition).Match(sampleRecord())
			if matched != test.matches {
				t.Fatalf("matched = %v, want %v", matched, test.matches)
			}
		})
	}
}

func TestMatchHandlesAbsentHeadersAndBodies(t *testing.T) {
	record := sampleRecord()
	record.Body = nil
	tests := map[string]struct {
		condition Condition
		matches   bool
	}{
		"missing header equals":       {Condition{Field: "response_header", Name: "x-none", Operator: "equals", Value: "a"}, false},
		"missing header contains":     {Condition{Field: "response_header", Name: "x-none", Operator: "contains", Value: "a"}, false},
		"missing header not_contains": {Condition{Field: "response_header", Name: "x-none", Operator: "not_contains", Value: "a"}, true},
		"missing header regex":        {Condition{Field: "response_header", Name: "x-none", Operator: "regex", Value: "a"}, false},
		"missing header exists":       {Condition{Field: "response_header", Name: "x-none", Operator: "exists"}, false},
		"missing header missing":      {Condition{Field: "response_header", Name: "x-none", Operator: "missing"}, true},
		"nil body contains":           {Condition{Field: "body", Operator: "contains", Value: "a"}, false},
		"nil body regex":              {Condition{Field: "body", Operator: "regex", Value: "a"}, false},
		"nil body not_contains":       {Condition{Field: "body", Operator: "not_contains", Value: "a"}, true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, matched := compileSingle(t, test.condition).Match(record)
			if matched != test.matches {
				t.Fatalf("matched = %v, want %v", matched, test.matches)
			}
		})
	}
}

func TestMatchHonorsEmptyHeaderValueForExists(t *testing.T) {
	record := sampleRecord()
	record.ResponseHeaders = map[string]string{"X-Empty": ""}
	_, matched := compileSingle(t, Condition{Field: "response_header", Name: "x-empty", Operator: "exists"}).Match(record)
	if !matched {
		t.Fatal("exists should pass for a header with an empty value")
	}
}

func TestMatchStopsBeforeBodyWhenCheaperConditionFails(t *testing.T) {
	rule, err := Compile(validDiscordRule(
		Condition{Field: "body", Operator: "contains", Value: "captcha"},
		Condition{Field: "status", Operator: "equals", Value: "200"},
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, matched := rule.Match(sampleRecord()); matched {
		t.Fatal("status mismatch must prevent a match")
	}
}

func TestMatchScansOnlyFirstMebibyteOfBody(t *testing.T) {
	rule := compileSingle(t, Condition{Field: "body", Operator: "contains", Value: "needle"})
	tests := map[string]struct {
		offset  int
		matches bool
	}{
		"before the limit": {maxBodyScanBytes - 100, true},
		"at the limit":     {maxBodyScanBytes, false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			record := sampleRecord()
			record.Body = append(bytes.Repeat([]byte("a"), test.offset), []byte("needle")...)
			record.Body = append(record.Body, bytes.Repeat([]byte("a"), 200)...)
			_, matched := rule.Match(record)
			if matched != test.matches {
				t.Fatalf("matched = %v, want %v", matched, test.matches)
			}
		})
	}
}

func TestMatchReturnsSnippetAroundHit(t *testing.T) {
	rule := compileSingle(t, Condition{Field: "body", Operator: "contains", Value: "needle"})
	record := sampleRecord()
	record.Body = []byte(strings.Repeat("é", 300) + "needle" + strings.Repeat("é", 300))
	result, matched := rule.Match(record)
	if !matched {
		t.Fatal("expected a match")
	}
	if !strings.Contains(result.BodySnippet, "needle") {
		t.Fatalf("snippet lacks the hit: %q", result.BodySnippet)
	}
	if len(result.BodySnippet) > maxSnippetBytes {
		t.Fatalf("snippet is %d bytes", len(result.BodySnippet))
	}
	if !utf8.ValidString(result.BodySnippet) {
		t.Fatalf("snippet is not valid UTF-8: %q", result.BodySnippet)
	}

	nonBodyResult, matched := compileSingle(t, Condition{Field: "status", Operator: "equals", Value: "403"}).Match(sampleRecord())
	if !matched || nonBodyResult.BodySnippet != "" {
		t.Fatalf("non-body match should have an empty snippet, got %q", nonBodyResult.BodySnippet)
	}
}

func TestMatchRespectsRuleProjectAndCopiesFields(t *testing.T) {
	rule := validDiscordRule(Condition{Field: "status", Operator: "equals", Value: "403"})
	rule.RuleID = "rule-1"
	rule.Project = "shop"
	compiled, err := Compile(rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result, matched := compiled.Match(sampleRecord())
	if !matched {
		t.Fatal("expected a match")
	}
	record := sampleRecord()
	want := Match{
		RuleID:    "rule-1",
		Project:   "shop",
		Timestamp: record.Timestamp,
		RequestID: "req-1",
		Method:    "GET",
		URL:       record.URL,
		Domain:    "example.com",
		Status:    403,
		LatencyMs: 1500,
	}
	if result != want {
		t.Fatalf("unexpected match: %+v", result)
	}

	otherProject := sampleRecord()
	otherProject.Project = "blog"
	if _, matched := compiled.Match(otherProject); matched {
		t.Fatal("rule scoped to shop must not match blog")
	}

	rule.Project = ""
	anyProject, err := Compile(rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, matched := anyProject.Match(otherProject); !matched {
		t.Fatal("rule without project must match every project")
	}
}
