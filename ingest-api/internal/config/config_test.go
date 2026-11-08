package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestLoadAppliesDefaultsWhenOnlyPasswordIsSet(t *testing.T) {
	t.Setenv("CLICKHOUSE_URL", "")
	t.Setenv("CLICKHOUSE_DATABASE", "")
	t.Setenv("CLICKHOUSE_INGEST_USER", "")
	t.Setenv("CLICKHOUSE_INGEST_PASSWORD", "secret")
	t.Setenv("ADMIN_TOKEN", "admin-secret")

	loadedConfig, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedConfig := Config{
		ClickHouseURL:      "http://clickhouse:8123",
		ClickHouseDatabase: "scrapelog",
		ClickHouseUser:     "ingest_writer",
		ClickHousePassword: "secret",
		AdminToken:         "admin-secret",
		MaxBatchRecords:    5000,
		MaxRequestBytes:    10485760,
		RedactedHeaders:    map[string]bool{"authorization": true, "cookie": true, "proxy-authorization": true},

		RateLimitRecordsPerMinute: 600000,

		ClickHouseDiskBudgetBytes: 25000000000,
	}
	if !reflect.DeepEqual(loadedConfig, expectedConfig) {
		t.Fatalf("got %+v, want %+v", loadedConfig, expectedConfig)
	}
}

func TestLoadUsesProvidedValues(t *testing.T) {
	t.Setenv("CLICKHOUSE_URL", "http://example:9000")
	t.Setenv("CLICKHOUSE_DATABASE", "other")
	t.Setenv("CLICKHOUSE_INGEST_USER", "writer")
	t.Setenv("CLICKHOUSE_INGEST_PASSWORD", "secret")
	t.Setenv("ADMIN_TOKEN", "admin-secret")

	loadedConfig, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedConfig := Config{
		ClickHouseURL:      "http://example:9000",
		ClickHouseDatabase: "other",
		ClickHouseUser:     "writer",
		ClickHousePassword: "secret",
		AdminToken:         "admin-secret",
		MaxBatchRecords:    5000,
		MaxRequestBytes:    10485760,
		RedactedHeaders:    map[string]bool{"authorization": true, "cookie": true, "proxy-authorization": true},

		RateLimitRecordsPerMinute: 600000,

		ClickHouseDiskBudgetBytes: 25000000000,
	}
	if !reflect.DeepEqual(loadedConfig, expectedConfig) {
		t.Fatalf("got %+v, want %+v", loadedConfig, expectedConfig)
	}
}

func TestLoadRejectsEmptyPassword(t *testing.T) {
	t.Setenv("CLICKHOUSE_INGEST_PASSWORD", "")
	t.Setenv("ADMIN_TOKEN", "admin-secret")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "CLICKHOUSE_INGEST_PASSWORD") {
		t.Fatalf("expected password error, got %v", err)
	}
}

func TestLoadRejectsEmptyAdminToken(t *testing.T) {
	t.Setenv("CLICKHOUSE_INGEST_PASSWORD", "secret")
	t.Setenv("ADMIN_TOKEN", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "ADMIN_TOKEN") {
		t.Fatalf("expected admin token error, got %v", err)
	}
}

func TestLoadRejectsInvalidLimits(t *testing.T) {
	t.Setenv("CLICKHOUSE_INGEST_PASSWORD", "secret")
	t.Setenv("ADMIN_TOKEN", "admin-secret")

	for _, variableName := range []string{"MAX_BATCH_RECORDS", "MAX_REQUEST_BYTES"} {
		for _, value := range []string{"abc", "0", "-1"} {
			t.Setenv("MAX_BATCH_RECORDS", "")
			t.Setenv("MAX_REQUEST_BYTES", "")
			t.Setenv(variableName, value)

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), variableName) {
				t.Errorf("%s=%s: expected error, got %v", variableName, value, err)
			}
		}
	}
}

func TestLoadParsesRedactedHeaders(t *testing.T) {
	t.Setenv("CLICKHOUSE_INGEST_PASSWORD", "secret")
	t.Setenv("ADMIN_TOKEN", "admin-secret")
	t.Setenv("REDACT_HEADERS", " X-Api-Key , ,Cookie")

	loadedConfig, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := map[string]bool{"x-api-key": true, "cookie": true}
	if !reflect.DeepEqual(loadedConfig.RedactedHeaders, expected) {
		t.Fatalf("got %v, want %v", loadedConfig.RedactedHeaders, expected)
	}
}

func TestLoadRateLimitRecordsPerMinute(t *testing.T) {
	t.Setenv("CLICKHOUSE_INGEST_PASSWORD", "secret")
	t.Setenv("ADMIN_TOKEN", "admin-secret")
	t.Setenv("MAX_BATCH_RECORDS", "")
	t.Setenv("MAX_REQUEST_BYTES", "")

	t.Setenv("RATE_LIMIT_RECORDS_PER_MINUTE", "12000")
	loadedConfig, err := Load()
	if err != nil || loadedConfig.RateLimitRecordsPerMinute != 12000 {
		t.Fatalf("override: got %d, %v", loadedConfig.RateLimitRecordsPerMinute, err)
	}

	t.Setenv("RATE_LIMIT_RECORDS_PER_MINUTE", "5000")
	if _, err := Load(); err != nil {
		t.Fatalf("value equal to MAX_BATCH_RECORDS should load: %v", err)
	}

	for _, value := range []string{"4999", "0", "-1", "abc"} {
		t.Setenv("RATE_LIMIT_RECORDS_PER_MINUTE", value)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "RATE_LIMIT_RECORDS_PER_MINUTE") {
			t.Errorf("value %q: expected error, got %v", value, err)
		}
	}
}

func TestLoadClickHouseDiskBudgetBytes(t *testing.T) {
	t.Setenv("CLICKHOUSE_INGEST_PASSWORD", "secret")
	t.Setenv("ADMIN_TOKEN", "admin-secret")

	t.Setenv("CLICKHOUSE_DISK_BUDGET_BYTES", "")
	loadedConfig, err := Load()
	if err != nil || loadedConfig.ClickHouseDiskBudgetBytes != 25000000000 {
		t.Fatalf("default: got %d, %v", loadedConfig.ClickHouseDiskBudgetBytes, err)
	}

	t.Setenv("CLICKHOUSE_DISK_BUDGET_BYTES", "5000000000")
	loadedConfig, err = Load()
	if err != nil || loadedConfig.ClickHouseDiskBudgetBytes != 5000000000 {
		t.Fatalf("override: got %d, %v", loadedConfig.ClickHouseDiskBudgetBytes, err)
	}

	for _, value := range []string{"0", "-1", "abc"} {
		t.Setenv("CLICKHOUSE_DISK_BUDGET_BYTES", value)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CLICKHOUSE_DISK_BUDGET_BYTES") {
			t.Errorf("value %q: expected error, got %v", value, err)
		}
	}
}
