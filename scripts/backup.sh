#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

backup_name="scrapelog-$(date -u +%Y%m%dT%H%M%SZ)"

docker compose exec -T clickhouse clickhouse-client --query "BACKUP DATABASE scrapelog TO S3(backup_s3, filename = 'backups/${backup_name}')" </dev/null

echo "backups/${backup_name}"
