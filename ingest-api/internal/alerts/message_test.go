package alerts

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func newTestWindow(rule *Rule, sampleCount int, urlLength int) *alertWindow {
	baseTime := time.Date(2026, 1, 2, 3, 4, 5, 900000000, time.UTC)
	window := &alertWindow{rule: rule, matchCount: uint32(sampleCount), earliestTimestamp: baseTime, latestTimestamp: baseTime.Add(10 * time.Second)}
	for index := 0; index < sampleCount; index++ {
		window.samples = append(window.samples, Match{RuleID: rule.RuleID, Project: rule.Project, Timestamp: baseTime, Method: "GET", URL: "https://example.com/" + strings.Repeat("a", urlLength), Status: 500, LatencyMs: 12})
	}
	return window
}

func TestWindowMessageFitsChannelLimits(t *testing.T) {
	rule := &Rule{Name: "errors", Project: "shop", WindowSeconds: 60}
	for channel, limit := range map[string]int{ChannelDiscord: discordMessageLimit, ChannelTelegram: telegramMessageLimit} {
		window := newTestWindow(rule, 3, 5000)
		message := windowMessage(window, 0, "https://logs.example.com", messageLimit(channel))
		if utf16Length(message) > limit {
			t.Fatalf("%s message is %d units, limit %d", channel, utf16Length(message), limit)
		}
		if !strings.HasPrefix(message, "errors on shop: 3 matching requests in the last 60 s.") {
			t.Fatalf("%s header missing: %q", channel, message)
		}
		if !strings.Contains(message, "Open in scrapelog: https://logs.example.com/requests?") {
			t.Fatalf("%s link missing", channel)
		}
	}
}

func TestWindowMessageLink(t *testing.T) {
	window := newTestWindow(&Rule{Name: "errors", Project: "shop", WindowSeconds: 60}, 1, 5)
	message := windowMessage(window, 0, "https://logs.example.com", discordMessageLimit)
	for _, expected := range []string{"q=project%3Ashop", "from=2026-01-02T03%3A04%3A05Z", "to=2026-01-02T03%3A04%3A16Z"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("link missing %q in %q", expected, message)
		}
	}

	window = newTestWindow(&Rule{Name: "errors", WindowSeconds: 60}, 1, 5)
	message = windowMessage(window, 0, "https://logs.example.com", discordMessageLimit)
	linkLine := message[strings.Index(message, "Open in scrapelog: ")+len("Open in scrapelog: "):]
	parsedLink, err := url.Parse(linkLine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsedLink.Query().Has("q") {
		t.Fatalf("unexpected q in %q", linkLine)
	}

	message = windowMessage(window, 0, "", discordMessageLimit)
	if strings.Contains(message, "Open in scrapelog") {
		t.Fatalf("unexpected link in %q", message)
	}
}

func TestWindowMessageSingularAndDropped(t *testing.T) {
	window := newTestWindow(&Rule{Name: "errors", WindowSeconds: 60}, 1, 5)
	message := windowMessage(window, 2, "", discordMessageLimit)
	if !strings.HasPrefix(message, "errors: 1 matching request in the last 60 s.") {
		t.Fatalf("unexpected header: %q", message)
	}
	if !strings.Contains(message, "2 further matches were dropped.") {
		t.Fatalf("dropped line missing: %q", message)
	}
}

func TestTruncateTextCountsUTF16(t *testing.T) {
	text := strings.Repeat("😀", 10)
	truncated := truncateText(text, 10)
	if utf16Length(truncated) > 10 {
		t.Fatalf("truncated to %d units", utf16Length(truncated))
	}
	if !strings.HasSuffix(truncated, "…") {
		t.Fatalf("missing ellipsis: %q", truncated)
	}
	if truncateText(text, 20) != text {
		t.Fatal("text at the limit must not be truncated")
	}
}
