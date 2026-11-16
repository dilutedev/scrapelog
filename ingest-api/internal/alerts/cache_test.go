package alerts

import (
	"context"
	"errors"
	"testing"
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
