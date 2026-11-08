package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const recordCount = 1000

func main() {
	baseURL := os.Getenv("SCRAPELOG_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	apiKey := os.Getenv("SCRAPELOG_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "SCRAPELOG_KEY is required")
		os.Exit(1)
	}

	records := make([]map[string]any, 0, recordCount)
	for pageNumber := 0; pageNumber < recordCount; pageNumber++ {
		records = append(records, map[string]any{
			"method":     "GET",
			"url":        fmt.Sprintf("https://example.com/page/%d", pageNumber),
			"status":     200,
			"latency_ms": 120 + pageNumber%50,
			"scraper":    "example-go",
			"request_headers": map[string]string{
				"User-Agent":    "example-scraper/1.0",
				"Authorization": "Bearer example-token-that-will-be-redacted",
			},
		})
	}
	encodedRecords, err := json.Marshal(records)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var compressedBody bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressedBody)
	gzipWriter.Write(encodedRecords)
	gzipWriter.Close()

	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/requests", &compressedBody)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")

	client := http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(response.Body)
	if response.StatusCode < 200 || response.StatusCode > 299 {
		fmt.Fprintln(os.Stderr, string(responseBody))
		os.Exit(1)
	}
	fmt.Println(string(responseBody))
}
