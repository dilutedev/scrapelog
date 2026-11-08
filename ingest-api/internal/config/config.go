package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	ClickHouseURL      string
	ClickHouseDatabase string
	ClickHouseUser     string
	ClickHousePassword string
	AdminToken         string
	MaxBatchRecords    int
	MaxRequestBytes    int64
	MaxBodyBytes       int64
	RedactedHeaders    map[string]bool

	RateLimitRecordsPerMinute int64

	ClickHouseDiskBudgetBytes int64
}

func Load() (Config, error) {
	loadedConfig := Config{
		ClickHouseURL:      valueOrDefault("CLICKHOUSE_URL", "http://clickhouse:8123"),
		ClickHouseDatabase: valueOrDefault("CLICKHOUSE_DATABASE", "scrapelog"),
		ClickHouseUser:     valueOrDefault("CLICKHOUSE_INGEST_USER", "ingest_writer"),
		ClickHousePassword: os.Getenv("CLICKHOUSE_INGEST_PASSWORD"),
		AdminToken:         os.Getenv("ADMIN_TOKEN"),
	}
	maxBatchRecords, err := positiveIntegerOrDefault("MAX_BATCH_RECORDS", 5000)
	if err != nil {
		return Config{}, err
	}
	maxRequestBytes, err := positiveIntegerOrDefault("MAX_REQUEST_BYTES", 10485760)
	if err != nil {
		return Config{}, err
	}
	maxBodyBytes, err := positiveIntegerOrDefault("MAX_BODY_BYTES", 2097152)
	if err != nil {
		return Config{}, err
	}
	rateLimitRecordsPerMinute, err := positiveIntegerOrDefault("RATE_LIMIT_RECORDS_PER_MINUTE", 600000)
	if err != nil {
		return Config{}, err
	}
	if rateLimitRecordsPerMinute < maxBatchRecords {
		return Config{}, errors.New("RATE_LIMIT_RECORDS_PER_MINUTE must be at least MAX_BATCH_RECORDS")
	}
	diskBudgetBytes, err := positiveIntegerOrDefault("CLICKHOUSE_DISK_BUDGET_BYTES", 25000000000)
	if err != nil {
		return Config{}, err
	}
	loadedConfig.MaxBatchRecords = int(maxBatchRecords)
	loadedConfig.ClickHouseDiskBudgetBytes = diskBudgetBytes
	loadedConfig.RateLimitRecordsPerMinute = rateLimitRecordsPerMinute
	loadedConfig.MaxRequestBytes = maxRequestBytes
	loadedConfig.MaxBodyBytes = maxBodyBytes
	loadedConfig.RedactedHeaders = headerNameSet(valueOrDefault("REDACT_HEADERS", "authorization,cookie,proxy-authorization"))
	if loadedConfig.ClickHousePassword == "" {
		return Config{}, errors.New("CLICKHOUSE_INGEST_PASSWORD is required")
	}
	if loadedConfig.AdminToken == "" {
		return Config{}, errors.New("ADMIN_TOKEN is required")
	}
	return loadedConfig, nil
}

func valueOrDefault(variableName string, defaultValue string) string {
	value := os.Getenv(variableName)
	if value == "" {
		return defaultValue
	}
	return value
}

func positiveIntegerOrDefault(variableName string, defaultValue int64) (int64, error) {
	value := os.Getenv(variableName)
	if value == "" {
		return defaultValue, nil
	}
	parsedValue, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsedValue <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", variableName)
	}
	return parsedValue, nil
}

func headerNameSet(commaSeparated string) map[string]bool {
	names := map[string]bool{}
	for _, part := range strings.Split(commaSeparated, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name != "" {
			names[name] = true
		}
	}
	return names
}
