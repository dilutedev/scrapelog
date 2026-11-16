package alerts

import (
	"bytes"
	"strings"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/ingest"
)

type Match struct {
	RuleID      string
	Project     string
	Timestamp   time.Time
	RequestID   string
	Method      string
	URL         string
	Domain      string
	Status      uint16
	LatencyMs   uint32
	BodySnippet string
}

func (rule CompiledRule) Match(record ingest.Record) (Match, bool) {
	if rule.Rule.Project != "" && rule.Rule.Project != record.Project {
		return Match{}, false
	}
	bodySnippet := ""
	for _, condition := range rule.conditions {
		if condition.field == fieldBody {
			passed, snippet := condition.matchBody(record.Body)
			if !passed {
				return Match{}, false
			}
			if bodySnippet == "" {
				bodySnippet = snippet
			}
			continue
		}
		if !condition.matchRecord(record) {
			return Match{}, false
		}
	}
	return Match{
		RuleID:      rule.Rule.RuleID,
		Project:     record.Project,
		Timestamp:   record.Timestamp,
		RequestID:   record.RequestID,
		Method:      record.Method,
		URL:         record.URL,
		Domain:      record.Domain,
		Status:      record.Status,
		LatencyMs:   record.LatencyMs,
		BodySnippet: bodySnippet,
	}, true
}

func (condition compiledCondition) matchRecord(record ingest.Record) bool {
	switch condition.field {
	case fieldStatus:
		return condition.matchNumber(uint64(record.Status))
	case fieldLatency:
		return condition.matchNumber(uint64(record.LatencyMs))
	case fieldResponseSize:
		return condition.matchNumber(uint64(record.ResponseSize))
	case fieldMethod:
		return condition.matchText(record.Method)
	case fieldDomain:
		return condition.matchText(record.Domain)
	case fieldScraper:
		return condition.matchText(record.Scraper)
	case fieldProxy:
		return condition.matchText(record.Proxy)
	case fieldTLSProfile:
		return condition.matchText(record.TLSProfile)
	case fieldURL:
		return condition.matchText(record.URL)
	case fieldError:
		return condition.matchText(record.Error)
	case fieldResponseHeader:
		actual, present := lookupHeader(record.ResponseHeaders, condition.name)
		return condition.matchKeyed(actual, present)
	case fieldRequestHeader:
		actual, present := lookupHeader(record.RequestHeaders, condition.name)
		return condition.matchKeyed(actual, present)
	case fieldTag:
		actual, present := record.Tags[condition.name]
		return condition.matchKeyed(actual, present)
	}
	return false
}

func lookupHeader(headers map[string]string, name string) (string, bool) {
	for headerName, headerValue := range headers {
		if strings.EqualFold(headerName, name) {
			return headerValue, true
		}
	}
	return "", false
}

func (condition compiledCondition) matchNumber(actual uint64) bool {
	switch condition.operator {
	case operatorEquals:
		return actual == condition.lowerBound
	case operatorNotEquals:
		return actual != condition.lowerBound
	case operatorGreaterThan:
		return actual > condition.lowerBound
	case operatorLessThan:
		return actual < condition.lowerBound
	case operatorBetween:
		return actual >= condition.lowerBound && actual <= condition.upperBound
	case operatorClass:
		return actual/100 == uint64(condition.classDigit)
	}
	return false
}

func (condition compiledCondition) matchText(actual string) bool {
	switch condition.operator {
	case operatorEquals:
		return strings.EqualFold(actual, condition.value)
	case operatorNotEquals:
		return !strings.EqualFold(actual, condition.value)
	case operatorContains, operatorRegex:
		return condition.pattern.MatchString(actual)
	case operatorNotContains:
		return !condition.pattern.MatchString(actual)
	}
	return false
}

func (condition compiledCondition) matchKeyed(actual string, present bool) bool {
	switch condition.operator {
	case operatorExists:
		return present
	case operatorMissing:
		return !present
	case operatorNotContains:
		return !present || condition.matchText(actual)
	}
	return present && condition.matchText(actual)
}

func (condition compiledCondition) matchBody(body []byte) (bool, string) {
	scanned := body[:min(len(body), maxBodyScanBytes)]
	location := condition.pattern.FindIndex(scanned)
	if condition.operator == operatorNotContains {
		return location == nil, ""
	}
	if location == nil {
		return false, ""
	}
	snippetStart := max(0, location[0]-80)
	snippetEnd := min(len(scanned), location[0]+120)
	return true, string(bytes.ToValidUTF8(scanned[snippetStart:snippetEnd], nil))
}
