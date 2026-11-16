package alerts

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

const maxSampleLineLength = 400

func windowMessage(window *alertWindow, droppedCount uint32, publicURL string, limit int) string {
	noun := "requests"
	if window.matchCount == 1 {
		noun = "request"
	}
	subject := window.rule.Name
	if window.rule.Project != "" {
		subject += " on " + window.rule.Project
	}
	header := fmt.Sprintf("%s: %d matching %s in the last %d s.", subject, window.matchCount, noun, window.rule.WindowSeconds)

	trailer := []string{}
	if droppedCount > 0 {
		trailer = append(trailer, fmt.Sprintf("%d further matches were dropped.", droppedCount))
	}
	if publicURL != "" {
		trailer = append(trailer, "Open in scrapelog: "+requestsLink(window, publicURL))
	}

	lines := []string{header}
	for _, sample := range window.samples {
		candidateLines := append(append(append([]string{}, lines...), sampleLine(sample)), trailer...)
		if utf16Length(strings.Join(candidateLines, "\n")) > limit {
			break
		}
		lines = append(lines, sampleLine(sample))
	}
	lines = append(lines, trailer...)
	return truncateText(strings.Join(lines, "\n"), limit)
}

func sampleLine(sample Match) string {
	line := fmt.Sprintf("%d %s %s (%d ms)", sample.Status, sample.Method, replaceControlCharacters(sample.URL), sample.LatencyMs)
	if sample.BodySnippet != "" {
		line += ` body: "` + replaceControlCharacters(sample.BodySnippet) + `"`
	}
	return truncateText(line, maxSampleLineLength)
}

func requestsLink(window *alertWindow, publicURL string) string {
	values := url.Values{}
	if window.rule.Project != "" {
		values.Set("q", "project:"+window.rule.Project)
	}
	earliest := window.earliestTimestamp.UTC().Truncate(time.Second)
	latest := window.latestTimestamp.UTC().Truncate(time.Second).Add(time.Second)
	values.Set("from", earliest.Format(time.RFC3339))
	values.Set("to", latest.Format(time.RFC3339))
	return publicURL + "/requests?" + values.Encode()
}

func testMessage(rule Rule) string {
	return `Test message from scrapelog for rule "` + rule.Name + `".`
}

func replaceControlCharacters(text string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, text)
}

func utf16Length(text string) int {
	units := 0
	for _, character := range text {
		units += utf16.RuneLen(character)
	}
	return units
}

func truncateText(text string, limit int) string {
	if utf16Length(text) <= limit {
		return text
	}
	var truncated strings.Builder
	units := 0
	for _, character := range text {
		characterUnits := utf16.RuneLen(character)
		if units+characterUnits > limit-1 {
			break
		}
		truncated.WriteRune(character)
		units += characterUnits
	}
	return truncated.String() + "…"
}
