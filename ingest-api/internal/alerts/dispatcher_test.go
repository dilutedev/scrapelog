package alerts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type capturedPosts struct {
	mutex  sync.Mutex
	bodies []string
}

func (posts *capturedPosts) add(body string) {
	posts.mutex.Lock()
	defer posts.mutex.Unlock()
	posts.bodies = append(posts.bodies, body)
}

func (posts *capturedPosts) all() []string {
	posts.mutex.Lock()
	defer posts.mutex.Unlock()
	return append([]string{}, posts.bodies...)
}

func newCapturingServer(posts *capturedPosts) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		posts.add(string(body))
		writer.WriteHeader(http.StatusNoContent)
	}))
}

func newPendingAlert(rule *Rule, requestID string) PendingAlert {
	return PendingAlert{Rule: rule, Match: Match{RuleID: rule.RuleID, Project: rule.Project, Timestamp: time.Now(), RequestID: requestID, Method: "GET", URL: "https://example.com/" + requestID, Domain: "example.com", Status: 500, LatencyMs: 20}}
}

func TestDispatcherSendsOneMessagePerWindow(t *testing.T) {
	posts := &capturedPosts{}
	server := newCapturingServer(posts)
	defer server.Close()
	dispatcher, recorder := newTestDispatcher(io.Discard)
	rule := &Rule{RuleID: "rule-1", Name: "errors", Project: "shop", Channel: ChannelDiscord, DiscordWebhookURL: server.URL, WindowSeconds: 60}
	startTime := time.Now()
	for index := 0; index < 50; index++ {
		dispatcher.addMatch(newPendingAlert(rule, "request"), startTime)
	}

	dispatcher.flushWindows(context.Background(), startTime.Add(59*time.Second))
	dispatcher.sends.Wait()
	if len(posts.all()) != 0 {
		t.Fatal("window must not flush before it closes")
	}
	dispatcher.flushWindows(context.Background(), startTime.Add(60*time.Second))
	dispatcher.sends.Wait()

	bodies := posts.all()
	if len(bodies) != 1 {
		t.Fatalf("got %d posts, want 1", len(bodies))
	}
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(bodies[0]), &payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(payload.Content, "errors on shop: 50 matching requests in the last 60 s.") {
		t.Fatalf("unexpected content: %q", payload.Content)
	}
	if lineCount := len(strings.Split(payload.Content, "\n")); lineCount != 4 {
		t.Fatalf("got %d lines, want header plus 3 samples", lineCount)
	}
	events := recorder.recorded()
	if len(events) != 1 || events[0].MatchCount != 50 || events[0].Delivery != DeliverySent {
		t.Fatalf("unexpected events: %+v", events)
	}
	var samples []map[string]any
	if err := json.Unmarshal([]byte(events[0].Samples), &samples); err != nil || len(samples) != 3 {
		t.Fatalf("unexpected samples %q: %v", events[0].Samples, err)
	}
}

func TestDispatcherReportsDroppedMatches(t *testing.T) {
	posts := &capturedPosts{}
	server := newCapturingServer(posts)
	defer server.Close()
	dispatcher, recorder := newTestDispatcher(io.Discard)
	dispatcher.queue = make(chan PendingAlert, 1)
	rule := &Rule{RuleID: "rule-1", Name: "errors", Channel: ChannelDiscord, DiscordWebhookURL: server.URL, WindowSeconds: 10}

	dispatcher.Enqueue([]PendingAlert{newPendingAlert(rule, "a"), newPendingAlert(rule, "b"), newPendingAlert(rule, "c")})
	if dispatcher.droppedByRule["rule-1"] != 2 {
		t.Fatalf("got drop count %d, want 2", dispatcher.droppedByRule["rule-1"])
	}
	dispatcher.addMatch(<-dispatcher.queue, time.Now())
	dispatcher.flushWindows(context.Background(), time.Now().Add(time.Minute))
	dispatcher.sends.Wait()

	bodies := posts.all()
	if len(bodies) != 1 || !strings.Contains(bodies[0], "2 further matches were dropped.") {
		t.Fatalf("unexpected posts: %v", bodies)
	}
	events := recorder.recorded()
	if len(events) != 1 || events[0].DroppedCount != 2 {
		t.Fatalf("unexpected events: %+v", events)
	}
	if dispatcher.droppedByRule["rule-1"] != 0 {
		t.Fatal("drop counter must reset")
	}
}

func TestDispatcherShutdownFlushesOpenWindows(t *testing.T) {
	posts := &capturedPosts{}
	server := newCapturingServer(posts)
	defer server.Close()
	dispatcher, recorder := newTestDispatcher(io.Discard)
	rule := &Rule{RuleID: "rule-1", Name: "errors", Channel: ChannelDiscord, DiscordWebhookURL: server.URL, WindowSeconds: 3600}
	go dispatcher.Run()
	alertsToSend := []PendingAlert{}
	for index := 0; index < 5; index++ {
		alertsToSend = append(alertsToSend, newPendingAlert(rule, "request"))
	}
	dispatcher.Enqueue(alertsToSend)

	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dispatcher.Shutdown(shutdownContext)

	if len(posts.all()) != 1 {
		t.Fatalf("got %d posts, want 1", len(posts.all()))
	}
	events := recorder.recorded()
	if len(events) != 1 || events[0].MatchCount != 5 {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestDispatcherShutdownReturnsWhenBudgetExpires(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)
	dispatcher, _ := newTestDispatcher(io.Discard)
	rule := &Rule{RuleID: "rule-1", Name: "errors", Channel: ChannelDiscord, DiscordWebhookURL: server.URL, WindowSeconds: 3600}
	go dispatcher.Run()
	dispatcher.Enqueue([]PendingAlert{newPendingAlert(rule, "request")})

	shutdownContext, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	startTime := time.Now()
	dispatcher.Shutdown(shutdownContext)

	if elapsed := time.Since(startTime); elapsed > time.Second {
		t.Fatalf("shutdown took %s", elapsed)
	}
}

func TestDispatcherShutdownWithoutRunReturnsWhenBudgetExpires(t *testing.T) {
	dispatcher, _ := newTestDispatcher(io.Discard)
	shutdownContext, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	startTime := time.Now()
	dispatcher.Shutdown(shutdownContext)
	if elapsed := time.Since(startTime); elapsed > time.Second {
		t.Fatalf("shutdown took %s", elapsed)
	}
}

func TestSamplesJSONIsAlwaysAnArray(t *testing.T) {
	if samplesJSON(nil) != "[]" {
		t.Fatalf("got %q", samplesJSON(nil))
	}
	encoded := samplesJSON([]Match{{Timestamp: time.Now(), Project: "shop", Domain: "example.com", RequestID: "abc", Method: "GET", URL: "https://example.com", Status: 500, LatencyMs: 5}})
	var samples []map[string]any
	if err := json.Unmarshal([]byte(encoded), &samples); err != nil || len(samples) != 1 {
		t.Fatalf("unexpected samples %q: %v", encoded, err)
	}
	for _, key := range []string{"ts", "project", "domain", "request_id", "method", "url", "status", "latency_ms"} {
		if _, present := samples[0][key]; !present {
			t.Fatalf("missing key %q in %q", key, encoded)
		}
	}
}
