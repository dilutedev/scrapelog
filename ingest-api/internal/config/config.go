package config

import (
	"errors"
	"os"
)

type Config struct {
	ClickHouseURL      string
	ClickHouseDatabase string
	ClickHouseUser     string
	ClickHousePassword string
}

func Load() (Config, error) {
	loadedConfig := Config{
		ClickHouseURL:      valueOrDefault("CLICKHOUSE_URL", "http://clickhouse:8123"),
		ClickHouseDatabase: valueOrDefault("CLICKHOUSE_DATABASE", "scrapelog"),
		ClickHouseUser:     valueOrDefault("CLICKHOUSE_INGEST_USER", "ingest_writer"),
		ClickHousePassword: os.Getenv("CLICKHOUSE_INGEST_PASSWORD"),
	}
	if loadedConfig.ClickHousePassword == "" {
		return Config{}, errors.New("CLICKHOUSE_INGEST_PASSWORD is required")
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
