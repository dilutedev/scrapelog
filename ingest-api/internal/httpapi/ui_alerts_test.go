package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dilutedev/scrapelog/ingest-api/internal/alerts"
	"github.com/dilutedev/scrapelog/ingest-api/internal/config"
	"github.com/dilutedev/scrapelog/ingest-api/internal/session"
)

const (
	storedWebhookSecret = "https://discord.example/api/webhooks/STOREDSECRET"
	storedTokenSecret   = "111:STOREDTOKEN"
	newWebhookSecret    = "https://discord.example/api/webhooks/NEWSECRET"
)

type fakeAlertStore struct {
	rules         []alerts.Rule
	events        []alerts.Event
	listError     error
	getError      error
	writeError    error
	lastCreated   alerts.Rule
	lastUpdated   alerts.Rule
	lastDeletedID string
	lastEventRule string
	lastEventSize int
}

func (store *fakeAlertStore) ListRules(ctx context.Context) ([]alerts.Rule, error) {
	return store.rules, store.listError
}

func (store *fakeAlertStore) GetRule(ctx context.Context, ruleID string) (alerts.Rule, error) {
	if store.getError != nil {
		return alerts.Rule{}, store.getError
	}
	for _, rule := range store.rules {
		if rule.RuleID == ruleID {
			return rule, nil
		}
	}
	return alerts.Rule{}, alerts.ErrRuleNotFound
}

func (store *fakeAlertStore) CreateRule(ctx context.Context, rule alerts.Rule) (alerts.Rule, error) {
	store.lastCreated = rule
	if store.writeError != nil {
		return alerts.Rule{}, store.writeError
	}
	rule.RuleID = "new-rule"
	rule.CreatedAt = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	rule.UpdatedAt = rule.CreatedAt
	return rule, nil
}

func (store *fakeAlertStore) UpdateRule(ctx context.Context, rule alerts.Rule) (alerts.Rule, error) {
	store.lastUpdated = rule
	if store.writeError != nil {
		return alerts.Rule{}, store.writeError
	}
	return rule, nil
}

func (store *fakeAlertStore) DeleteRule(ctx context.Context, ruleID string) error {
	store.lastDeletedID = ruleID
	if store.writeError != nil {
		return store.writeError
	}
	for _, rule := range store.rules {
		if rule.RuleID == ruleID {
			return nil
		}
	}
	return alerts.ErrRuleNotFound
}

func (store *fakeAlertStore) ListEvents(ctx context.Context, ruleID string, limit int) ([]alerts.Event, error) {
	store.lastEventRule = ruleID
	store.lastEventSize = limit
	return store.events, store.listError
}

func newAlertsHandler(store AlertStore, refresher AlertRuleRefresher) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewHandler(context.Background(), logger, &fakePinger{}, &fakeKeyStore{}, &fakeRefresher{}, &fakeRecordWriter{}, &fakeBodyStore{}, &fakeStorageReporter{}, &fakeDiskGuard{}, &fakeRequestReader{}, nil, store, refresher, nil, config.Config{AdminToken: testAdminToken})
}

type fakeAlertTester struct {
	result   alerts.Delivery
	lastRule alerts.Rule
}

func (tester *fakeAlertTester) SendTest(ctx context.Context, rule alerts.Rule) alerts.Delivery {
	tester.lastRule = rule
	return tester.result
}

func newAlertsHandlerWithTester(store AlertStore, tester AlertTester) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewHandler(context.Background(), logger, &fakePinger{}, &fakeKeyStore{}, &fakeRefresher{}, &fakeRecordWriter{}, &fakeBodyStore{}, &fakeStorageReporter{}, &fakeDiskGuard{}, &fakeRequestReader{}, nil, store, &fakeRefresher{}, tester, config.Config{AdminToken: testAdminToken})
}

func alertRequest(method string, target string, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: newTestSigner().Issue()})
	return request
}

func storedAlertRule() alerts.Rule {
	return alerts.Rule{
		RuleID:            "rule-1",
		Name:              "Blocked",
		Project:           "shop",
		Conditions:        []alerts.Condition{{Field: "status", Operator: "equals", Value: "403"}},
		Channel:           alerts.ChannelDiscord,
		DiscordWebhookURL: storedWebhookSecret,
		TelegramBotToken:  storedTokenSecret,
		TelegramChatID:    "42",
		WindowSeconds:     60,
		Enabled:           true,
		CreatedAt:         time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt:         time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC),
	}
}

const validCreateBody = `{"name":" Blocked by captcha ","project":"shop","conditions":[{"field":"status","operator":"equals","value":"403"},{"field":"body","operator":"contains","value":"captcha"}],"channel":"discord","discord_webhook_url":"` + newWebhookSecret + `","enabled":true}`

func TestAlertRoutesRequireSession(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodGet, "/ui/api/alert-rules"},
		{http.MethodPost, "/ui/api/alert-rules"},
		{http.MethodPut, "/ui/api/alert-rules/rule-1"},
		{http.MethodDelete, "/ui/api/alert-rules/rule-1"},
		{http.MethodGet, "/ui/api/alert-events"},
		{http.MethodPost, "/ui/api/alert-rules/rule-1/test"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			recorder := serve(newAlertsHandler(&fakeAlertStore{}, &fakeRefresher{}), httptest.NewRequest(route.method, route.path, nil))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401", recorder.Code)
			}
		})
	}
}

func TestListAlertRulesNeverReturnsSecrets(t *testing.T) {
	store := &fakeAlertStore{rules: []alerts.Rule{storedAlertRule()}}
	recorder := serve(newAlertsHandler(store, &fakeRefresher{}), alertRequest(http.MethodGet, "/ui/api/alert-rules", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "STOREDSECRET") || strings.Contains(body, "STOREDTOKEN") {
		t.Fatalf("response leaks a secret: %s", body)
	}
	var response []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(response) != 1 || response[0]["discord_webhook_set"] != true || response[0]["telegram_token_set"] != true || response[0]["telegram_chat_id"] != "42" {
		t.Fatalf("unexpected response: %s", body)
	}
}

func TestListAlertRulesReturnsEmptyArray(t *testing.T) {
	recorder := serve(newAlertsHandler(&fakeAlertStore{}, &fakeRefresher{}), alertRequest(http.MethodGet, "/ui/api/alert-rules", ""))
	if strings.TrimSpace(recorder.Body.String()) != "[]" {
		t.Fatalf("body %q, want []", recorder.Body.String())
	}
}

func TestCreateAlertRuleValidatesAndRefreshes(t *testing.T) {
	t.Run("valid rule", func(t *testing.T) {
		store := &fakeAlertStore{}
		refresher := &fakeRefresher{}
		recorder := serve(newAlertsHandler(store, refresher), alertRequest(http.MethodPost, "/ui/api/alert-rules", validCreateBody))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
		}
		if store.lastCreated.DiscordWebhookURL != newWebhookSecret {
			t.Fatalf("store got webhook %q", store.lastCreated.DiscordWebhookURL)
		}
		if store.lastCreated.Name != "Blocked by captcha" {
			t.Fatalf("name was not trimmed: %q", store.lastCreated.Name)
		}
		if store.lastCreated.WindowSeconds != alerts.DefaultWindowSeconds {
			t.Fatalf("window %d, want default", store.lastCreated.WindowSeconds)
		}
		if strings.Contains(recorder.Body.String(), "NEWSECRET") {
			t.Fatalf("response leaks the secret: %s", recorder.Body.String())
		}
		if refresher.calls != 1 {
			t.Fatalf("refresh called %d times, want 1", refresher.calls)
		}
	})

	tests := map[string]struct {
		body    string
		message string
	}{
		"zero conditions":  {`{"name":"a","conditions":[],"channel":"discord","discord_webhook_url":"https://d.example/x"}`, "at least one condition is required"},
		"invalid regex":    {`{"name":"a","conditions":[{"field":"body","operator":"regex","value":"("}],"channel":"discord","discord_webhook_url":"https://d.example/x"}`, "invalid regex: error parsing regexp"},
		"bad project":      {`{"name":"a","project":"Bad Project","conditions":[{"field":"status","operator":"equals","value":"1"}],"channel":"discord","discord_webhook_url":"https://d.example/x"}`, "project must be empty or match"},
		"missing name":     {`{"name":"  ","conditions":[{"field":"status","operator":"equals","value":"1"}],"channel":"discord","discord_webhook_url":"https://d.example/x"}`, "name is required"},
		"long name":        {`{"name":"` + strings.Repeat("a", 129) + `","conditions":[{"field":"status","operator":"equals","value":"1"}],"channel":"discord","discord_webhook_url":"https://d.example/x"}`, "name must be at most 128 characters"},
		"invalid json":     {`{`, "invalid json body"},
		"bad webhook":      {`{"name":"a","conditions":[{"field":"status","operator":"equals","value":"1"}],"channel":"discord","discord_webhook_url":"ftp://x"}`, "discord_webhook_url must be an http or https URL"},
		"window too small": {`{"name":"a","conditions":[{"field":"status","operator":"equals","value":"1"}],"channel":"discord","discord_webhook_url":"https://d.example/x","window_seconds":5}`, "window_seconds must be between 10 and 3600"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store := &fakeAlertStore{}
			refresher := &fakeRefresher{}
			recorder := serve(newAlertsHandler(store, refresher), alertRequest(http.MethodPost, "/ui/api/alert-rules", test.body))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), test.message) {
				t.Fatalf("body %s lacks %q", recorder.Body.String(), test.message)
			}
			if refresher.calls != 0 || store.lastCreated.Name != "" {
				t.Fatal("an invalid rule must not be stored or refreshed")
			}
		})
	}
}

func TestUpdateAlertRuleKeepsStoredSecretWhenEmpty(t *testing.T) {
	updateBody := func(webhook string) string {
		return `{"name":"Renamed","project":"shop","conditions":[{"field":"status","operator":"equals","value":"500"}],"channel":"discord","discord_webhook_url":"` + webhook + `","window_seconds":120,"enabled":false}`
	}
	t.Run("empty secret keeps stored", func(t *testing.T) {
		store := &fakeAlertStore{rules: []alerts.Rule{storedAlertRule()}}
		refresher := &fakeRefresher{}
		recorder := serve(newAlertsHandler(store, refresher), alertRequest(http.MethodPut, "/ui/api/alert-rules/rule-1", updateBody("")))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
		}
		if store.lastUpdated.DiscordWebhookURL != storedWebhookSecret || store.lastUpdated.TelegramBotToken != storedTokenSecret {
			t.Fatalf("stored secrets were not kept: %+v", store.lastUpdated)
		}
		stored := storedAlertRule()
		if store.lastUpdated.RuleID != "rule-1" || !store.lastUpdated.CreatedAt.Equal(stored.CreatedAt) || !store.lastUpdated.UpdatedAt.Equal(stored.UpdatedAt) {
			t.Fatalf("identity fields not kept: %+v", store.lastUpdated)
		}
		if store.lastUpdated.Name != "Renamed" || store.lastUpdated.WindowSeconds != 120 || store.lastUpdated.Enabled {
			t.Fatalf("request fields not applied: %+v", store.lastUpdated)
		}
		if refresher.calls != 1 {
			t.Fatalf("refresh called %d times, want 1", refresher.calls)
		}
	})
	t.Run("new secret replaces stored", func(t *testing.T) {
		store := &fakeAlertStore{rules: []alerts.Rule{storedAlertRule()}}
		recorder := serve(newAlertsHandler(store, &fakeRefresher{}), alertRequest(http.MethodPut, "/ui/api/alert-rules/rule-1", updateBody(newWebhookSecret)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
		}
		if store.lastUpdated.DiscordWebhookURL != newWebhookSecret {
			t.Fatalf("webhook %q", store.lastUpdated.DiscordWebhookURL)
		}
	})
	t.Run("unknown id", func(t *testing.T) {
		store := &fakeAlertStore{}
		recorder := serve(newAlertsHandler(store, &fakeRefresher{}), alertRequest(http.MethodPut, "/ui/api/alert-rules/missing", updateBody("")))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status %d, want 404", recorder.Code)
		}
	})
	t.Run("invalid rule", func(t *testing.T) {
		store := &fakeAlertStore{rules: []alerts.Rule{storedAlertRule()}}
		recorder := serve(newAlertsHandler(store, &fakeRefresher{}), alertRequest(http.MethodPut, "/ui/api/alert-rules/rule-1", `{"name":"a","conditions":[],"channel":"discord"}`))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status %d, want 400", recorder.Code)
		}
	})
	t.Run("switching to telegram without a token", func(t *testing.T) {
		stored := storedAlertRule()
		stored.TelegramBotToken = ""
		store := &fakeAlertStore{rules: []alerts.Rule{stored}}
		recorder := serve(newAlertsHandler(store, &fakeRefresher{}), alertRequest(http.MethodPut, "/ui/api/alert-rules/rule-1", `{"name":"a","conditions":[{"field":"status","operator":"equals","value":"1"}],"channel":"telegram","telegram_chat_id":"5"}`))
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "telegram_bot_token is not a valid bot token") {
			t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestDeleteAlertRule(t *testing.T) {
	store := &fakeAlertStore{rules: []alerts.Rule{storedAlertRule()}}
	refresher := &fakeRefresher{}
	handler := newAlertsHandler(store, refresher)

	recorder := serve(handler, alertRequest(http.MethodDelete, "/ui/api/alert-rules/rule-1", ""))
	if recorder.Code != http.StatusNoContent || store.lastDeletedID != "rule-1" || refresher.calls != 1 {
		t.Fatalf("status %d, deleted %q, refreshes %d", recorder.Code, store.lastDeletedID, refresher.calls)
	}

	recorder = serve(handler, alertRequest(http.MethodDelete, "/ui/api/alert-rules/missing", ""))
	if recorder.Code != http.StatusNotFound || refresher.calls != 1 {
		t.Fatalf("status %d, refreshes %d", recorder.Code, refresher.calls)
	}
}

func TestListAlertEvents(t *testing.T) {
	store := &fakeAlertStore{events: []alerts.Event{{
		FiredAt:    time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC),
		RuleID:     "rule-1",
		RuleName:   "Blocked",
		MatchCount: 50,
		Samples:    `[{"url":"https://a.example"}]`,
		Delivery:   "sent",
	}}}
	handler := newAlertsHandler(store, &fakeRefresher{})

	recorder := serve(handler, alertRequest(http.MethodGet, "/ui/api/alert-events?rule_id=rule-1", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if store.lastEventRule != "rule-1" || store.lastEventSize != 50 {
		t.Fatalf("store got rule %q limit %d", store.lastEventRule, store.lastEventSize)
	}
	var response []struct {
		Samples    []map[string]string `json:"samples"`
		MatchCount int                 `json:"match_count"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(response) != 1 || len(response[0].Samples) != 1 || response[0].MatchCount != 50 {
		t.Fatalf("unexpected response: %s", recorder.Body.String())
	}

	serve(handler, alertRequest(http.MethodGet, "/ui/api/alert-events?limit=200", ""))
	if store.lastEventSize != 200 {
		t.Fatalf("limit %d, want 200", store.lastEventSize)
	}
	for _, limit := range []string{"0", "201", "abc"} {
		recorder = serve(handler, alertRequest(http.MethodGet, "/ui/api/alert-events?limit="+limit, ""))
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "limit must be between 1 and 200") {
			t.Fatalf("limit %s: status %d: %s", limit, recorder.Code, recorder.Body.String())
		}
	}
}

func TestAlertStoreErrorsReturnServiceUnavailable(t *testing.T) {
	failure := errors.New("clickhouse down")
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/ui/api/alert-rules", ""},
		{http.MethodPost, "/ui/api/alert-rules", validCreateBody},
		{http.MethodPut, "/ui/api/alert-rules/rule-1", validCreateBody},
		{http.MethodDelete, "/ui/api/alert-rules/rule-1", ""},
		{http.MethodGet, "/ui/api/alert-events", ""},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			store := &fakeAlertStore{rules: []alerts.Rule{storedAlertRule()}, listError: failure, getError: failure, writeError: failure}
			recorder := serve(newAlertsHandler(store, &fakeRefresher{}), alertRequest(route.method, route.path, route.body))
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status %d, want 503", recorder.Code)
			}
		})
	}
}

func TestTestAlertRuleReturnsDeliveryResult(t *testing.T) {
	store := &fakeAlertStore{rules: []alerts.Rule{storedAlertRule()}}
	tester := &fakeAlertTester{result: alerts.Delivery{Delivery: alerts.DeliveryFailed, StatusCode: 500, Error: "HTTP 500"}}
	handler := newAlertsHandlerWithTester(store, tester)

	recorder := serve(handler, alertRequest(http.MethodPost, "/ui/api/alert-rules/rule-1/test", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if response["delivery"] != "failed" || response["status_code"] != float64(500) || response["delivery_error"] != "HTTP 500" {
		t.Fatalf("unexpected response: %s", recorder.Body.String())
	}
	if tester.lastRule.RuleID != "rule-1" {
		t.Fatalf("tester got rule %q", tester.lastRule.RuleID)
	}

	recorder = serve(handler, alertRequest(http.MethodPost, "/ui/api/alert-rules/missing/test", ""))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", recorder.Code)
	}
}

func TestTestAlertRuleNeverLeaksSecrets(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	closedAddress := listener.Addr().String()
	listener.Close()
	rule := storedAlertRule()
	rule.DiscordWebhookURL = "http://" + closedAddress + "/webhook/SECRETMARKER7f3a"
	store := &fakeAlertStore{rules: []alerts.Rule{rule}}
	dispatcher := alerts.NewDispatcher(slog.New(slog.NewJSONHandler(io.Discard, nil)), nil, "")

	recorder := serve(newAlertsHandlerWithTester(store, dispatcher), alertRequest(http.MethodPost, "/ui/api/alert-rules/rule-1/test", ""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "SECRETMARKER7f3a") || !strings.Contains(recorder.Body.String(), "connection refused") {
		t.Fatalf("unexpected response: %s", recorder.Body.String())
	}
}
