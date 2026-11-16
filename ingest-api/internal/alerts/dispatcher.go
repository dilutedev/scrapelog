package alerts

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	queueCapacity      = 10000
	maxSamplesPerAlert = 3
	flushInterval      = time.Second
	eventInsertTimeout = 10 * time.Second
	deliveryTimeout    = 10 * time.Second
	DeliverySent       = "sent"
	DeliveryFailed     = "failed"
)

type PendingAlert struct {
	Rule  *Rule
	Match Match
}

type eventRecorder interface {
	InsertEvent(ctx context.Context, event Event) error
}

type alertWindow struct {
	rule              *Rule
	closesAt          time.Time
	matchCount        uint32
	samples           []Match
	earliestTimestamp time.Time
	latestTimestamp   time.Time
}

type sampleSummary struct {
	Timestamp   time.Time `json:"ts"`
	Project     string    `json:"project"`
	Domain      string    `json:"domain"`
	RequestID   string    `json:"request_id"`
	Method      string    `json:"method"`
	URL         string    `json:"url"`
	Status      uint16    `json:"status"`
	LatencyMs   uint32    `json:"latency_ms"`
	BodySnippet string    `json:"body_snippet,omitempty"`
}

type Dispatcher struct {
	logger          *slog.Logger
	recorder        eventRecorder
	publicURL       string
	client          *http.Client
	telegramBaseURL string
	queue           chan PendingAlert
	droppedMutex    sync.Mutex
	droppedByRule   map[string]uint32
	windows         map[string]*alertWindow
	sends           sync.WaitGroup
	stop            chan context.Context
	done            chan struct{}
}

func NewDispatcher(logger *slog.Logger, recorder eventRecorder, publicURL string) *Dispatcher {
	return &Dispatcher{
		logger:    logger,
		recorder:  recorder,
		publicURL: publicURL,
		client: &http.Client{
			Timeout: deliveryTimeout,
			CheckRedirect: func(request *http.Request, previousRequests []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		telegramBaseURL: "https://api.telegram.org",
		queue:           make(chan PendingAlert, queueCapacity),
		droppedByRule:   map[string]uint32{},
		windows:         map[string]*alertWindow{},
		stop:            make(chan context.Context),
		done:            make(chan struct{}),
	}
}

func (dispatcher *Dispatcher) Enqueue(pendingAlerts []PendingAlert) {
	for _, pendingAlert := range pendingAlerts {
		select {
		case dispatcher.queue <- pendingAlert:
		default:
			dispatcher.droppedMutex.Lock()
			dispatcher.droppedByRule[pendingAlert.Match.RuleID]++
			dispatcher.droppedMutex.Unlock()
		}
	}
}

func (dispatcher *Dispatcher) Run() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case pendingAlert := <-dispatcher.queue:
			dispatcher.addMatch(pendingAlert, time.Now())
		case now := <-ticker.C:
			dispatcher.flushWindows(context.Background(), now)
		case flushContext := <-dispatcher.stop:
			for draining := true; draining; {
				select {
				case pendingAlert := <-dispatcher.queue:
					dispatcher.addMatch(pendingAlert, time.Now())
				default:
					draining = false
				}
			}
			dispatcher.flushWindows(flushContext, time.Now().Add(maxWindowSeconds*time.Second))
			dispatcher.sends.Wait()
			close(dispatcher.done)
			return
		}
	}
}

func (dispatcher *Dispatcher) Shutdown(ctx context.Context) {
	select {
	case dispatcher.stop <- ctx:
	case <-ctx.Done():
		return
	}
	select {
	case <-dispatcher.done:
	case <-ctx.Done():
	}
}

func (dispatcher *Dispatcher) SendTest(ctx context.Context, rule Rule) Delivery {
	return dispatcher.deliver(ctx, rule, testMessage(rule))
}

func (dispatcher *Dispatcher) addMatch(pendingAlert PendingAlert, now time.Time) {
	match := pendingAlert.Match
	window, exists := dispatcher.windows[match.RuleID]
	if !exists {
		window = &alertWindow{
			closesAt:          now.Add(time.Duration(pendingAlert.Rule.WindowSeconds) * time.Second),
			earliestTimestamp: match.Timestamp,
			latestTimestamp:   match.Timestamp,
		}
		dispatcher.windows[match.RuleID] = window
	}
	window.rule = pendingAlert.Rule
	window.matchCount++
	if len(window.samples) < maxSamplesPerAlert {
		window.samples = append(window.samples, match)
	}
	if match.Timestamp.Before(window.earliestTimestamp) {
		window.earliestTimestamp = match.Timestamp
	}
	if match.Timestamp.After(window.latestTimestamp) {
		window.latestTimestamp = match.Timestamp
	}
}

func (dispatcher *Dispatcher) flushWindows(ctx context.Context, cutoff time.Time) {
	for ruleID, window := range dispatcher.windows {
		if window.closesAt.After(cutoff) {
			continue
		}
		delete(dispatcher.windows, ruleID)
		droppedCount := dispatcher.takeDropped(ruleID)
		dispatcher.sends.Add(1)
		go func() {
			defer dispatcher.sends.Done()
			dispatcher.deliverWindow(ctx, window, droppedCount)
		}()
	}
}

func (dispatcher *Dispatcher) takeDropped(ruleID string) uint32 {
	dispatcher.droppedMutex.Lock()
	defer dispatcher.droppedMutex.Unlock()
	droppedCount := dispatcher.droppedByRule[ruleID]
	delete(dispatcher.droppedByRule, ruleID)
	return droppedCount
}

func (dispatcher *Dispatcher) deliverWindow(ctx context.Context, window *alertWindow, droppedCount uint32) {
	rule := *window.rule
	text := windowMessage(window, droppedCount, dispatcher.publicURL, messageLimit(rule.Channel))
	result := dispatcher.deliver(ctx, rule, text)
	event := Event{
		FiredAt:       time.Now().UTC(),
		RuleID:        rule.RuleID,
		RuleName:      rule.Name,
		Project:       rule.Project,
		MatchCount:    window.matchCount,
		DroppedCount:  droppedCount,
		Samples:       samplesJSON(window.samples),
		Delivery:      result.Delivery,
		DeliveryError: result.Error,
	}
	insertContext, cancel := context.WithTimeout(ctx, eventInsertTimeout)
	defer cancel()
	if err := dispatcher.recorder.InsertEvent(insertContext, event); err != nil {
		dispatcher.logger.Warn("recording alert event failed", "rule_id", rule.RuleID, "error", err)
	}
}

func samplesJSON(samples []Match) string {
	summaries := make([]sampleSummary, 0, len(samples))
	for _, sample := range samples {
		summaries = append(summaries, sampleSummary{
			Timestamp:   sample.Timestamp,
			Project:     sample.Project,
			Domain:      sample.Domain,
			RequestID:   sample.RequestID,
			Method:      sample.Method,
			URL:         sample.URL,
			Status:      sample.Status,
			LatencyMs:   sample.LatencyMs,
			BodySnippet: sample.BodySnippet,
		})
	}
	encoded, err := json.Marshal(summaries)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}
