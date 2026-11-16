package alerts

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/dilutedev/scrapelog/ingest-api/internal/ingest"
)

type fakeLister struct {
	rules []Rule
	err   error
}

func (lister *fakeLister) ListRules(ctx context.Context) ([]Rule, error) {
	return lister.rules, lister.err
}

func cacheRule(ruleID string, enabled bool) Rule {
	rule := validDiscordRule(statusEquals())
	rule.RuleID = ruleID
	rule.Enabled = enabled
	return rule
}

func TestCacheRefreshErrorLeavesEmptyRuleSet(t *testing.T) {
	cache := NewCache(&fakeLister{err: errors.New("clickhouse down")})
	if err := cache.Refresh(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if len(cache.Rules()) != 0 {
		t.Fatalf("expected an empty rule set, got %d", len(cache.Rules()))
	}
}

func TestCacheKeepsLastRulesWhenRefreshFails(t *testing.T) {
	lister := &fakeLister{rules: []Rule{cacheRule("a", true)}}
	cache := NewCache(lister)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lister.err = errors.New("clickhouse down")
	if err := cache.Refresh(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if len(cache.Rules()) != 1 {
		t.Fatalf("expected the last rule set to stay, got %d", len(cache.Rules()))
	}

	lister.err = nil
	invalidRule := cacheRule("b", true)
	invalidRule.Conditions = nil
	lister.rules = []Rule{cacheRule("a", true), invalidRule}
	if err := cache.Refresh(context.Background()); err == nil {
		t.Fatal("expected a compile error")
	}
	if len(cache.Rules()) != 1 {
		t.Fatalf("a failed compile must keep the last rule set, got %d", len(cache.Rules()))
	}
}

func TestCacheSkipsDisabledRules(t *testing.T) {
	cache := NewCache(&fakeLister{rules: []Rule{cacheRule("on", true), cacheRule("off", false)}})
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rules := cache.Rules()
	if len(rules) != 1 || rules[0].Rule.RuleID != "on" {
		t.Fatalf("unexpected rules: %+v", rules)
	}
}

func refreshedCacheWithTwoRules(t *testing.T) *Cache {
	t.Helper()
	status403Rule := cacheRule("status-403", true)
	captchaRule := cacheRule("body-captcha", true)
	captchaRule.Conditions = []Condition{{Field: "body", Operator: "contains", Value: "captcha"}}
	cache := NewCache(&fakeLister{rules: []Rule{status403Rule, captchaRule}})
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return cache
}

func TestMatchRecordsReturnsRulePointersFromPublishedSlice(t *testing.T) {
	cache := refreshedCacheWithTwoRules(t)
	records := []ingest.Record{
		{Status: 403, Body: []byte("clean")},
		{Status: 200, Body: []byte("a captcha page")},
		{Status: 403, Body: []byte("captcha")},
		{Status: 200, Body: []byte("clean")},
	}

	pendingAlerts := cache.MatchRecords(records)

	expectedRuleIDs := []string{"status-403", "body-captcha", "status-403", "body-captcha"}
	if len(pendingAlerts) != len(expectedRuleIDs) {
		t.Fatalf("expected %d alerts, got %d", len(expectedRuleIDs), len(pendingAlerts))
	}
	publishedRules := cache.Rules()
	for index, pendingAlert := range pendingAlerts {
		if pendingAlert.Match.RuleID != expectedRuleIDs[index] {
			t.Fatalf("alert %d: expected rule %s, got %s", index, expectedRuleIDs[index], pendingAlert.Match.RuleID)
		}
		matchedRule := &publishedRules[0].Rule
		if expectedRuleIDs[index] == "body-captcha" {
			matchedRule = &publishedRules[1].Rule
		}
		if pendingAlert.Rule != matchedRule {
			t.Fatalf("alert %d: rule pointer does not point into the published slice", index)
		}
	}
}

func TestMatchRecordsWithNoMatchesReturnsNil(t *testing.T) {
	cache := refreshedCacheWithTwoRules(t)

	pendingAlerts := cache.MatchRecords([]ingest.Record{{Status: 200, Body: []byte("clean")}})

	if pendingAlerts != nil {
		t.Fatalf("expected nil, got %d alerts", len(pendingAlerts))
	}
}

func BenchmarkMatchRecords(b *testing.B) {
	webhook := "https://discord.example/api/webhooks/1/abc"
	rules := []Rule{
		{RuleID: "r1", Conditions: []Condition{{Field: "body", Operator: "regex", Value: "(?i)captcha|access denied"}}},
		{RuleID: "r2", Conditions: []Condition{{Field: "status", Operator: "equals", Value: "403"}}},
		{RuleID: "r3", Conditions: []Condition{{Field: "url", Operator: "contains", Value: "/checkout"}}},
		{RuleID: "r4", Conditions: []Condition{{Field: "latency_ms", Operator: "greater_than", Value: "5000"}}},
		{RuleID: "r5", Conditions: []Condition{{Field: "status", Operator: "class", Value: "5xx"}, {Field: "body", Operator: "contains", Value: "error"}}},
	}
	for index := range rules {
		rules[index].Channel = ChannelDiscord
		rules[index].DiscordWebhookURL = webhook
		rules[index].WindowSeconds = 60
		rules[index].Enabled = true
	}
	cache := NewCache(&fakeLister{rules: rules})
	if err := cache.Refresh(context.Background()); err != nil {
		b.Fatalf("unexpected error: %v", err)
	}
	records := make([]ingest.Record, 5000)
	for index := range records {
		records[index] = ingest.Record{Status: 200, URL: "https://example.com/loadtest/1", Body: bytes.Repeat([]byte("x"), 1024)}
	}
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		cache.MatchRecords(records)
	}
}
