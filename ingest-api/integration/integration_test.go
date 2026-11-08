//go:build integration

package integration

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	bulkRecordCount     = 1000
	revocationDeadline  = 35 * time.Second
	readBackDeadline    = 20 * time.Second
	composeLogLineLimit = "80"
)

var (
	composeProjectName  string
	runIdentifier       string
	baseURL             string
	adminToken          string
	clickHousePasswords []string
	bodyMarker          string
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
	clickHousePasswords = []string{randomHex(16), randomHex(16), randomHex(16)}
	bodyMarker = "marker-" + randomHex(8)
	stackEnvironment = append(os.Environ(),
		"CLICKHOUSE_ADMIN_PASSWORD="+clickHousePasswords[0],
		"CLICKHOUSE_INGEST_PASSWORD="+clickHousePasswords[1],
		"CLICKHOUSE_READER_PASSWORD="+clickHousePasswords[2],
		"ADMIN_TOKEN="+adminToken,
		"GRAFANA_ADMIN_PASSWORD="+randomHex(16),
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

func TestStack(t *testing.T) {
	projectA := "it-" + runIdentifier + "-a"
	projectB := "it-" + runIdentifier + "-b"
	var keyA, keyB string

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

		commonNeedles := append([]string{adminToken, bodyMarker}, createdKeys...)
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
