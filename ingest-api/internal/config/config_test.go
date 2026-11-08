package config

import (
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
	}
	if loadedConfig != expectedConfig {
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
	}
	if loadedConfig != expectedConfig {
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
