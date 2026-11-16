package alerts

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultWindowSeconds   = 60
	MaxConditions          = 20
	minWindowSeconds       = 10
	maxWindowSeconds       = 3600
	maxConditionTextLength = 1024
	maxBodyScanBytes       = 1 << 20
	maxSnippetBytes        = 200

	ChannelDiscord  = "discord"
	ChannelTelegram = "telegram"

	fieldStatus         = "status"
	fieldLatency        = "latency_ms"
	fieldResponseSize   = "response_size"
	fieldMethod         = "method"
	fieldDomain         = "domain"
	fieldScraper        = "scraper"
	fieldProxy          = "proxy"
	fieldTLSProfile     = "tls_profile"
	fieldURL            = "url"
	fieldError          = "error"
	fieldResponseHeader = "response_header"
	fieldRequestHeader  = "request_header"
	fieldTag            = "tag"
	fieldBody           = "body"

	operatorEquals      = "equals"
	operatorNotEquals   = "not_equals"
	operatorBetween     = "between"
	operatorGreaterThan = "greater_than"
	operatorLessThan    = "less_than"
	operatorClass       = "class"
	operatorContains    = "contains"
	operatorNotContains = "not_contains"
	operatorRegex       = "regex"
	operatorExists      = "exists"
	operatorMissing     = "missing"
)

type fieldKind int

const (
	kindNumber fieldKind = iota
	kindText
	kindKeyed
	kindBody
)

type fieldSpec struct {
	kind      fieldKind
	bits      int
	operators []string
}

var fieldSpecs = map[string]fieldSpec{
	fieldStatus:         {kind: kindNumber, bits: 16, operators: []string{operatorEquals, operatorNotEquals, operatorBetween, operatorGreaterThan, operatorLessThan, operatorClass}},
	fieldLatency:        {kind: kindNumber, bits: 32, operators: []string{operatorEquals, operatorBetween, operatorGreaterThan, operatorLessThan}},
	fieldResponseSize:   {kind: kindNumber, bits: 32, operators: []string{operatorEquals, operatorBetween, operatorGreaterThan, operatorLessThan}},
	fieldMethod:         {kind: kindText, operators: []string{operatorEquals, operatorNotEquals}},
	fieldDomain:         {kind: kindText, operators: []string{operatorEquals, operatorNotEquals}},
	fieldScraper:        {kind: kindText, operators: []string{operatorEquals, operatorNotEquals}},
	fieldProxy:          {kind: kindText, operators: []string{operatorEquals, operatorNotEquals}},
	fieldTLSProfile:     {kind: kindText, operators: []string{operatorEquals, operatorNotEquals}},
	fieldURL:            {kind: kindText, operators: []string{operatorContains, operatorNotContains, operatorRegex}},
	fieldError:          {kind: kindText, operators: []string{operatorContains, operatorNotContains, operatorRegex}},
	fieldResponseHeader: {kind: kindKeyed, operators: []string{operatorEquals, operatorContains, operatorNotContains, operatorRegex, operatorExists, operatorMissing}},
	fieldRequestHeader:  {kind: kindKeyed, operators: []string{operatorEquals, operatorContains, operatorNotContains, operatorRegex, operatorExists, operatorMissing}},
	fieldTag:            {kind: kindKeyed, operators: []string{operatorEquals, operatorContains, operatorNotContains, operatorRegex, operatorExists, operatorMissing}},
	fieldBody:           {kind: kindBody, operators: []string{operatorContains, operatorNotContains, operatorRegex}},
}

var telegramTokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

type Condition struct {
	Field    string `json:"field"`
	Name     string `json:"name,omitempty"`
	Operator string `json:"operator"`
	Value    string `json:"value,omitempty"`
}

type Rule struct {
	RuleID            string
	Name              string
	Project           string
	Conditions        []Condition
	Channel           string
	DiscordWebhookURL string
	TelegramBotToken  string
	TelegramChatID    string
	WindowSeconds     uint32
	Enabled           bool
	Deleted           bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type compiledCondition struct {
	field      string
	name       string
	operator   string
	value      string
	lowerBound uint64
	upperBound uint64
	classDigit uint16
	pattern    *regexp.Regexp
}

type CompiledRule struct {
	Rule       Rule
	conditions []compiledCondition
}

func Compile(rule Rule) (CompiledRule, error) {
	if len(rule.Conditions) == 0 {
		return CompiledRule{}, errors.New("at least one condition is required")
	}
	if len(rule.Conditions) > MaxConditions {
		return CompiledRule{}, fmt.Errorf("at most %d conditions are allowed", MaxConditions)
	}
	if rule.WindowSeconds < minWindowSeconds || rule.WindowSeconds > maxWindowSeconds {
		return CompiledRule{}, fmt.Errorf("window_seconds must be between %d and %d", minWindowSeconds, maxWindowSeconds)
	}
	compiledConditions := make([]compiledCondition, 0, len(rule.Conditions))
	for index, condition := range rule.Conditions {
		compiled, err := compileCondition(index+1, condition)
		if err != nil {
			return CompiledRule{}, err
		}
		compiledConditions = append(compiledConditions, compiled)
	}
	if err := validateChannel(rule); err != nil {
		return CompiledRule{}, err
	}
	slices.SortStableFunc(compiledConditions, func(left compiledCondition, right compiledCondition) int {
		return bodyRank(left) - bodyRank(right)
	})
	return CompiledRule{Rule: rule, conditions: compiledConditions}, nil
}

func bodyRank(condition compiledCondition) int {
	if condition.field == fieldBody {
		return 1
	}
	return 0
}

func compileCondition(position int, condition Condition) (compiledCondition, error) {
	spec, known := fieldSpecs[condition.Field]
	if !known {
		return compiledCondition{}, fmt.Errorf("condition %d: unknown field %q", position, condition.Field)
	}
	if !slices.Contains(spec.operators, condition.Operator) {
		return compiledCondition{}, fmt.Errorf("condition %d: %s does not support %q", position, condition.Field, condition.Operator)
	}
	if spec.kind == kindKeyed && condition.Name == "" {
		return compiledCondition{}, fmt.Errorf("condition %d: %s needs a name", position, condition.Field)
	}
	if len(condition.Value) > maxConditionTextLength {
		return compiledCondition{}, fmt.Errorf("condition %d: value is longer than %d bytes", position, maxConditionTextLength)
	}
	if len(condition.Name) > maxConditionTextLength {
		return compiledCondition{}, fmt.Errorf("condition %d: name is longer than %d bytes", position, maxConditionTextLength)
	}
	existenceOperator := condition.Operator == operatorExists || condition.Operator == operatorMissing
	if condition.Value == "" && !existenceOperator {
		return compiledCondition{}, fmt.Errorf("condition %d: value is required", position)
	}
	compiled := compiledCondition{
		field:    condition.Field,
		name:     condition.Name,
		operator: condition.Operator,
		value:    condition.Value,
	}
	switch condition.Operator {
	case operatorBetween:
		lowerText, upperText, separated := strings.Cut(condition.Value, "..")
		lowerBound, lowerErr := strconv.ParseUint(lowerText, 10, spec.bits)
		upperBound, upperErr := strconv.ParseUint(upperText, 10, spec.bits)
		if !separated || lowerErr != nil || upperErr != nil || lowerBound > upperBound {
			return compiledCondition{}, fmt.Errorf("condition %d: between needs N..M with N <= M", position)
		}
		compiled.lowerBound = lowerBound
		compiled.upperBound = upperBound
	case operatorClass:
		if len(condition.Value) != 3 || condition.Value[0] < '1' || condition.Value[0] > '5' || condition.Value[1:] != "xx" {
			return compiledCondition{}, fmt.Errorf("condition %d: class must be one of 1xx, 2xx, 3xx, 4xx, 5xx", position)
		}
		compiled.classDigit = uint16(condition.Value[0] - '0')
	case operatorContains, operatorNotContains:
		pattern, err := regexp.Compile("(?i)" + regexp.QuoteMeta(condition.Value))
		if err != nil {
			return compiledCondition{}, fmt.Errorf("condition %d: invalid regex: %s", position, err)
		}
		compiled.pattern = pattern
	case operatorRegex:
		pattern, err := regexp.Compile(condition.Value)
		if err != nil {
			return compiledCondition{}, fmt.Errorf("condition %d: invalid regex: %s", position, err)
		}
		compiled.pattern = pattern
	default:
		if spec.kind == kindNumber {
			number, err := strconv.ParseUint(condition.Value, 10, spec.bits)
			if err != nil {
				return compiledCondition{}, fmt.Errorf("condition %d: invalid number %q", position, condition.Value)
			}
			compiled.lowerBound = number
		}
	}
	return compiled, nil
}

func validateChannel(rule Rule) error {
	switch rule.Channel {
	case ChannelDiscord:
		parsed, err := url.Parse(rule.DiscordWebhookURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return errors.New("discord_webhook_url must be an http or https URL")
		}
	case ChannelTelegram:
		if !telegramTokenPattern.MatchString(rule.TelegramBotToken) {
			return errors.New("telegram_bot_token is not a valid bot token")
		}
		if rule.TelegramChatID == "" {
			return errors.New("telegram_chat_id is required")
		}
	default:
		return errors.New("channel must be discord or telegram")
	}
	return nil
}
