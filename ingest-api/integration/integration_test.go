//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	bulkRecordCount              = 1000
	revocationDeadline           = 35 * time.Second
	readBackDeadline             = 20 * time.Second
	alertDeliveryDeadline        = 45 * time.Second
	webhookReceiverStartDeadline = 90 * time.Second
	composeLogLineLimit          = "80"
)

var (
	composeProjectName  string
	runIdentifier       string
	baseURL             string
	adminToken          string
	clickHousePasswords []string
	bodyMarker          string
	webhookSecret       string
	telegramTokenMarker string
	createdKeys         []string
	stackEnvironment    []string
	httpClient          = &http.Client{Timeout: 30 * time.Second}
)

func randomHex(byteCount int) string {
	randomBytes := make([]byte, byteCount)
	if _, err := rand.Read(randomBytes); err != nil {
		panic(err)
	}
	return hex.EncodeToString(randomBytes)
}

func freeLoopbackPort() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	return port, err
}

func prepareStackEnvironment() error {
	port := os.Getenv("SCRAPELOG_IT_PORT")
	if port == "" {
		var err error
		port, err = freeLoopbackPort()
		if err != nil {
			return err
		}
	}
	runIdentifier = randomHex(3)
	composeProjectName = "scrapelog-it-" + runIdentifier
	baseURL = "http://127.0.0.1:" + port
	adminToken = randomHex(16)
	clickHousePasswords = []string{randomHex(16), randomHex(16), randomHex(16), randomHex(16)}
	bodyMarker = "marker-" + randomHex(8)
	webhookSecret = randomHex(16)
	telegramTokenMarker = randomHex(16)
	stackEnvironment = append(os.Environ(),
		"CLICKHOUSE_ADMIN_PASSWORD="+clickHousePasswords[0],
		"CLICKHOUSE_INGEST_PASSWORD="+clickHousePasswords[1],
		"CLICKHOUSE_READER_PASSWORD="+clickHousePasswords[2],
		"CLICKHOUSE_ARCHIVER_PASSWORD="+clickHousePasswords[3],
		"ADMIN_TOKEN="+adminToken,
		"INGEST_API_PORT="+port,
		"CLICKHOUSE_URL=http://clickhouse:8123",
		"CLICKHOUSE_DATABASE=scrapelog",
		"CLICKHOUSE_INGEST_USER=ingest_writer",
		"S3_ENDPOINT=",
		"S3_BUCKET=",
		"S3_ACCESS_KEY_ID=",
		"S3_SECRET_ACCESS_KEY=",
	)
	return nil
}

func composeCommand(arguments ...string) *exec.Cmd {
	command := exec.Command("docker", append([]string{"compose", "-p", composeProjectName}, arguments...)...)
	command.Dir = "../.."
	command.Env = stackEnvironment
	return command
}

func composeLogs(service string) (string, error) {
	output, err := composeCommand("logs", "--no-color", service).CombinedOutput()
	return string(output), err
}

func TestMain(m *testing.M) {
	if err := prepareStackEnvironment(); err != nil {
		fmt.Fprintln(os.Stderr, "preparing stack environment failed:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "integration stack project:", composeProjectName, "api:", baseURL)

	exitCode := 0
	upOutput, upErr := composeCommand("up", "-d", "--build", "--wait", "clickhouse", "ingest-api").CombinedOutput()
	if upErr != nil {
		fmt.Fprintf(os.Stderr, "compose up failed: %v\n%s\n", upErr, upOutput)
		logsOutput, _ := composeCommand("logs", "--no-color", "--tail", composeLogLineLimit).CombinedOutput()
		fmt.Fprintf(os.Stderr, "%s\n", logsOutput)
		exitCode = 1
	} else {
		exitCode = m.Run()
	}

	if downOutput, downErr := composeCommand("down", "-v", "--remove-orphans", "--rmi", "local").CombinedOutput(); downErr != nil {
		fmt.Fprintf(os.Stderr, "compose down failed for project %s: %v\n%s\n", composeProjectName, downErr, downOutput)
	}
	os.Exit(exitCode)
}

func doRequest(t *testing.T, method, path string, headers map[string]string, body []byte) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, baseURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building %s %s: %v", method, path, err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading response of %s %s: %v", method, path, err)
	}
	return response.StatusCode, responseBody
}

func createKey(t *testing.T, project string) (string, string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"project": project, "name": "integration"})
	statusCode, responseBody := doRequest(t, http.MethodPost, "/v1/admin/keys",
		map[string]string{"Authorization": "Bearer " + adminToken, "Content-Type": "application/json"}, payload)
	if statusCode != http.StatusCreated {
		t.Fatalf("creating key for %s returned %d: %s", project, statusCode, responseBody)
	}
	var created struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if err := json.Unmarshal(responseBody, &created); err != nil {
		t.Fatalf("decoding created key: %v", err)
	}
	createdKeys = append(createdKeys, created.Key)
	return created.KeyID, created.Key
}

func revokeKey(t *testing.T, keyID string) {
	t.Helper()
	statusCode, responseBody := doRequest(t, http.MethodDelete, "/v1/admin/keys/"+keyID,
		map[string]string{"Authorization": "Bearer " + adminToken}, nil)
	if statusCode != http.StatusNoContent {
		t.Fatalf("revoking key %s returned %d: %s", keyID, statusCode, responseBody)
	}
}

func postRecords(t *testing.T, fullKey string, records []map[string]any) (int, []byte) {
	t.Helper()
	payload, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("encoding records: %v", err)
	}
	return doRequest(t, http.MethodPost, "/v1/requests",
		map[string]string{"Authorization": "Bearer " + fullKey, "Content-Type": "application/json"}, payload)
}

type ingestResult struct {
	Accepted int               `json:"accepted"`
	Rejected int               `json:"rejected"`
	Errors   []json.RawMessage `json:"errors"`
	Warnings []json.RawMessage `json:"warnings"`
}

func postRecordsExpectingSuccess(t *testing.T, fullKey string, records []map[string]any) ingestResult {
	t.Helper()
	statusCode, responseBody := postRecords(t, fullKey, records)
	if statusCode != http.StatusOK {
		t.Fatalf("posting %d records returned %d: %s", len(records), statusCode, responseBody)
	}
	var result ingestResult
	if err := json.Unmarshal(responseBody, &result); err != nil {
		t.Fatalf("decoding ingest result: %v", err)
	}
	return result
}

func projectRowCount(t *testing.T, fullKey string) uint64 {
	t.Helper()
	statusCode, responseBody := doRequest(t, http.MethodGet, "/v1/stats",
		map[string]string{"Authorization": "Bearer " + fullKey}, nil)
	if statusCode != http.StatusOK {
		t.Fatalf("reading stats returned %d: %s", statusCode, responseBody)
	}
	var stats struct {
		RowCount uint64 `json:"row_count"`
	}
	if err := json.Unmarshal(responseBody, &stats); err != nil {
		t.Fatalf("decoding stats: %v", err)
	}
	return stats.RowCount
}

func waitForRowCount(t *testing.T, fullKey string, expected uint64, deadline time.Duration) {
	t.Helper()
	stopAt := time.Now().Add(deadline)
	var observed uint64
	for {
		observed = projectRowCount(t, fullKey)
		if observed == expected {
			return
		}
		if time.Now().After(stopAt) {
			t.Fatalf("row count is %d, want %d after %s", observed, expected, deadline)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func buildRecords(count int, extraFields map[string]any) []map[string]any {
	records := make([]map[string]any, 0, count)
	batchIdentifier := randomHex(4)
	for index := 0; index < count; index++ {
		record := map[string]any{
			"ts":         time.Now().UTC().Format(time.RFC3339Nano),
			"scraper":    "integration",
			"request_id": fmt.Sprintf("%s-%s-%d", runIdentifier, batchIdentifier, index),
			"method":     "GET",
			"url":        fmt.Sprintf("https://example.test/page/%d", index),
			"status":     200,
			"latency_ms": 12,
			"request_headers": map[string]string{
				"Authorization": "Bearer " + bodyMarker,
			},
			"request_body": bodyMarker,
			"body":         bodyMarker,
		}
		for name, value := range extraFields {
			record[name] = value
		}
		records = append(records, record)
	}
	return records
}

func signIn(t *testing.T) string {
	t.Helper()
	login := func(password string) *http.Response {
		payload, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
		request, err := http.NewRequest(http.MethodPost, baseURL+"/ui/api/login", bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("building login request: %v", err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := httpClient.Do(request)
		if err != nil {
			t.Fatalf("login request: %v", err)
		}
		response.Body.Close()
		return response
	}
	if response := login("wrong"); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login with a wrong password returned %d, want 401", response.StatusCode)
	}
	response := login(adminToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("login with the admin token returned %d, want 204", response.StatusCode)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == "scrapelog_session" {
			return "scrapelog_session=" + cookie.Value
		}
	}
	t.Fatal("login response set no session cookie")
	return ""
}

const webhookReceiverSource = `package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
)

func main() {
	webhookPath := os.Getenv("WEBHOOK_PATH")
	http.HandleFunc("/", func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != webhookPath {
			http.NotFound(responseWriter, request)
			return
		}
		body, _ := io.ReadAll(request.Body)
		fmt.Println("body", base64.StdEncoding.EncodeToString(body))
		responseWriter.WriteHeader(http.StatusNoContent)
	})
	listener, err := net.Listen("tcp", ":8080")
	if err != nil {
		panic(err)
	}
	fmt.Println("listening")
	http.Serve(listener, nil)
}
`

func startWebhookReceiver(t *testing.T) (string, <-chan []byte) {
	t.Helper()
	sourceDirectory := t.TempDir()
	if err := os.WriteFile(sourceDirectory+"/main.go", []byte(webhookReceiverSource), 0o644); err != nil {
		t.Fatalf("writing the webhook receiver source: %v", err)
	}
	webhookPath := "/webhook/" + webhookSecret
	containerName := composeProjectName + "-webhook"
	runOutput, err := exec.Command("docker", "run", "-d", "--rm", "--name", containerName,
		"--network", composeProjectName+"_scrapelog", "--network-alias", "webhook",
		"-v", sourceDirectory+":/receiver:ro", "-e", "WEBHOOK_PATH="+webhookPath,
		"-e", "HOME=/tmp", "golang:1.27.3-trixie", "go", "run", "/receiver/main.go").CombinedOutput()
	if err != nil {
		t.Fatalf("starting the webhook receiver container: %v\n%s", err, runOutput)
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", containerName).Run() })

	logsCommand := exec.Command("docker", "logs", "-f", containerName)
	logsPipe, err := logsCommand.StdoutPipe()
	if err != nil {
		t.Fatalf("opening the webhook receiver logs: %v", err)
	}
	if err := logsCommand.Start(); err != nil {
		t.Fatalf("following the webhook receiver logs: %v", err)
	}
	t.Cleanup(func() { logsCommand.Process.Kill(); logsCommand.Wait() })

	listening := make(chan struct{})
	deliveredBodies := make(chan []byte, 10)
	go func() {
		scanner := bufio.NewScanner(logsPipe)
		scanner.Buffer(nil, 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "listening" {
				close(listening)
			} else if encodedBody, found := strings.CutPrefix(line, "body "); found {
				if body, decodeErr := base64.StdEncoding.DecodeString(encodedBody); decodeErr == nil {
					deliveredBodies <- body
				}
			}
		}
	}()
	select {
	case <-listening:
	case <-time.After(webhookReceiverStartDeadline):
		t.Fatalf("webhook receiver not listening within %s", webhookReceiverStartDeadline)
	}
	return "http://webhook:8080" + webhookPath, deliveredBodies
}

func sessionJSONRequest(t *testing.T, method, path, sessionCookie string, payload any) (int, []byte) {
	t.Helper()
	var encoded []byte
	if payload != nil {
		var err error
		encoded, err = json.Marshal(payload)
		if err != nil {
			t.Fatalf("encoding payload for %s %s: %v", method, path, err)
		}
	}
	return doRequest(t, method, path, map[string]string{"Cookie": sessionCookie, "Content-Type": "application/json"}, encoded)
}

func TestStack(t *testing.T) {
	projectA := "it-" + runIdentifier + "-a"
	projectB := "it-" + runIdentifier + "-b"
	var keyA, keyB, sessionCookie string

	t.Run("ingest_and_read_back", func(t *testing.T) {
		_, keyA = createKey(t, projectA)
		result := postRecordsExpectingSuccess(t, keyA, buildRecords(bulkRecordCount, nil))
		if result.Accepted != bulkRecordCount || result.Rejected != 0 {
			t.Fatalf("accepted %d rejected %d errors %s", result.Accepted, result.Rejected, result.Errors)
		}
		if len(result.Warnings) == 0 {
			t.Fatal("expected body warnings because S3 is not configured")
		}
		waitForRowCount(t, keyA, bulkRecordCount, readBackDeadline)
	})

	t.Run("project_isolation", func(t *testing.T) {
		_, keyB = createKey(t, projectB)
		result := postRecordsExpectingSuccess(t, keyA, buildRecords(5, map[string]any{"project": projectB}))
		if result.Accepted != 5 {
			t.Fatalf("accepted %d, want 5", result.Accepted)
		}
		waitForRowCount(t, keyA, bulkRecordCount+5, readBackDeadline)
		if count := projectRowCount(t, keyB); count != 0 {
			t.Fatalf("project B sees %d rows before writing, want 0", count)
		}
		result = postRecordsExpectingSuccess(t, keyB, buildRecords(3, nil))
		if result.Accepted != 3 {
			t.Fatalf("project B accepted %d, want 3", result.Accepted)
		}
		waitForRowCount(t, keyB, 3, readBackDeadline)
		if count := projectRowCount(t, keyA); count != bulkRecordCount+5 {
			t.Fatalf("project A count is %d after B wrote, want %d", count, bulkRecordCount+5)
		}
	})

	t.Run("revocation", func(t *testing.T) {
		revokedKeyID, revokedKey := createKey(t, projectA)
		singleRecord := buildRecords(1, nil)
		if statusCode, responseBody := postRecords(t, revokedKey, singleRecord); statusCode != http.StatusOK {
			t.Fatalf("fresh key returned %d: %s", statusCode, responseBody)
		}
		revokeKey(t, revokedKeyID)
		revokedAt := time.Now()
		sawRejection := false
		for {
			statusCode, _ := postRecords(t, revokedKey, singleRecord)
			if statusCode == http.StatusUnauthorized {
				if !sawRejection {
					t.Logf("revoked key rejected after %s", time.Since(revokedAt))
				}
				sawRejection = true
			} else if sawRejection {
				t.Fatalf("revoked key returned %d after first 401", statusCode)
			}
			if sawRejection && time.Since(revokedAt) > 3*time.Second {
				break
			}
			if !sawRejection && time.Since(revokedAt) > revocationDeadline {
				t.Fatalf("revoked key still accepted after %s", revocationDeadline)
			}
			time.Sleep(time.Second)
		}
		if statusCode, responseBody := postRecords(t, keyA, singleRecord); statusCode != http.StatusOK {
			t.Fatalf("remaining key returned %d: %s", statusCode, responseBody)
		}
	})

	t.Run("ui_sign_in", func(t *testing.T) {
		if statusCode, responseBody := doRequest(t, http.MethodGet, "/ui/api/requests", nil, nil); statusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated search returned %d, want 401: %s", statusCode, responseBody)
		}
		sessionCookie = signIn(t)
	})

	t.Run("alert_rule_and_search", func(t *testing.T) {
		webhookURL, deliveredBodies := startWebhookReceiver(t)
		conditions := []map[string]string{
			{"field": "status", "operator": "equals", "value": "418"},
			{"field": "body", "operator": "contains", "value": bodyMarker},
		}
		statusCode, responseBody := sessionJSONRequest(t, http.MethodPost, "/ui/api/alert-rules", sessionCookie, map[string]any{
			"name":                "integration discord",
			"project":             projectA,
			"conditions":          conditions,
			"channel":             "discord",
			"discord_webhook_url": webhookURL,
			"window_seconds":      10,
			"enabled":             true,
		})
		if statusCode != http.StatusCreated {
			t.Fatalf("creating the discord rule returned %d: %s", statusCode, responseBody)
		}
		var createdRule struct {
			RuleID string `json:"rule_id"`
		}
		if err := json.Unmarshal(responseBody, &createdRule); err != nil {
			t.Fatalf("decoding the created rule: %v", err)
		}
		statusCode, responseBody = sessionJSONRequest(t, http.MethodPost, "/ui/api/alert-rules", sessionCookie, map[string]any{
			"name":               "integration telegram",
			"project":            projectA,
			"conditions":         conditions,
			"channel":            "telegram",
			"telegram_bot_token": "1:" + telegramTokenMarker,
			"telegram_chat_id":   "1",
			"window_seconds":     10,
			"enabled":            false,
		})
		if statusCode != http.StatusCreated {
			t.Fatalf("creating the telegram rule returned %d: %s", statusCode, responseBody)
		}
		statusCode, responseBody = sessionJSONRequest(t, http.MethodGet, "/ui/api/alert-rules", sessionCookie, nil)
		if statusCode != http.StatusOK {
			t.Fatalf("listing rules returned %d: %s", statusCode, responseBody)
		}
		for _, secret := range []string{webhookSecret, telegramTokenMarker} {
			if strings.Contains(string(responseBody), secret) {
				t.Fatalf("rule list contains a secret of length %d", len(secret))
			}
		}

		const matchingCount = 50
		accepted := postRecordsExpectingSuccess(t, keyA, buildRecords(matchingCount, map[string]any{"status": 418}))
		if accepted.Accepted != matchingCount {
			t.Fatalf("accepted %d matching records, want %d", accepted.Accepted, matchingCount)
		}
		accepted = postRecordsExpectingSuccess(t, keyA, buildRecords(20, nil))
		if accepted.Accepted != 20 {
			t.Fatalf("accepted %d non-matching records, want 20", accepted.Accepted)
		}

		searchPath := "/ui/api/requests?limit=500&q=" + url.QueryEscape("status:418 project:"+projectA)
		stopAt := time.Now().Add(readBackDeadline)
		var searchResult struct {
			Rows []struct {
				Status  int    `json:"status"`
				Project string `json:"project"`
			} `json:"rows"`
		}
		for {
			statusCode, responseBody = sessionJSONRequest(t, http.MethodGet, searchPath, sessionCookie, nil)
			if statusCode != http.StatusOK {
				t.Fatalf("search returned %d: %s", statusCode, responseBody)
			}
			if err := json.Unmarshal(responseBody, &searchResult); err != nil {
				t.Fatalf("decoding search: %v", err)
			}
			if len(searchResult.Rows) == matchingCount {
				break
			}
			if time.Now().After(stopAt) {
				t.Fatalf("search returned %d rows, want %d after %s", len(searchResult.Rows), matchingCount, readBackDeadline)
			}
			time.Sleep(500 * time.Millisecond)
		}
		for _, row := range searchResult.Rows {
			if row.Status != 418 || row.Project != projectA {
				t.Fatalf("search row has status %d and project %q, want 418 and %q", row.Status, row.Project, projectA)
			}
		}

		select {
		case deliveredBody := <-deliveredBodies:
			var message struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(deliveredBody, &message); err != nil {
				t.Fatalf("decoding the webhook message: %v", err)
			}
			if !strings.Contains(message.Content, "50 matching requests in the last 10 s.") {
				t.Fatalf("webhook message is %q", message.Content)
			}
		case <-time.After(alertDeliveryDeadline):
			t.Fatalf("no webhook message within %s", alertDeliveryDeadline)
		}
		select {
		case extraBody := <-deliveredBodies:
			t.Fatalf("second webhook message: %s", extraBody)
		case <-time.After(15 * time.Second):
		}

		eventsPath := "/ui/api/alert-events?rule_id=" + url.QueryEscape(createdRule.RuleID)
		stopAt = time.Now().Add(readBackDeadline)
		var events []struct {
			MatchCount uint32          `json:"match_count"`
			Delivery   string          `json:"delivery"`
			Samples    json.RawMessage `json:"samples"`
		}
		for {
			statusCode, responseBody = sessionJSONRequest(t, http.MethodGet, eventsPath, sessionCookie, nil)
			if statusCode != http.StatusOK {
				t.Fatalf("listing events returned %d: %s", statusCode, responseBody)
			}
			if err := json.Unmarshal(responseBody, &events); err != nil {
				t.Fatalf("decoding events: %v", err)
			}
			if len(events) == 1 {
				break
			}
			if time.Now().After(stopAt) {
				t.Fatalf("rule has %d events, want 1 after %s", len(events), readBackDeadline)
			}
			time.Sleep(500 * time.Millisecond)
		}
		var samples []json.RawMessage
		if err := json.Unmarshal(events[0].Samples, &samples); err != nil {
			t.Fatalf("decoding samples: %v", err)
		}
		if events[0].MatchCount != matchingCount || events[0].Delivery != "sent" || len(samples) != 3 {
			t.Fatalf("event has match_count %d, delivery %q and %d samples, want %d, sent and 3", events[0].MatchCount, events[0].Delivery, len(samples), matchingCount)
		}
	})

	t.Run("logs_contain_no_secrets", func(t *testing.T) {
		apiLogs, err := composeLogs("ingest-api")
		if err != nil {
			t.Fatalf("reading ingest-api logs: %v", err)
		}
		clickHouseLogs, err := composeLogs("clickhouse")
		if err != nil {
			t.Fatalf("reading clickhouse logs: %v", err)
		}
		if !strings.Contains(apiLogs, `"msg"`) {
			t.Fatal("ingest-api log has no structured startup line")
		}
		if strings.TrimSpace(clickHouseLogs) == "" {
			t.Fatal("clickhouse log is empty")
		}

		commonNeedles := append([]string{adminToken, bodyMarker, webhookSecret, telegramTokenMarker}, createdKeys...)
		apiOnlyNeedles := append([]string{"sl_", "Bearer"}, clickHousePasswords...)
		for _, needle := range commonNeedles {
			if strings.Contains(apiLogs, needle) {
				t.Errorf("ingest-api log contains a secret of length %d", len(needle))
			}
			if strings.Contains(clickHouseLogs, needle) {
				t.Errorf("clickhouse log contains a secret of length %d", len(needle))
			}
		}
		for _, needle := range apiOnlyNeedles {
			if strings.Contains(apiLogs, needle) {
				t.Errorf("ingest-api log contains forbidden text of length %d", len(needle))
			}
		}
	})
}
