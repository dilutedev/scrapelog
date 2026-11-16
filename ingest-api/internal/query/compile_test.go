package query

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"
)

const invalidFieldSuffix = "; valid fields: domain, error, http, id, ja3, ja4, latency, method, project, proxy, req.<header>, res.<header>, scraper, size, status, tag.<name>, tls, url"

func contains(column string, parameter string) string {
	return fmt.Sprintf("positionCaseInsensitive(%s, {%s:String}) > 0", column, parameter)
}

func headerExact(column string, nameParameter string, valueParameter string) string {
	return fmt.Sprintf("arrayExists((k, v) -> lower(k) = lower({%s:String}) AND v = {%s:String}, mapKeys(%s), mapValues(%s))", nameParameter, valueParameter, column, column)
}

func headerContains(column string, nameParameter string, valueParameter string) string {
	return fmt.Sprintf("arrayExists((k, v) -> lower(k) = lower({%s:String}) AND positionCaseInsensitive(v, {%s:String}) > 0, mapKeys(%s), mapValues(%s))", nameParameter, valueParameter, column, column)
}

func named(name string, value any) any {
	return clickhouse.Named(name, value)
}

func TestCompileProducesParameterizedSQL(t *testing.T) {
	tests := map[string]struct {
		input           string
		wantRawWhere    string
		wantRawArgs     []any
		wantHourlyWhere string
		wantHourlyArgs  []any
		wantApproximate bool
	}{
		"empty": {
			input:           "   ",
			wantRawWhere:    "1",
			wantHourlyWhere: "1",
		},
		"status exact": {
			input:           "status:403",
			wantRawWhere:    "(status = {p0:UInt16})",
			wantRawArgs:     []any{named("p0", uint64(403))},
			wantHourlyWhere: "(status = {p0:UInt16})",
			wantHourlyArgs:  []any{named("p0", uint64(403))},
		},
		"status range": {
			input:           "status:400..499",
			wantRawWhere:    "(status BETWEEN {p0:UInt16} AND {p1:UInt16})",
			wantRawArgs:     []any{named("p0", uint64(400)), named("p1", uint64(499))},
			wantHourlyWhere: "(status BETWEEN {p0:UInt16} AND {p1:UInt16})",
			wantHourlyArgs:  []any{named("p0", uint64(400)), named("p1", uint64(499))},
		},
		"status class": {
			input:           "status:4xx",
			wantRawWhere:    "(status BETWEEN {p0:UInt16} AND {p1:UInt16})",
			wantRawArgs:     []any{named("p0", uint64(400)), named("p1", uint64(499))},
			wantHourlyWhere: "(status BETWEEN {p0:UInt16} AND {p1:UInt16})",
			wantHourlyArgs:  []any{named("p0", uint64(400)), named("p1", uint64(499))},
		},
		"latency greater": {
			input:           "latency:>2000",
			wantRawWhere:    "(latency_ms > {p0:UInt32})",
			wantRawArgs:     []any{named("p0", uint64(2000))},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"size less": {
			input:           "size:<1024",
			wantRawWhere:    "(response_size < {p0:UInt32})",
			wantRawArgs:     []any{named("p0", uint64(1024))},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"latency range": {
			input:           "latency:500..2000",
			wantRawWhere:    "(latency_ms BETWEEN {p0:UInt32} AND {p1:UInt32})",
			wantRawArgs:     []any{named("p0", uint64(500)), named("p1", uint64(2000))},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"project": {
			input:           "project:a",
			wantRawWhere:    "(project = {p0:String})",
			wantRawArgs:     []any{named("p0", "a")},
			wantHourlyWhere: "(project = {p0:String})",
			wantHourlyArgs:  []any{named("p0", "a")},
		},
		"scraper": {
			input:           "scraper:a",
			wantRawWhere:    "(scraper = {p0:String})",
			wantRawArgs:     []any{named("p0", "a")},
			wantHourlyWhere: "(scraper = {p0:String})",
			wantHourlyArgs:  []any{named("p0", "a")},
		},
		"domain": {
			input:           "domain:shop.example.com",
			wantRawWhere:    "(domain = {p0:String})",
			wantRawArgs:     []any{named("p0", "shop.example.com")},
			wantHourlyWhere: "(domain = {p0:String})",
			wantHourlyArgs:  []any{named("p0", "shop.example.com")},
		},
		"proxy": {
			input:           "proxy:a",
			wantRawWhere:    "(proxy = {p0:String})",
			wantRawArgs:     []any{named("p0", "a")},
			wantHourlyWhere: "(proxy = {p0:String})",
			wantHourlyArgs:  []any{named("p0", "a")},
		},
		"tls": {
			input:           "tls:chrome_120",
			wantRawWhere:    "(tls_profile = {p0:String})",
			wantRawArgs:     []any{named("p0", "chrome_120")},
			wantHourlyWhere: "(tls_profile = {p0:String})",
			wantHourlyArgs:  []any{named("p0", "chrome_120")},
		},
		"method": {
			input:           "method:GET",
			wantRawWhere:    "(method = {p0:String})",
			wantRawArgs:     []any{named("p0", "GET")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"ja3": {
			input:           "ja3:a",
			wantRawWhere:    "(tls_ja3 = {p0:String})",
			wantRawArgs:     []any{named("p0", "a")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"ja4": {
			input:           "ja4:a",
			wantRawWhere:    "(tls_ja4 = {p0:String})",
			wantRawArgs:     []any{named("p0", "a")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"http": {
			input:           "http:h2",
			wantRawWhere:    "(http_version = {p0:String})",
			wantRawArgs:     []any{named("p0", "h2")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"id": {
			input:           "id:01J0",
			wantRawWhere:    "(request_id = {p0:String})",
			wantRawArgs:     []any{named("p0", "01J0")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"url contains": {
			input:           "url~/api/v2",
			wantRawWhere:    "(" + contains("url", "p0") + ")",
			wantRawArgs:     []any{named("p0", "/api/v2")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"error contains": {
			input:           "error~timeout",
			wantRawWhere:    "(" + contains("error", "p0") + ")",
			wantRawArgs:     []any{named("p0", "timeout")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"response header exact": {
			input:           "res.server:cloudflare",
			wantRawWhere:    "(" + headerExact("response_headers", "p0", "p1") + ")",
			wantRawArgs:     []any{named("p0", "server"), named("p1", "cloudflare")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"request header contains mixed case": {
			input:           "req.User-Agent~Chrome",
			wantRawWhere:    "(" + headerContains("request_headers", "p0", "p1") + ")",
			wantRawArgs:     []any{named("p0", "User-Agent"), named("p1", "Chrome")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"tag exact": {
			input:           "tag.region:eu",
			wantRawWhere:    "(tags[{p0:String}] = {p1:String})",
			wantRawArgs:     []any{named("p0", "region"), named("p1", "eu")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"tag contains": {
			input:           "tag.region~eu",
			wantRawWhere:    "(positionCaseInsensitive(tags[{p0:String}], {p1:String}) > 0)",
			wantRawArgs:     []any{named("p0", "region"), named("p1", "eu")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"bare word": {
			input:           "checkout",
			wantRawWhere:    "(" + contains("url", "p0") + ")",
			wantRawArgs:     []any{named("p0", "checkout")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"quoted phrase": {
			input:           `"add to cart"`,
			wantRawWhere:    "(" + contains("url", "p0") + ")",
			wantRawArgs:     []any{named("p0", "add to cart")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"quoted value": {
			input:           `error~"connection reset"`,
			wantRawWhere:    "(" + contains("error", "p0") + ")",
			wantRawArgs:     []any{named("p0", "connection reset")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"empty quoted value": {
			input:           `tls:""`,
			wantRawWhere:    "(tls_profile = {p0:String})",
			wantRawArgs:     []any{named("p0", "")},
			wantHourlyWhere: "(tls_profile = {p0:String})",
			wantHourlyArgs:  []any{named("p0", "")},
		},
		"negation": {
			input:           "-status:4xx",
			wantRawWhere:    "(NOT (status BETWEEN {p0:UInt16} AND {p1:UInt16}))",
			wantRawArgs:     []any{named("p0", uint64(400)), named("p1", uint64(499))},
			wantHourlyWhere: "(NOT (status BETWEEN {p0:UInt16} AND {p1:UInt16}))",
			wantHourlyArgs:  []any{named("p0", uint64(400)), named("p1", uint64(499))},
		},
		"negated header": {
			input:           "-res.server:cloudflare",
			wantRawWhere:    "(NOT (" + headerExact("response_headers", "p0", "p1") + "))",
			wantRawArgs:     []any{named("p0", "server"), named("p1", "cloudflare")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
		"and join with mixed hourly": {
			input:           "status:403 domain:a.com url~x",
			wantRawWhere:    "(status = {p0:UInt16}) AND (domain = {p1:String}) AND (" + contains("url", "p2") + ")",
			wantRawArgs:     []any{named("p0", uint64(403)), named("p1", "a.com"), named("p2", "x")},
			wantHourlyWhere: "(status = {p0:UInt16}) AND (domain = {p1:String})",
			wantHourlyArgs:  []any{named("p0", uint64(403)), named("p1", "a.com")},
			wantApproximate: true,
		},
		"dropped middle term keeps parameter names": {
			input:           "domain:a.com url~x status:200",
			wantRawWhere:    "(domain = {p0:String}) AND (" + contains("url", "p1") + ") AND (status = {p2:UInt16})",
			wantRawArgs:     []any{named("p0", "a.com"), named("p1", "x"), named("p2", uint64(200))},
			wantHourlyWhere: "(domain = {p0:String}) AND (status = {p2:UInt16})",
			wantHourlyArgs:  []any{named("p0", "a.com"), named("p2", uint64(200))},
			wantApproximate: true,
		},
		"value with colon": {
			input:           "res.x:a:b",
			wantRawWhere:    "(" + headerExact("response_headers", "p0", "p1") + ")",
			wantRawArgs:     []any{named("p0", "x"), named("p1", "a:b")},
			wantHourlyWhere: "1",
			wantApproximate: true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			compiled, err := Compile(test.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if compiled.Raw.Where != test.wantRawWhere {
				t.Fatalf("raw where = %q, want %q", compiled.Raw.Where, test.wantRawWhere)
			}
			if !reflect.DeepEqual(compiled.Raw.Args, test.wantRawArgs) {
				t.Fatalf("raw args = %#v, want %#v", compiled.Raw.Args, test.wantRawArgs)
			}
			if compiled.Hourly.Where != test.wantHourlyWhere {
				t.Fatalf("hourly where = %q, want %q", compiled.Hourly.Where, test.wantHourlyWhere)
			}
			if !reflect.DeepEqual(compiled.Hourly.Args, test.wantHourlyArgs) {
				t.Fatalf("hourly args = %#v, want %#v", compiled.Hourly.Args, test.wantHourlyArgs)
			}
			if compiled.Approximate != test.wantApproximate {
				t.Fatalf("approximate = %v, want %v", compiled.Approximate, test.wantApproximate)
			}
		})
	}
}

func TestCompileKeepsInjectionInParameters(t *testing.T) {
	tests := map[string]struct {
		input     string
		wantWhere string
		wantArgs  []any
	}{
		"quote": {
			input:     "domain:'",
			wantWhere: "(domain = {p0:String})",
			wantArgs:  []any{named("p0", "'")},
		},
		"backslash": {
			input:     `url~\`,
			wantWhere: "(" + contains("url", "p0") + ")",
			wantArgs:  []any{named("p0", `\`)},
		},
		"parenthesis": {
			input:     "error~)",
			wantWhere: "(" + contains("error", "p0") + ")",
			wantArgs:  []any{named("p0", ")")},
		},
		"comment marker": {
			input:     "url~--",
			wantWhere: "(" + contains("url", "p0") + ")",
			wantArgs:  []any{named("p0", "--")},
		},
		"parameter placeholder in value": {
			input:     "url~{x:String}",
			wantWhere: "(" + contains("url", "p0") + ")",
			wantArgs:  []any{named("p0", "{x:String}")},
		},
		"quoted or clause": {
			input:     `domain:"a' OR 1=1 --"`,
			wantWhere: "(domain = {p0:String})",
			wantArgs:  []any{named("p0", "a' OR 1=1 --")},
		},
		"header name with sql": {
			input:     "res.x')--:v",
			wantWhere: "(" + headerExact("response_headers", "p0", "p1") + ")",
			wantArgs:  []any{named("p0", "x')--"), named("p1", "v")},
		},
		"header name with placeholder": {
			input:     "req.a{p0:String}:v",
			wantWhere: "(" + headerExact("request_headers", "p0", "p1") + ")",
			wantArgs:  []any{named("p0", "a{p0"), named("p1", "String}:v")},
		},
		"tag name with braces": {
			input:     "tag.a}{b:v",
			wantWhere: "(tags[{p0:String}] = {p1:String})",
			wantArgs:  []any{named("p0", "a}{b"), named("p1", "v")},
		},
		"drop table words": {
			input: "'); DROP TABLE requests --",
			wantWhere: "(" + contains("url", "p0") + ") AND (" + contains("url", "p1") + ") AND (" + contains("url", "p2") + ") AND (" +
				contains("url", "p3") + ") AND (NOT (" + contains("url", "p4") + "))",
			wantArgs: []any{named("p0", "');"), named("p1", "DROP"), named("p2", "TABLE"), named("p3", "requests"), named("p4", "-")},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			compiled, err := Compile(test.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if compiled.Raw.Where != test.wantWhere {
				t.Fatalf("raw where = %q, want %q", compiled.Raw.Where, test.wantWhere)
			}
			if !reflect.DeepEqual(compiled.Raw.Args, test.wantArgs) {
				t.Fatalf("raw args = %#v, want %#v", compiled.Raw.Args, test.wantArgs)
			}
		})
	}
}

func TestCompileRejectsInvalidQueries(t *testing.T) {
	tests := map[string]struct {
		input     string
		wantError string
	}{
		"unknown field":             {input: "foo:bar", wantError: `unknown field "foo"` + invalidFieldSuffix},
		"column name is no alias":   {input: "latency_ms:5", wantError: `unknown field "latency_ms"` + invalidFieldSuffix},
		"missing value":             {input: "status:", wantError: `missing value for "status"`},
		"missing field":             {input: ":x", wantError: `missing field name in ":x"`},
		"missing header name":       {input: "res.:x", wantError: `missing name in "res.:x"`},
		"missing tag name":          {input: "tag.:x", wantError: `missing name in "tag.:x"`},
		"lone negation":             {input: "-", wantError: `nothing to negate in "-"`},
		"unterminated quote":        {input: `url~"abc`, wantError: `unterminated quote in "url~\"abc"`},
		"quote inside value":        {input: `url~a"b"`, wantError: `unexpected quote in "url~a\"b\""`},
		"quote in bare word":        {input: `ab"c"`, wantError: `unexpected quote in "ab\"c\""`},
		"quote in field":            {input: `a"b":c`, wantError: `unexpected quote in "a\"b\":c"`},
		"status not a number":       {input: "status:abc", wantError: `invalid value "abc" for status: expected N, N..M, >N, <N or a class like 4xx`},
		"status overflow":           {input: "status:70000", wantError: `invalid value "70000" for status: expected N, N..M, >N, <N or a class like 4xx`},
		"status class out of range": {input: "status:6xx", wantError: `invalid value "6xx" for status: expected N, N..M, >N, <N or a class like 4xx`},
		"open range":                {input: "latency:500..", wantError: `invalid value "500.." for latency: expected N, N..M, >N or <N`},
		"greater or equal":          {input: "latency:>=5", wantError: `invalid value ">=5" for latency: expected N, N..M, >N or <N`},
		"negative number":           {input: "size:-1", wantError: `invalid value "-1" for size: expected N, N..M, >N or <N`},
		"numeric contains":          {input: "status~4", wantError: `status does not support "~"`},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Compile(test.input)
			if err == nil {
				t.Fatalf("expected error %q, got none", test.wantError)
			}
			if err.Error() != test.wantError {
				t.Fatalf("error = %q, want %q", err.Error(), test.wantError)
			}
		})
	}
}
