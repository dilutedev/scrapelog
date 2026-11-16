package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/ingest"
)

const (
	refreshInterval = 30 * time.Second
	refreshTimeout  = 10 * time.Second
)

type ruleLister interface {
	ListRules(ctx context.Context) ([]Rule, error)
}

type Cache struct {
	lister ruleLister
	rules  atomic.Pointer[[]CompiledRule]
}

func NewCache(lister ruleLister) *Cache {
	cache := &Cache{lister: lister}
	emptyRules := []CompiledRule{}
	cache.rules.Store(&emptyRules)
	return cache
}

func (cache *Cache) Refresh(ctx context.Context) error {
	rules, err := cache.lister.ListRules(ctx)
	if err != nil {
		return err
	}
	activeRules := make([]CompiledRule, 0, len(rules))
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		compiled, err := Compile(rule)
		if err != nil {
			return fmt.Errorf("alert rule %s: %s", rule.RuleID, err)
		}
		activeRules = append(activeRules, compiled)
	}
	cache.rules.Store(&activeRules)
	return nil
}

func (cache *Cache) RunRefreshLoop(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshContext, cancel := context.WithTimeout(ctx, refreshTimeout)
			if err := cache.Refresh(refreshContext); err != nil {
				logger.Warn("refreshing alert rules failed", "error", err)
			}
			cancel()
		}
	}
}

func (cache *Cache) Rules() []CompiledRule {
	return *cache.rules.Load()
}

func (cache *Cache) MatchRecords(records []ingest.Record) []PendingAlert {
	rules := cache.Rules()
	var pendingAlerts []PendingAlert
	for _, record := range records {
		for ruleIndex := range rules {
			if match, matched := rules[ruleIndex].Match(record); matched {
				pendingAlerts = append(pendingAlerts, PendingAlert{Rule: &rules[ruleIndex].Rule, Match: match})
			}
		}
	}
	return pendingAlerts
}
