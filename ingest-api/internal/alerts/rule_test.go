package alerts

import (
	"strings"
	"testing"
)

func validDiscordRule(conditions ...Condition) Rule {
	return Rule{
		Name:              "rule",
		Conditions:        conditions,
		Channel:           ChannelDiscord,
		DiscordWebhookURL: "https://discord.example/api/webhooks/1/abc",
		WindowSeconds:     60,
		Enabled:           true,
	}
}

func statusEquals() Condition {
	return Condition{Field: "status", Operator: "equals", Value: "403"}
}

func TestCompileRejectsInvalidRules(t *testing.T) {
	manyConditions := make([]Condition, MaxConditions+1)
	for index := range manyConditions {
		manyConditions[index] = statusEquals()
	}
	withRule := func(modify func(rule *Rule)) Rule {
		rule := validDiscordRule(statusEquals())
		modify(&rule)
		return rule
	}
	withCondition := func(condition Condition) Rule {
		return validDiscordRule(condition)
	}
	tests := map[string]struct {
		rule    Rule
		message string
	}{
		"no conditions":         {withRule(func(rule *Rule) { rule.Conditions = nil }), "at least one condition is required"},
		"too many conditions":   {withRule(func(rule *Rule) { rule.Conditions = manyConditions }), "at most 20 conditions are allowed"},
		"window too small":      {withRule(func(rule *Rule) { rule.WindowSeconds = 9 }), "window_seconds must be between 10 and 3600"},
		"window too large":      {withRule(func(rule *Rule) { rule.WindowSeconds = 3601 }), "window_seconds must be between 10 and 3600"},
		"unknown field":         {withCondition(Condition{Field: "colour", Operator: "equals", Value: "x"}), `condition 1: unknown field "colour"`},
		"url equals":            {withCondition(Condition{Field: "url", Operator: "equals", Value: "x"}), `condition 1: url does not support "equals"`},
		"status contains":       {withCondition(Condition{Field: "status", Operator: "contains", Value: "4"}), `condition 1: status does not support "contains"`},
		"tag without name":      {withCondition(Condition{Field: "tag", Operator: "exists"}), "condition 1: tag needs a name"},
		"long value":            {withCondition(Condition{Field: "body", Operator: "contains", Value: strings.Repeat("a", 1025)}), "condition 1: value is longer than 1024 bytes"},
		"long name":             {withCondition(Condition{Field: "tag", Name: strings.Repeat("a", 1025), Operator: "exists"}), "condition 1: name is longer than 1024 bytes"},
		"empty value":           {withCondition(Condition{Field: "method", Operator: "equals"}), "condition 1: value is required"},
		"non numeric status":    {withCondition(Condition{Field: "status", Operator: "equals", Value: "abc"}), `condition 1: invalid number "abc"`},
		"status out of range":   {withCondition(Condition{Field: "status", Operator: "equals", Value: "70000"}), `condition 1: invalid number "70000"`},
		"negative latency":      {withCondition(Condition{Field: "latency_ms", Operator: "greater_than", Value: "-1"}), `condition 1: invalid number "-1"`},
		"between reversed":      {withCondition(Condition{Field: "status", Operator: "between", Value: "5..1"}), "condition 1: between needs N..M with N <= M"},
		"between without range": {withCondition(Condition{Field: "status", Operator: "between", Value: "5"}), "condition 1: between needs N..M with N <= M"},
		"class 6xx":             {withCondition(Condition{Field: "status", Operator: "class", Value: "6xx"}), "condition 1: class must be one of 1xx, 2xx, 3xx, 4xx, 5xx"},
		"invalid regex":         {withCondition(Condition{Field: "body", Operator: "regex", Value: "("}), "condition 1: invalid regex: error parsing regexp: missing closing ): `(`"},
		"second condition bad":  {validDiscordRule(statusEquals(), Condition{Field: "colour", Operator: "equals", Value: "x"}), `condition 2: unknown field "colour"`},
		"bad channel":           {withRule(func(rule *Rule) { rule.Channel = "email" }), "channel must be discord or telegram"},
		"ftp webhook":           {withRule(func(rule *Rule) { rule.DiscordWebhookURL = "ftp://discord.example/x" }), "discord_webhook_url must be an http or https URL"},
		"webhook without host":  {withRule(func(rule *Rule) { rule.DiscordWebhookURL = "https:///x" }), "discord_webhook_url must be an http or https URL"},
		"bad telegram token": {withRule(func(rule *Rule) {
			rule.Channel = ChannelTelegram
			rule.TelegramBotToken = "1:a/b"
			rule.TelegramChatID = "5"
		}), "telegram_bot_token is not a valid bot token"},
		"empty chat id": {withRule(func(rule *Rule) {
			rule.Channel = ChannelTelegram
			rule.TelegramBotToken = "123:abc-DEF_1"
		}), "telegram_chat_id is required"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Compile(test.rule)
			if err == nil {
				t.Fatalf("expected error %q", test.message)
			}
			if err.Error() != test.message {
				t.Fatalf("unexpected error: got %q, want %q", err.Error(), test.message)
			}
		})
	}
}

func TestCompileErrorsNeverContainSecrets(t *testing.T) {
	const marker = "SECRETMARKER9f3a"
	discordRule := validDiscordRule(statusEquals())
	discordRule.DiscordWebhookURL = "ftp://" + marker + "/x"
	telegramRule := validDiscordRule(statusEquals())
	telegramRule.Channel = ChannelTelegram
	telegramRule.TelegramBotToken = marker + "/bad"
	telegramRule.TelegramChatID = "5"
	for name, rule := range map[string]Rule{"discord": discordRule, "telegram": telegramRule} {
		t.Run(name, func(t *testing.T) {
			_, err := Compile(rule)
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("error leaks secret: %v", err)
			}
		})
	}
}

func TestCompileAcceptsValidRules(t *testing.T) {
	telegramRule := validDiscordRule(statusEquals())
	telegramRule.Channel = ChannelTelegram
	telegramRule.TelegramBotToken = "123456:AA-bb_CC"
	telegramRule.TelegramChatID = "-1001"
	tests := map[string]Rule{
		"discord":        validDiscordRule(statusEquals()),
		"telegram":       telegramRule,
		"body only":      validDiscordRule(Condition{Field: "body", Operator: "contains", Value: "captcha"}),
		"between":        validDiscordRule(Condition{Field: "latency_ms", Operator: "between", Value: "100..200"}),
		"class":          validDiscordRule(Condition{Field: "status", Operator: "class", Value: "5xx"}),
		"missing header": validDiscordRule(Condition{Field: "response_header", Name: "x-cache", Operator: "missing"}),
		"http webhook": func() Rule {
			rule := validDiscordRule(statusEquals())
			rule.DiscordWebhookURL = "http://localhost:9/x"
			return rule
		}(),
	}
	for name, rule := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(rule); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCompilePlacesBodyConditionsLast(t *testing.T) {
	rule := validDiscordRule(
		Condition{Field: "body", Operator: "contains", Value: "captcha"},
		Condition{Field: "status", Operator: "equals", Value: "403"},
		Condition{Field: "method", Operator: "equals", Value: "GET"},
	)
	compiled, err := Compile(rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gotFields := []string{}
	for _, condition := range compiled.conditions {
		gotFields = append(gotFields, condition.field)
	}
	if strings.Join(gotFields, ",") != "status,method,body" {
		t.Fatalf("unexpected order: %v", gotFields)
	}
}
