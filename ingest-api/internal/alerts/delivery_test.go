package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const secretMarker = "SECRETMARKER7f3a"

type fakeEventRecorder struct {
	mutex  sync.Mutex
	events []Event
}

func (recorder *fakeEventRecorder) InsertEvent(ctx context.Context, event Event) error {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	recorder.events = append(recorder.events, event)
	return nil
}

func (recorder *fakeEventRecorder) recorded() []Event {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	return append([]Event{}, recorder.events...)
}

func newTestDispatcher(logBuffer io.Writer) (*Dispatcher, *fakeEventRecorder) {
	recorder := &fakeEventRecorder{}
	dispatcher := NewDispatcher(slog.New(slog.NewJSONHandler(logBuffer, nil)), recorder, "")
	return dispatcher, recorder
}

func TestDeliverDiscordPayload(t *testing.T) {
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		receivedBody, _ = io.ReadAll(request.Body)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	dispatcher, _ := newTestDispatcher(io.Discard)

	result := dispatcher.deliver(context.Background(), Rule{Channel: ChannelDiscord, DiscordWebhookURL: server.URL}, "hello")

	if result.Delivery != DeliverySent || result.StatusCode != 204 {
		t.Fatalf("unexpected result: %+v", result)
	}
	var payload struct {
		Content         string `json:"content"`
		AllowedMentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
	}
	if err := json.Unmarshal(receivedBody, &payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload.Content != "hello" || payload.AllowedMentions.Parse == nil || len(payload.AllowedMentions.Parse) != 0 {
		t.Fatalf("unexpected payload: %s", receivedBody)
	}
	if !bytes.Contains(receivedBody, []byte(`"parse":[]`)) {
		t.Fatalf("parse must encode as an empty array: %s", receivedBody)
	}
}

func TestDeliverTelegramPayload(t *testing.T) {
	var receivedPath string
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		receivedPath = request.URL.Path
		json.NewDecoder(request.Body).Decode(&receivedBody)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	dispatcher, _ := newTestDispatcher(io.Discard)
	dispatcher.telegramBaseURL = server.URL

	result := dispatcher.deliver(context.Background(), Rule{Channel: ChannelTelegram, TelegramBotToken: "123:abc", TelegramChatID: "-100"}, "hello")

	if result.Delivery != DeliverySent || result.StatusCode != 200 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if receivedPath != "/bot123:abc/sendMessage" {
		t.Fatalf("unexpected path %q", receivedPath)
	}
	if receivedBody["chat_id"] != "-100" || receivedBody["text"] != "hello" {
		t.Fatalf("unexpected body: %v", receivedBody)
	}
	if _, present := receivedBody["parse_mode"]; present {
		t.Fatal("parse_mode must not be set")
	}
}

func TestDeliverRetriesOnceOn429(t *testing.T) {
	tests := map[string]struct {
		channel   string
		responses []struct {
			status int
			body   string
		}
		expectedCalls  int32
		expectedResult string
		expectedError  string
	}{
		"discord retry_after then success": {ChannelDiscord, []struct {
			status int
			body   string
		}{{429, `{"retry_after":0.05}`}, {204, ""}}, 2, DeliverySent, ""},
		"telegram zero retry_after waits default": {ChannelTelegram, []struct {
			status int
			body   string
		}{{429, `{"parameters":{"retry_after":0}}`}, {200, ""}}, 2, DeliverySent, ""},
		"two rate limits": {ChannelDiscord, []struct {
			status int
			body   string
		}{{429, `{"retry_after":0.01}`}, {429, `{"retry_after":0.01}`}}, 2, DeliveryFailed, "rate limited"},
		"retry_after too long": {ChannelDiscord, []struct {
			status int
			body   string
		}{{429, `{"retry_after":60}`}}, 1, DeliveryFailed, "rate limited"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost {
					return
				}
				call := calls.Add(1)
				response := test.responses[min(int(call), len(test.responses))-1]
				writer.WriteHeader(response.status)
				writer.Write([]byte(response.body))
			}))
			defer server.Close()
			dispatcher, _ := newTestDispatcher(io.Discard)
			dispatcher.telegramBaseURL = server.URL

			result := dispatcher.deliver(context.Background(), Rule{Channel: test.channel, DiscordWebhookURL: server.URL, TelegramBotToken: "token", TelegramChatID: "1"}, "hello")

			if calls.Load() != test.expectedCalls {
				t.Fatalf("got %d calls, want %d", calls.Load(), test.expectedCalls)
			}
			if result.Delivery != test.expectedResult || result.Error != test.expectedError {
				t.Fatalf("unexpected result: %+v", result)
			}
		})
	}
}

func TestDeliverRecordsHTTPStatus(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	dispatcher, _ := newTestDispatcher(io.Discard)

	result := dispatcher.deliver(context.Background(), Rule{Channel: ChannelDiscord, DiscordWebhookURL: failing.URL}, "hello")
	if result.Delivery != DeliveryFailed || result.Error != "HTTP 500" || result.StatusCode != 500 {
		t.Fatalf("unexpected result: %+v", result)
	}

	var otherServerCalls atomic.Int32
	otherServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		otherServerCalls.Add(1)
	}))
	defer otherServer.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, otherServer.URL, http.StatusFound)
	}))
	defer redirecting.Close()

	result = dispatcher.deliver(context.Background(), Rule{Channel: ChannelDiscord, DiscordWebhookURL: redirecting.URL}, "hello")
	if result.Error != "HTTP 302" || otherServerCalls.Load() != 0 {
		t.Fatalf("unexpected result %+v, other server calls %d", result, otherServerCalls.Load())
	}
}

func closedListenerAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func TestDeliveryNeverLeaksSecrets(t *testing.T) {
	vocabulary := map[string]bool{"timeout": true, "connection refused": true}
	for _, failureKind := range []string{"timeout", "connection refused"} {
		for _, channel := range []string{ChannelDiscord, ChannelTelegram} {
			t.Run(failureKind+" "+channel, func(t *testing.T) {
				release := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					<-release
				}))
				defer server.Close()
				defer close(release)
				baseURL := server.URL
				if failureKind == "connection refused" {
					baseURL = "http://" + closedListenerAddress(t)
				}
				logBuffer := &bytes.Buffer{}
				dispatcher, recorder := newTestDispatcher(logBuffer)
				dispatcher.client.Timeout = 100 * time.Millisecond
				dispatcher.telegramBaseURL = baseURL
				rule := &Rule{RuleID: "rule-1", Name: "errors", Channel: channel, WindowSeconds: 10, DiscordWebhookURL: baseURL + "/webhook/" + secretMarker, TelegramBotToken: secretMarker, TelegramChatID: "1"}
				window := newTestWindow(rule, 1, 5)

				dispatcher.deliverWindow(context.Background(), window, 0)

				events := recorder.recorded()
				if len(events) != 1 {
					t.Fatalf("got %d events", len(events))
				}
				if !vocabulary[events[0].DeliveryError] || events[0].DeliveryError != failureKind {
					t.Fatalf("unexpected delivery error %q", events[0].DeliveryError)
				}
				eventJSON, _ := json.Marshal(events[0])
				if strings.Contains(string(eventJSON), secretMarker) {
					t.Fatalf("event leaks the secret: %s", eventJSON)
				}
				if strings.Contains(logBuffer.String(), secretMarker) {
					t.Fatalf("log leaks the secret: %s", logBuffer.String())
				}
			})
		}
	}
}
