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
		MaxBodyBytes:       2097152,
		RedactedHeaders:    map[string]bool{"authorization": true, "cookie": true, "proxy-authorization": true},

		RateLimitRecordsPerMinute: 600000,

		ClickHouseDiskBudgetBytes: 25000000000,

		S3Region:     "auto",
		S3BodyPrefix: "bodies/",
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
		MaxBodyBytes:       2097152,
		RedactedHeaders:    map[string]bool{"authorization": true, "cookie": true, "proxy-authorization": true},

		RateLimitRecordsPerMinute: 600000,

		ClickHouseDiskBudgetBytes: 25000000000,

		S3Region:     "auto",
		S3BodyPrefix: "bodies/",
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

	for _, variableName := range []string{"MAX_BATCH_RECORDS", "MAX_REQUEST_BYTES", "MAX_BODY_BYTES"} {
		for _, value := range []string{"abc", "0", "-1"} {
			t.Setenv("MAX_BATCH_RECORDS", "")
			t.Setenv("MAX_REQUEST_BYTES", "")
			t.Setenv("MAX_BODY_BYTES", "")
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

func TestLoadMaxBodyBytes(t *testing.T) {
	t.Setenv("CLICKHOUSE_INGEST_PASSWORD", "secret")
	t.Setenv("ADMIN_TOKEN", "admin-secret")

	t.Setenv("MAX_BODY_BYTES", "")
	loadedConfig, err := Load()
	if err != nil || loadedConfig.MaxBodyBytes != 2097152 {
		t.Fatalf("default: got %d, %v", loadedConfig.MaxBodyBytes, err)
	}

	t.Setenv("MAX_BODY_BYTES", "4096")
	loadedConfig, err = Load()
	if err != nil || loadedConfig.MaxBodyBytes != 4096 {
		t.Fatalf("override: got %d, %v", loadedConfig.MaxBodyBytes, err)
	}
}

func TestLoadS3Settings(t *testing.T) {
	t.Setenv("CLICKHOUSE_INGEST_PASSWORD", "secret")
	t.Setenv("ADMIN_TOKEN", "admin-secret")
	t.Setenv("S3_ENDPOINT", "http://seaweed:8333")
	t.Setenv("S3_BUCKET", "scrapelog")
	t.Setenv("S3_ACCESS_KEY_ID", "key-id")
	t.Setenv("S3_SECRET_ACCESS_KEY", "key-secret")
	t.Setenv("S3_REGION", "eu-west-1")
	t.Setenv("S3_BODY_PREFIX", "payloads/")

	loadedConfig, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loadedConfig.S3Endpoint != "http://seaweed:8333" || loadedConfig.S3Bucket != "scrapelog" ||
		loadedConfig.S3AccessKeyID != "key-id" || loadedConfig.S3SecretAccessKey != "key-secret" ||
		loadedConfig.S3Region != "eu-west-1" || loadedConfig.S3BodyPrefix != "payloads/" {
		t.Fatalf("unexpected S3 settings: %+v", loadedConfig)
	}
}

func TestLoadRejectsIncompleteS3Settings(t *testing.T) {
	tests := map[string]struct {
		bucket, accessKeyID, secretAccessKey, message string
	}{
		"missing bucket":     {"", "key-id", "key-secret", "S3_BUCKET is required when S3_ENDPOINT is set"},
		"missing key id":     {"scrapelog", "", "key-secret", "S3_ACCESS_KEY_ID is required when S3_ENDPOINT is set"},
		"missing secret key": {"scrapelog", "key-id", "", "S3_SECRET_ACCESS_KEY is required when S3_ENDPOINT is set"},
	}
	for name, test := range tests {
		t.Setenv("CLICKHOUSE_INGEST_PASSWORD", "secret")
		t.Setenv("ADMIN_TOKEN", "admin-secret")
		t.Setenv("S3_ENDPOINT", "http://seaweed:8333")
		t.Setenv("S3_BUCKET", test.bucket)
		t.Setenv("S3_ACCESS_KEY_ID", test.accessKeyID)
		t.Setenv("S3_SECRET_ACCESS_KEY", test.secretAccessKey)
		_, err := Load()
		if err == nil || err.Error() != test.message {
			t.Errorf("%s: got %v, want %q", name, err, test.message)
		}
	}
}
