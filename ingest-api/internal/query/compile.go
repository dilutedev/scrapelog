package query

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/ClickHouse/clickhouse-go/v2"
)

type Filter struct {
	Where string
	Args  []any
}

type Compiled struct {
	Raw         Filter
	Hourly      Filter
	Approximate bool
}

type fieldDefinition struct {
	column        string
	numeric       bool
	parameterType string
	bitSize       int
	hourly        bool
	allowsClass   bool
}

const validFieldsList = "domain, error, http, id, ja3, ja4, latency, method, project, proxy, req.<header>, res.<header>, scraper, size, status, tag.<name>, tls, url"

const (
	responseHeaderPrefix = "res."
	requestHeaderPrefix  = "req."
	tagPrefix            = "tag."
)

var fieldDefinitions = map[string]fieldDefinition{
	"project": {column: "project", parameterType: "String", hourly: true},
	"scraper": {column: "scraper", parameterType: "String", hourly: true},
	"domain":  {column: "domain", parameterType: "String", hourly: true},
	"proxy":   {column: "proxy", parameterType: "String", hourly: true},
	"tls":     {column: "tls_profile", parameterType: "String", hourly: true},
	"method":  {column: "method", parameterType: "String"},
	"ja3":     {column: "tls_ja3", parameterType: "String"},
	"ja4":     {column: "tls_ja4", parameterType: "String"},
	"http":    {column: "http_version", parameterType: "String"},
	"url":     {column: "url", parameterType: "String"},
	"error":   {column: "error", parameterType: "String"},
	"id":      {column: "request_id", parameterType: "String"},
	"status":  {column: "status", numeric: true, parameterType: "UInt16", bitSize: 16, hourly: true, allowsClass: true},
	"latency": {column: "latency_ms", numeric: true, parameterType: "UInt32", bitSize: 32},
	"size":    {column: "response_size", numeric: true, parameterType: "UInt32", bitSize: 32},
}

type term struct {
	negated     bool
	field       string
	operator    byte
	value       string
	quotedValue bool
}

type parameterBuilder struct {
	count int
	args  []any
}

func (builder *parameterBuilder) addParameter(parameterType string, value any) string {
	name := "p" + strconv.Itoa(builder.count)
	builder.count++
	builder.args = append(builder.args, clickhouse.Named(name, value))
	return "{" + name + ":" + parameterType + "}"
}

func Compile(queryText string) (Compiled, error) {
	tokens, err := splitTokens(queryText)
	if err != nil {
		return Compiled{}, err
	}

	var builder parameterBuilder
	var rawParts, hourlyParts []string
	var rawArgs, hourlyArgs []any
	approximate := false

	for _, token := range tokens {
		parsed, err := parseTerm(token)
		if err != nil {
			return Compiled{}, err
		}
		builder.args = nil
		sql, hourly, err := buildTermSQL(&builder, parsed, token)
		if err != nil {
			return Compiled{}, err
		}
		if parsed.negated {
			sql = "NOT (" + sql + ")"
		}
		rawParts = append(rawParts, sql)
		rawArgs = append(rawArgs, builder.args...)
		if hourly {
			hourlyParts = append(hourlyParts, sql)
			hourlyArgs = append(hourlyArgs, builder.args...)
		} else {
			approximate = true
		}
	}

	return Compiled{
		Raw:         Filter{Where: joinParts(rawParts), Args: rawArgs},
		Hourly:      Filter{Where: joinParts(hourlyParts), Args: hourlyArgs},
		Approximate: approximate,
	}, nil
}

func joinParts(parts []string) string {
	if len(parts) == 0 {
		return "1"
	}
	return "(" + strings.Join(parts, ") AND (") + ")"
}

func splitTokens(queryText string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	inQuote := false

	for _, character := range queryText {
		switch {
		case character == '"':
			inQuote = !inQuote
			current.WriteRune(character)
		case unicode.IsSpace(character) && !inQuote:
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(character)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unterminated quote in %q", current.String())
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens, nil
}

func unquote(text string) (string, bool) {
	if len(text) < 2 || !strings.HasSuffix(text, `"`) {
		return "", false
	}
	inside := text[1 : len(text)-1]
	if strings.Contains(inside, `"`) {
		return "", false
	}
	return inside, true
}

func parseTerm(token string) (term, error) {
	parsed := term{}
	rest := token
	if strings.HasPrefix(rest, "-") {
		parsed.negated = true
		rest = rest[1:]
		if rest == "" {
			return term{}, fmt.Errorf("nothing to negate in %q", token)
		}
	}

	unexpectedQuote := fmt.Errorf("unexpected quote in %q", token)

	if strings.HasPrefix(rest, `"`) {
		phrase, ok := unquote(rest)
		if !ok {
			return term{}, unexpectedQuote
		}
		parsed.field = "url"
		parsed.operator = '~'
		parsed.value = phrase
		return parsed, nil
	}

	operatorIndex := strings.IndexAny(rest, ":~")
	if operatorIndex == -1 {
		if strings.Contains(rest, `"`) {
			return term{}, unexpectedQuote
		}
		parsed.field = "url"
		parsed.operator = '~'
		parsed.value = rest
		return parsed, nil
	}

	parsed.field = rest[:operatorIndex]
	parsed.operator = rest[operatorIndex]
	if parsed.field == "" {
		return term{}, fmt.Errorf("missing field name in %q", token)
	}
	if strings.Contains(parsed.field, `"`) {
		return term{}, unexpectedQuote
	}

	value := rest[operatorIndex+1:]
	if strings.HasPrefix(value, `"`) {
		inside, ok := unquote(value)
		if !ok {
			return term{}, unexpectedQuote
		}
		parsed.value = inside
		parsed.quotedValue = true
		return parsed, nil
	}
	if strings.Contains(value, `"`) {
		return term{}, unexpectedQuote
	}
	if value == "" {
		return term{}, fmt.Errorf("missing value for %q", parsed.field)
	}
	parsed.value = value
	return parsed, nil
}

func buildTermSQL(builder *parameterBuilder, parsed term, token string) (string, bool, error) {
	for _, prefix := range []string{responseHeaderPrefix, requestHeaderPrefix, tagPrefix} {
		if !strings.HasPrefix(parsed.field, prefix) {
			continue
		}
		name := parsed.field[len(prefix):]
		if name == "" {
			return "", false, fmt.Errorf("missing name in %q", token)
		}
		return buildKeyedSQL(builder, prefix, name, parsed), false, nil
	}

	definition, found := fieldDefinitions[parsed.field]
	if !found {
		return "", false, fmt.Errorf("unknown field %q; valid fields: %s", parsed.field, validFieldsList)
	}

	if definition.numeric {
		sql, err := buildNumericSQL(builder, definition, parsed)
		return sql, definition.hourly, err
	}

	valueParameter := builder.addParameter(definition.parameterType, parsed.value)
	if parsed.operator == '~' {
		return "positionCaseInsensitive(" + definition.column + ", " + valueParameter + ") > 0", definition.hourly, nil
	}
	return definition.column + " = " + valueParameter, definition.hourly, nil
}

func buildKeyedSQL(builder *parameterBuilder, prefix string, name string, parsed term) string {
	nameParameter := builder.addParameter("String", name)
	valueParameter := builder.addParameter("String", parsed.value)

	if prefix == tagPrefix {
		if parsed.operator == '~' {
			return "positionCaseInsensitive(tags[" + nameParameter + "], " + valueParameter + ") > 0"
		}
		return "tags[" + nameParameter + "] = " + valueParameter
	}

	column := "response_headers"
	if prefix == requestHeaderPrefix {
		column = "request_headers"
	}
	valueCondition := "v = " + valueParameter
	if parsed.operator == '~' {
		valueCondition = "positionCaseInsensitive(v, " + valueParameter + ") > 0"
	}
	return "arrayExists((k, v) -> lower(k) = lower(" + nameParameter + ") AND " + valueCondition + ", mapKeys(" + column + "), mapValues(" + column + "))"
}

func buildNumericSQL(builder *parameterBuilder, definition fieldDefinition, parsed term) (string, error) {
	if parsed.operator == '~' {
		return "", fmt.Errorf("%s does not support %q", parsed.field, "~")
	}

	var invalidValue error
	if definition.allowsClass {
		invalidValue = fmt.Errorf("invalid value %q for %s: expected N, N..M, >N, <N or a class like 4xx", parsed.value, parsed.field)
	} else {
		invalidValue = fmt.Errorf("invalid value %q for %s: expected N, N..M, >N or <N", parsed.value, parsed.field)
	}

	parseNumber := func(text string) (uint64, bool) {
		number, err := strconv.ParseUint(text, 10, definition.bitSize)
		return number, err == nil
	}
	column := definition.column
	value := parsed.value

	if definition.allowsClass && len(value) == 3 && value[0] >= '1' && value[0] <= '5' && value[1:] == "xx" {
		lower := uint64(value[0]-'0') * 100
		return column + " BETWEEN " + builder.addParameter(definition.parameterType, lower) + " AND " + builder.addParameter(definition.parameterType, lower+99), nil
	}

	if strings.HasPrefix(value, ">") || strings.HasPrefix(value, "<") {
		number, ok := parseNumber(value[1:])
		if !ok {
			return "", invalidValue
		}
		return column + " " + value[:1] + " " + builder.addParameter(definition.parameterType, number), nil
	}

	if startText, endText, isRange := strings.Cut(value, ".."); isRange {
		start, startOK := parseNumber(startText)
		end, endOK := parseNumber(endText)
		if !startOK || !endOK {
			return "", invalidValue
		}
		startParameter := builder.addParameter(definition.parameterType, start)
		endParameter := builder.addParameter(definition.parameterType, end)
		return column + " BETWEEN " + startParameter + " AND " + endParameter, nil
	}

	number, ok := parseNumber(value)
	if !ok {
		return "", invalidValue
	}
	return column + " = " + builder.addParameter(definition.parameterType, number), nil
}
