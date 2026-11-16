package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	minimumAchievedFraction = 0.95
	revokeTimeout           = 30 * time.Second
	requestTimeout          = 30 * time.Second
)

type options struct {
	baseURL       string
	keyCount      int
	targetRate    int
	duration      time.Duration
	batchSize     int
	bodyBytes     int
	adminToken    string
	statusCounts  sync.Map
	accepted      atomic.Int64
	rejected      atomic.Int64
	transportFail atomic.Int64
	latencyMutex  sync.Mutex
	latencies     []time.Duration
}

type createdKey struct {
	KeyID string `json:"key_id"`
	Key   string `json:"key"`
}

type batchResponse struct {
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
}

type report struct {
	TargetRate      int            `json:"target_rate"`
	AchievedRate    float64        `json:"achieved_rate"`
	DurationSeconds float64        `json:"duration_seconds"`
	Accepted        int64          `json:"accepted"`
	Rejected        int64          `json:"rejected"`
	StatusCounts    map[string]int `json:"status_counts"`
	TransportErrors int64          `json:"transport_errors"`
	P95Milliseconds float64        `json:"p95_ms"`
}

func main() {
	os.Exit(run())
}

func run() (exitCode int) {
	runOptions := &options{adminToken: os.Getenv("ADMIN_TOKEN")}
	flag.StringVar(&runOptions.baseURL, "url", "http://127.0.0.1:8080", "ingest API base URL")
	flag.IntVar(&runOptions.keyCount, "keys", 6, "number of API keys, one sender per key")
	flag.IntVar(&runOptions.targetRate, "rate", 50000, "total records per second")
	flag.DurationVar(&runOptions.duration, "duration", 2*time.Minute, "how long to send")
	flag.IntVar(&runOptions.batchSize, "batch", 5000, "records per request")
	flag.IntVar(&runOptions.bodyBytes, "body-bytes", 0, "response body size in bytes added to every record, 0 sends no bodies")
	flag.Parse()

	if runOptions.adminToken == "" {
		fmt.Fprintln(os.Stderr, "ADMIN_TOKEN is required")
		return 2
	}
	if runOptions.keyCount <= 0 || runOptions.targetRate <= 0 || runOptions.batchSize <= 0 || runOptions.duration <= 0 {
		fmt.Fprintln(os.Stderr, "-keys, -rate, -batch and -duration must be positive")
		return 2
	}
	if runOptions.bodyBytes < 0 {
		fmt.Fprintln(os.Stderr, "-body-bytes must not be negative")
		return 2
	}

	signalContext, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	runContext, cancelRun := context.WithTimeout(signalContext, runOptions.duration)
	defer cancelRun()

	client := &http.Client{Timeout: requestTimeout}
	var createdKeyIDs []string
	defer func() {
		revokeContext, cancelRevoke := context.WithTimeout(context.Background(), revokeTimeout)
		defer cancelRevoke()
		for _, keyID := range createdKeyIDs {
			if err := revokeKey(revokeContext, client, runOptions, keyID); err != nil {
				fmt.Fprintf(os.Stderr, "revoking key %s failed: %v\n", keyID, err)
				exitCode = 1
			}
		}
	}()

	var senderKeys []string
	for keyNumber := 0; keyNumber < runOptions.keyCount; keyNumber++ {
		key, err := createKey(runContext, client, runOptions, keyNumber)
		if err != nil {
			fmt.Fprintf(os.Stderr, "creating key failed: %v\n", err)
			return 1
		}
		createdKeyIDs = append(createdKeyIDs, key.KeyID)
		senderKeys = append(senderKeys, key.Key)
	}

	batchBody := buildBatchBody(runOptions.batchSize, runOptions.bodyBytes)
	batchInterval := time.Duration(float64(time.Second) * float64(runOptions.batchSize) * float64(runOptions.keyCount) / float64(runOptions.targetRate))
	startedAt := time.Now()
	var senders sync.WaitGroup
	for _, senderKey := range senderKeys {
		senders.Add(1)
		go func(apiKey string) {
			defer senders.Done()
			sendBatches(runContext, client, runOptions, apiKey, batchBody, batchInterval)
		}(senderKey)
	}
	senders.Wait()
	elapsedSeconds := time.Since(startedAt).Seconds()
	interrupted := signalContext.Err() != nil

	finalReport := report{
		TargetRate:      runOptions.targetRate,
		AchievedRate:    float64(runOptions.accepted.Load()) / elapsedSeconds,
		DurationSeconds: elapsedSeconds,
		Accepted:        runOptions.accepted.Load(),
		Rejected:        runOptions.rejected.Load(),
		StatusCounts:    map[string]int{},
		TransportErrors: runOptions.transportFail.Load(),
		P95Milliseconds: p95Milliseconds(runOptions.latencies),
	}
	allStatusesOK := true
	runOptions.statusCounts.Range(func(status, count any) bool {
		finalReport.StatusCounts[fmt.Sprint(status)] = int(count.(*atomic.Int64).Load())
		if status != http.StatusOK {
			allStatusesOK = false
		}
		return true
	})
	encodedReport, _ := json.Marshal(finalReport)
	fmt.Println(string(encodedReport))

	passed := !interrupted && allStatusesOK &&
		finalReport.AchievedRate >= minimumAchievedFraction*float64(runOptions.targetRate) &&
		finalReport.TransportErrors == 0 && finalReport.Rejected == 0
	if !passed {
		return 1
	}
	return 0
}

func p95Milliseconds(latencies []time.Duration) float64 {
	if len(latencies) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), latencies...)
	sort.Slice(sorted, func(first, second int) bool { return sorted[first] < sorted[second] })
	rank := (len(sorted)*95 + 99) / 100
	return float64(sorted[rank-1]) / float64(time.Millisecond)
}

func buildBatchBody(batchSize int, bodyBytes int) []byte {
	responseBody := strings.Repeat("x", bodyBytes)
	records := make([]map[string]any, 0, batchSize)
	for recordNumber := 0; recordNumber < batchSize; recordNumber++ {
		record := map[string]any{
			"method":     "GET",
			"url":        fmt.Sprintf("https://example.com/loadtest/%d", recordNumber),
			"status":     200,
			"latency_ms": 100 + recordNumber%200,
			"scraper":    "loadtest",
		}
		if bodyBytes > 0 {
			record["body"] = responseBody
		}
		records = append(records, record)
	}
	encodedRecords, _ := json.Marshal(records)
	return encodedRecords
}

func sendBatches(runContext context.Context, client *http.Client, runOptions *options, apiKey string, batchBody []byte, batchInterval time.Duration) {
	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-runContext.Done():
			return
		case <-ticker.C:
			sendBatch(client, runOptions, apiKey, batchBody)
		}
	}
}

func sendBatch(client *http.Client, runOptions *options, apiKey string, batchBody []byte) {
	request, err := http.NewRequest(http.MethodPost, runOptions.baseURL+"/v1/requests", bytes.NewReader(batchBody))
	if err != nil {
		runOptions.transportFail.Add(1)
		return
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	sentAt := time.Now()
	response, err := client.Do(request)
	if err != nil {
		runOptions.transportFail.Add(1)
		return
	}
	defer response.Body.Close()
	runOptions.latencyMutex.Lock()
	runOptions.latencies = append(runOptions.latencies, time.Since(sentAt))
	runOptions.latencyMutex.Unlock()
	counter, _ := runOptions.statusCounts.LoadOrStore(response.StatusCode, &atomic.Int64{})
	counter.(*atomic.Int64).Add(1)
	if response.StatusCode != http.StatusOK {
		return
	}
	var summary batchResponse
	if err := json.NewDecoder(response.Body).Decode(&summary); err != nil {
		runOptions.transportFail.Add(1)
		return
	}
	runOptions.accepted.Add(int64(summary.Accepted))
	runOptions.rejected.Add(int64(summary.Rejected))
}

func createKey(requestContext context.Context, client *http.Client, runOptions *options, keyNumber int) (createdKey, error) {
	requestBody, _ := json.Marshal(map[string]string{
		"project": "loadtest",
		"name":    fmt.Sprintf("loadtest-%d-%d", keyNumber, time.Now().Unix()),
	})
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, runOptions.baseURL+"/v1/admin/keys", bytes.NewReader(requestBody))
	if err != nil {
		return createdKey{}, err
	}
	request.Header.Set("Authorization", "Bearer "+runOptions.adminToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return createdKey{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return createdKey{}, fmt.Errorf("unexpected status %d", response.StatusCode)
	}
	var key createdKey
	if err := json.NewDecoder(response.Body).Decode(&key); err != nil {
		return createdKey{}, err
	}
	return key, nil
}

func revokeKey(revokeContext context.Context, client *http.Client, runOptions *options, keyID string) error {
	request, err := http.NewRequestWithContext(revokeContext, http.MethodDelete, runOptions.baseURL+"/v1/admin/keys/"+keyID, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+runOptions.adminToken)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return errors.New("unexpected status " + response.Status)
	}
	return nil
}
