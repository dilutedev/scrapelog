package apikeys

import (
	"context"
	"errors"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

var ErrKeyNotFound = errors.New("api key not found")

type Record struct {
	KeyID     string
	Hash      string
	Project   string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
	Revoked   bool
}

type Store struct {
	connection driver.Conn
}

func NewStore(connection driver.Conn) *Store {
	return &Store{connection: connection}
}

const listKeysQuery = `SELECT key_id,
       argMax(key_hash, updated_at) AS current_hash,
       argMax(project, updated_at) AS current_project,
       argMax(name, updated_at) AS current_name,
       argMax(created_at, updated_at) AS current_created_at,
       max(updated_at) AS current_updated_at,
       argMax(revoked, updated_at) AS current_revoked
FROM api_keys
GROUP BY key_id
ORDER BY current_created_at`

func (store *Store) List(ctx context.Context) ([]Record, error) {
	rows, err := store.connection.Query(ctx, listKeysQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := []Record{}
	for rows.Next() {
		var record Record
		var revokedFlag uint8
		if err := rows.Scan(&record.KeyID, &record.Hash, &record.Project, &record.Name, &record.CreatedAt, &record.UpdatedAt, &revokedFlag); err != nil {
			return nil, err
		}
		record.Revoked = revokedFlag == 1
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func (store *Store) Create(ctx context.Context, project string, name string) (string, string, error) {
	keyID, fullKey, err := Generate()
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC()
	record := Record{
		KeyID:     keyID,
		Hash:      Hash(fullKey),
		Project:   project,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.insert(ctx, record); err != nil {
		return "", "", err
	}
	return keyID, fullKey, nil
}

func (store *Store) Revoke(ctx context.Context, keyID string) error {
	records, err := store.List(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.KeyID != keyID {
			continue
		}
		if record.Revoked {
			return nil
		}
		revokedAt := time.Now().UTC()
		earliestWinningTime := record.UpdatedAt.Add(time.Millisecond)
		if revokedAt.Before(earliestWinningTime) {
			revokedAt = earliestWinningTime
		}
		record.Revoked = true
		record.UpdatedAt = revokedAt
		return store.insert(ctx, record)
	}
	return ErrKeyNotFound
}

func (store *Store) insert(ctx context.Context, record Record) error {
	batch, err := store.connection.PrepareBatch(ctx, "INSERT INTO api_keys (key_id, key_hash, project, name, created_at, updated_at, revoked)")
	if err != nil {
		return err
	}
	var revokedFlag uint8
	if record.Revoked {
		revokedFlag = 1
	}
	if err := batch.Append(record.KeyID, record.Hash, record.Project, record.Name, record.CreatedAt, record.UpdatedAt, revokedFlag); err != nil {
		batch.Abort()
		return err
	}
	return batch.Send()
}
