package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"
)

const (
	maxRetryWait         = 10 * time.Second
	defaultRetryWait     = time.Second
	maxResponseBytes     = 64 << 10
	discordMessageLimit  = 2000
	telegramMessageLimit = 4096
	errorRateLimited     = "rate limited"

	maxResponseRetrySeconds = 3600
)

type Delivery struct {
	Delivery   string
	StatusCode int
	Error      string
}

type discordAllowedMentions struct {
	Parse []string `json:"parse"`
}

type discordPayload struct {
	Content         string                 `json:"content"`
	AllowedMentions discordAllowedMentions `json:"allowed_mentions"`
}

type telegramPayload struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

type retryAfterResponse struct {
	RetryAfter float64 `json:"retry_after"`
	Parameters struct {
		RetryAfter float64 `json:"retry_after"`
	} `json:"parameters"`
}

func (dispatcher *Dispatcher) deliver(ctx context.Context, rule Rule, text string) Delivery {
	endpoint := rule.DiscordWebhookURL
	var payload any = discordPayload{Content: text, AllowedMentions: discordAllowedMentions{Parse: []string{}}}
	if rule.Channel == ChannelTelegram {
		endpoint = dispatcher.telegramBaseURL + "/bot" + rule.TelegramBotToken + "/sendMessage"
		payload = telegramPayload{ChatID: rule.TelegramChatID, Text: text}
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return dispatcher.failure(rule, 0, "request failed")
	}

	statusCode, retryWait, err := dispatcher.post(ctx, endpoint, payloadBytes)
	if err == nil && statusCode == http.StatusTooManyRequests {
		if retryWait == 0 {
			retryWait = defaultRetryWait
		}
		if retryWait > maxRetryWait {
			return dispatcher.failure(rule, statusCode, errorRateLimited)
		}
		timer := time.NewTimer(retryWait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return dispatcher.failure(rule, 0, "canceled")
		}
		statusCode, _, err = dispatcher.post(ctx, endpoint, payloadBytes)
		if err == nil && statusCode == http.StatusTooManyRequests {
			return dispatcher.failure(rule, statusCode, errorRateLimited)
		}
	}
	if err != nil {
		return dispatcher.failure(rule, 0, describeRequestError(err))
	}
	if statusCode < 200 || statusCode > 299 {
		return dispatcher.failure(rule, statusCode, fmt.Sprintf("HTTP %d", statusCode))
	}
	return Delivery{Delivery: DeliverySent, StatusCode: statusCode}
}

func (dispatcher *Dispatcher) failure(rule Rule, statusCode int, description string) Delivery {
	dispatcher.logger.Warn("alert delivery failed", "rule_id", rule.RuleID, "status", statusCode, "error", description)
	return Delivery{Delivery: DeliveryFailed, StatusCode: statusCode, Error: description}
}

func (dispatcher *Dispatcher) post(ctx context.Context, endpoint string, payload []byte) (int, time.Duration, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := dispatcher.client.Do(request)
	if err != nil {
		return 0, 0, err
	}
	defer response.Body.Close()
	responseBytes, _ := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if response.StatusCode != http.StatusTooManyRequests {
		return response.StatusCode, 0, nil
	}
	var parsed retryAfterResponse
	if json.Unmarshal(responseBytes, &parsed) != nil {
		return response.StatusCode, 0, nil
	}
	retryAfterSeconds := parsed.RetryAfter
	if retryAfterSeconds <= 0 {
		retryAfterSeconds = parsed.Parameters.RetryAfter
	}
	if retryAfterSeconds <= 0 {
		return response.StatusCode, 0, nil
	}
	retryAfterSeconds = min(retryAfterSeconds, maxResponseRetrySeconds)
	return response.StatusCode, time.Duration(retryAfterSeconds * float64(time.Second)), nil
}

func messageLimit(channel string) int {
	if channel == ChannelTelegram {
		return telegramMessageLimit
	}
	return discordMessageLimit
}

func describeRequestError(err error) string {
	var netError net.Error
	var dnsError *net.DNSError
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netError) && netError.Timeout():
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.As(err, &dnsError):
		return "dns lookup failed"
	}
	return "request failed"
}
