package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/alerts"
)

const (
	maxAlertRuleBodyBytes   = 65536
	maxAlertRuleNameLength  = 128
	defaultAlertEventsLimit = 50
	maxAlertEventsLimit     = 200
)

type AlertStore interface {
	ListRules(ctx context.Context) ([]alerts.Rule, error)
	GetRule(ctx context.Context, ruleID string) (alerts.Rule, error)
	CreateRule(ctx context.Context, rule alerts.Rule) (alerts.Rule, error)
	UpdateRule(ctx context.Context, rule alerts.Rule) (alerts.Rule, error)
	DeleteRule(ctx context.Context, ruleID string) error
	ListEvents(ctx context.Context, ruleID string, limit int) ([]alerts.Event, error)
}

type AlertRuleRefresher interface {
	Refresh(ctx context.Context) error
}

type alertRuleRequest struct {
	Name              string             `json:"name"`
	Project           string             `json:"project"`
	Conditions        []alerts.Condition `json:"conditions"`
	Channel           string             `json:"channel"`
	DiscordWebhookURL string             `json:"discord_webhook_url"`
	TelegramBotToken  string             `json:"telegram_bot_token"`
	TelegramChatID    string             `json:"telegram_chat_id"`
	WindowSeconds     uint32             `json:"window_seconds"`
	Enabled           bool               `json:"enabled"`
}

type alertRuleResponse struct {
	RuleID            string             `json:"rule_id"`
	Name              string             `json:"name"`
	Project           string             `json:"project"`
	Conditions        []alerts.Condition `json:"conditions"`
	Channel           string             `json:"channel"`
	TelegramChatID    string             `json:"telegram_chat_id"`
	DiscordWebhookSet bool               `json:"discord_webhook_set"`
	TelegramTokenSet  bool               `json:"telegram_token_set"`
	WindowSeconds     uint32             `json:"window_seconds"`
	Enabled           bool               `json:"enabled"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

type alertEventResponse struct {
	FiredAt       time.Time       `json:"fired_at"`
	RuleID        string          `json:"rule_id"`
	RuleName      string          `json:"rule_name"`
	Project       string          `json:"project"`
	MatchCount    uint32          `json:"match_count"`
	DroppedCount  uint32          `json:"dropped_count"`
	Samples       json.RawMessage `json:"samples"`
	Delivery      string          `json:"delivery"`
	DeliveryError string          `json:"delivery_error"`
}

func toAlertRuleResponse(rule alerts.Rule) alertRuleResponse {
	return alertRuleResponse{
		RuleID:            rule.RuleID,
		Name:              rule.Name,
		Project:           rule.Project,
		Conditions:        rule.Conditions,
		Channel:           rule.Channel,
		TelegramChatID:    rule.TelegramChatID,
		DiscordWebhookSet: rule.DiscordWebhookURL != "",
		TelegramTokenSet:  rule.TelegramBotToken != "",
		WindowSeconds:     rule.WindowSeconds,
		Enabled:           rule.Enabled,
		CreatedAt:         rule.CreatedAt,
		UpdatedAt:         rule.UpdatedAt,
	}
}

func refreshAlertRulesAfterWrite(logger *slog.Logger, refresher AlertRuleRefresher, request *http.Request) {
	if err := refresher.Refresh(request.Context()); err != nil {
		logger.Warn("refreshing alert rules after write failed", "error", err)
	}
}

func ruleFromRequest(responseWriter http.ResponseWriter, request *http.Request) (alerts.Rule, bool) {
	var body alertRuleRequest
	limitedBody := http.MaxBytesReader(responseWriter, request.Body, maxAlertRuleBodyBytes)
	if err := json.NewDecoder(limitedBody).Decode(&body); err != nil {
		writeError(responseWriter, http.StatusBadRequest, "invalid json body")
		return alerts.Rule{}, false
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(responseWriter, http.StatusBadRequest, "name is required")
		return alerts.Rule{}, false
	}
	if len([]rune(name)) > maxAlertRuleNameLength {
		writeError(responseWriter, http.StatusBadRequest, "name must be at most 128 characters")
		return alerts.Rule{}, false
	}
	if body.Project != "" && !projectPattern.MatchString(body.Project) {
		writeError(responseWriter, http.StatusBadRequest, "project must be empty or match "+projectPattern.String())
		return alerts.Rule{}, false
	}
	windowSeconds := body.WindowSeconds
	if windowSeconds == 0 {
		windowSeconds = alerts.DefaultWindowSeconds
	}
	return alerts.Rule{
		Name:              name,
		Project:           body.Project,
		Conditions:        body.Conditions,
		Channel:           body.Channel,
		DiscordWebhookURL: body.DiscordWebhookURL,
		TelegramBotToken:  body.TelegramBotToken,
		TelegramChatID:    body.TelegramChatID,
		WindowSeconds:     windowSeconds,
		Enabled:           body.Enabled,
	}, true
}

func listAlertRulesHandler(logger *slog.Logger, store AlertStore) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		rules, err := store.ListRules(request.Context())
		if err != nil {
			logger.Error("listing alert rules failed", "error", err)
			writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
			return
		}
		responses := make([]alertRuleResponse, 0, len(rules))
		for _, rule := range rules {
			responses = append(responses, toAlertRuleResponse(rule))
		}
		writeJSON(responseWriter, http.StatusOK, responses)
	}
}

func createAlertRuleHandler(logger *slog.Logger, store AlertStore, refresher AlertRuleRefresher) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		rule, valid := ruleFromRequest(responseWriter, request)
		if !valid {
			return
		}
		if _, err := alerts.Compile(rule); err != nil {
			writeError(responseWriter, http.StatusBadRequest, err.Error())
			return
		}
		created, err := store.CreateRule(request.Context(), rule)
		if err != nil {
			logger.Error("creating alert rule failed", "error", err)
			writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
			return
		}
		refreshAlertRulesAfterWrite(logger, refresher, request)
		writeJSON(responseWriter, http.StatusCreated, toAlertRuleResponse(created))
	}
}

func updateAlertRuleHandler(logger *slog.Logger, store AlertStore, refresher AlertRuleRefresher) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		rule, valid := ruleFromRequest(responseWriter, request)
		if !valid {
			return
		}
		stored, err := store.GetRule(request.Context(), request.PathValue("rule_id"))
		if errors.Is(err, alerts.ErrRuleNotFound) {
			writeError(responseWriter, http.StatusNotFound, "alert rule not found")
			return
		}
		if err != nil {
			logger.Error("loading alert rule failed", "error", err)
			writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
			return
		}
		rule.RuleID = stored.RuleID
		rule.CreatedAt = stored.CreatedAt
		rule.UpdatedAt = stored.UpdatedAt
		if rule.DiscordWebhookURL == "" {
			rule.DiscordWebhookURL = stored.DiscordWebhookURL
		}
		if rule.TelegramBotToken == "" {
			rule.TelegramBotToken = stored.TelegramBotToken
		}
		if _, err := alerts.Compile(rule); err != nil {
			writeError(responseWriter, http.StatusBadRequest, err.Error())
			return
		}
		updated, err := store.UpdateRule(request.Context(), rule)
		if err != nil {
			logger.Error("updating alert rule failed", "error", err)
			writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
			return
		}
		refreshAlertRulesAfterWrite(logger, refresher, request)
		writeJSON(responseWriter, http.StatusOK, toAlertRuleResponse(updated))
	}
}

func deleteAlertRuleHandler(logger *slog.Logger, store AlertStore, refresher AlertRuleRefresher) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		err := store.DeleteRule(request.Context(), request.PathValue("rule_id"))
		if errors.Is(err, alerts.ErrRuleNotFound) {
			writeError(responseWriter, http.StatusNotFound, "alert rule not found")
			return
		}
		if err != nil {
			logger.Error("deleting alert rule failed", "error", err)
			writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
			return
		}
		refreshAlertRulesAfterWrite(logger, refresher, request)
		responseWriter.WriteHeader(http.StatusNoContent)
	}
}

func listAlertEventsHandler(logger *slog.Logger, store AlertStore) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		limit := defaultAlertEventsLimit
		if limitText := request.URL.Query().Get("limit"); limitText != "" {
			parsedLimit, err := strconv.Atoi(limitText)
			if err != nil || parsedLimit < 1 || parsedLimit > maxAlertEventsLimit {
				writeError(responseWriter, http.StatusBadRequest, "limit must be between 1 and 200")
				return
			}
			limit = parsedLimit
		}
		events, err := store.ListEvents(request.Context(), request.URL.Query().Get("rule_id"), limit)
		if err != nil {
			logger.Error("listing alert events failed", "error", err)
			writeError(responseWriter, http.StatusServiceUnavailable, "clickhouse unavailable")
			return
		}
		responses := make([]alertEventResponse, 0, len(events))
		for _, event := range events {
			responses = append(responses, alertEventResponse{
				FiredAt:       event.FiredAt,
				RuleID:        event.RuleID,
				RuleName:      event.RuleName,
				Project:       event.Project,
				MatchCount:    event.MatchCount,
				DroppedCount:  event.DroppedCount,
				Samples:       json.RawMessage(event.Samples),
				Delivery:      event.Delivery,
				DeliveryError: event.DeliveryError,
			})
		}
		writeJSON(responseWriter, http.StatusOK, responses)
	}
}
