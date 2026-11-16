package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"uuid"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

var ErrRuleNotFound = errors.New("alert rule not found")

type Event struct {
	FiredAt       time.Time
	RuleID        string
	RuleName      string
	Project       string
	MatchCount    uint32
	DroppedCount  uint32
	Samples       string
	Delivery      string
	DeliveryError string
}

type Store struct {
	connection driver.Conn
}

func NewStore(connection driver.Conn) *Store {
	return &Store{connection: connection}
}

const rulesQuery = `SELECT rule_id,
       argMax(name, updated_at) AS current_name,
       argMax(project, updated_at) AS current_project,
       argMax(conditions, updated_at) AS current_conditions,
       argMax(channel, updated_at) AS current_channel,
       argMax(discord_webhook_url, updated_at) AS current_discord_webhook_url,
       argMax(telegram_bot_token, updated_at) AS current_telegram_bot_token,
       argMax(telegram_chat_id, updated_at) AS current_telegram_chat_id,
       argMax(window_seconds, updated_at) AS current_window_seconds,
       argMax(enabled, updated_at) AS current_enabled,
       argMax(created_at, updated_at) AS current_created_at,
       max(updated_at) AS current_updated_at,
       argMax(deleted, updated_at) AS current_deleted
FROM alert_rules
WHERE ({rule_id:String} = '' OR rule_id = {rule_id:String})
GROUP BY rule_id
HAVING current_deleted = 0
ORDER BY current_created_at`

const eventsQuery = `SELECT fired_at, rule_id, rule_name, project, match_count, dropped_count, samples, delivery, delivery_error
FROM alert_events
WHERE ({rule_id:String} = '' OR rule_id = {rule_id:String})
ORDER BY fired_at DESC
LIMIT {limit:UInt32}`

func (store *Store) ListRules(ctx context.Context) ([]Rule, error) {
	return store.queryRules(ctx, "")
}

func (store *Store) GetRule(ctx context.Context, ruleID string) (Rule, error) {
	rules, err := store.queryRules(ctx, ruleID)
	if err != nil {
		return Rule{}, err
	}
	if len(rules) == 0 {
		return Rule{}, ErrRuleNotFound
	}
	return rules[0], nil
}

func (store *Store) queryRules(ctx context.Context, ruleID string) ([]Rule, error) {
	rows, err := store.connection.Query(ctx, rulesQuery, clickhouse.Named("rule_id", ruleID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rules := []Rule{}
	for rows.Next() {
		var rule Rule
		var conditionsJSON string
		var enabledFlag uint8
		var deletedFlag uint8
		if err := rows.Scan(&rule.RuleID, &rule.Name, &rule.Project, &conditionsJSON, &rule.Channel, &rule.DiscordWebhookURL, &rule.TelegramBotToken, &rule.TelegramChatID, &rule.WindowSeconds, &enabledFlag, &rule.CreatedAt, &rule.UpdatedAt, &deletedFlag); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(conditionsJSON), &rule.Conditions); err != nil {
			return nil, err
		}
		rule.Enabled = enabledFlag == 1
		rule.Deleted = deletedFlag == 1
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rules, nil
}

func (store *Store) CreateRule(ctx context.Context, rule Rule) (Rule, error) {
	now := time.Now().UTC()
	rule.RuleID = uuid.NewV7().String()
	rule.CreatedAt = now
	rule.UpdatedAt = now
	if err := store.insert(ctx, rule); err != nil {
		return Rule{}, err
	}
	return rule, nil
}

func (store *Store) UpdateRule(ctx context.Context, rule Rule) (Rule, error) {
	rule.UpdatedAt = nextUpdatedAt(rule.UpdatedAt)
	if err := store.insert(ctx, rule); err != nil {
		return Rule{}, err
	}
	return rule, nil
}

func (store *Store) DeleteRule(ctx context.Context, ruleID string) error {
	rule, err := store.GetRule(ctx, ruleID)
	if err != nil {
		return err
	}
	rule.Deleted = true
	rule.UpdatedAt = nextUpdatedAt(rule.UpdatedAt)
	return store.insert(ctx, rule)
}

func nextUpdatedAt(previous time.Time) time.Time {
	updatedAt := time.Now().UTC()
	earliestWinningTime := previous.Add(time.Millisecond)
	if updatedAt.Before(earliestWinningTime) {
		updatedAt = earliestWinningTime
	}
	return updatedAt
}

func (store *Store) insert(ctx context.Context, rule Rule) error {
	conditionsJSON, err := json.Marshal(rule.Conditions)
	if err != nil {
		return err
	}
	batch, err := store.connection.PrepareBatch(ctx, "INSERT INTO alert_rules (rule_id, name, project, conditions, channel, discord_webhook_url, telegram_bot_token, telegram_chat_id, window_seconds, enabled, deleted, created_at, updated_at)")
	if err != nil {
		return err
	}
	var enabledFlag uint8
	if rule.Enabled {
		enabledFlag = 1
	}
	var deletedFlag uint8
	if rule.Deleted {
		deletedFlag = 1
	}
	if err := batch.Append(rule.RuleID, rule.Name, rule.Project, string(conditionsJSON), rule.Channel, rule.DiscordWebhookURL, rule.TelegramBotToken, rule.TelegramChatID, rule.WindowSeconds, enabledFlag, deletedFlag, rule.CreatedAt, rule.UpdatedAt); err != nil {
		batch.Abort()
		return err
	}
	return batch.Send()
}

func (store *Store) ListEvents(ctx context.Context, ruleID string, limit int) ([]Event, error) {
	rows, err := store.connection.Query(ctx, eventsQuery, clickhouse.Named("rule_id", ruleID), clickhouse.Named("limit", uint32(limit)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []Event{}
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.FiredAt, &event.RuleID, &event.RuleName, &event.Project, &event.MatchCount, &event.DroppedCount, &event.Samples, &event.Delivery, &event.DeliveryError); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (store *Store) InsertEvent(ctx context.Context, event Event) error {
	batch, err := store.connection.PrepareBatch(ctx, "INSERT INTO alert_events (fired_at, rule_id, rule_name, project, match_count, dropped_count, samples, delivery, delivery_error)")
	if err != nil {
		return err
	}
	if err := batch.Append(event.FiredAt, event.RuleID, event.RuleName, event.Project, event.MatchCount, event.DroppedCount, event.Samples, event.Delivery, event.DeliveryError); err != nil {
		batch.Abort()
		return err
	}
	return batch.Send()
}
