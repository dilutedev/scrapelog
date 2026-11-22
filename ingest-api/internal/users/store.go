package users

import (
	"context"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type Record struct {
	UserID         string
	Username       string
	PasswordHash   string
	Permissions    []string
	Disabled       bool
	SessionVersion uint32
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Store struct {
	connection driver.Conn
}

func NewStore(connection driver.Conn) *Store {
	return &Store{connection: connection}
}

const listUsersQuery = `SELECT user_id,
       argMax(username, updated_at) AS current_username,
       argMax(password_hash, updated_at) AS current_password_hash,
       argMax(permissions, updated_at) AS current_permissions,
       argMax(disabled, updated_at) AS current_disabled,
       argMax(session_version, updated_at) AS current_session_version,
       argMax(created_at, updated_at) AS current_created_at,
       max(updated_at) AS current_updated_at
FROM users
GROUP BY user_id`

func (store *Store) List(ctx context.Context) ([]Record, error) {
	rows, err := store.connection.Query(ctx, listUsersQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := []Record{}
	for rows.Next() {
		var record Record
		var disabledFlag uint8
		if err := rows.Scan(&record.UserID, &record.Username, &record.PasswordHash, &record.Permissions, &disabledFlag, &record.SessionVersion, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, err
		}
		record.Disabled = disabledFlag == 1
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func (store *Store) Insert(ctx context.Context, record Record) error {
	batch, err := store.connection.PrepareBatch(ctx, "INSERT INTO users (user_id, username, password_hash, permissions, disabled, session_version, created_at, updated_at)")
	if err != nil {
		return err
	}
	var disabledFlag uint8
	if record.Disabled {
		disabledFlag = 1
	}
	if err := batch.Append(record.UserID, record.Username, record.PasswordHash, record.Permissions, disabledFlag, record.SessionVersion, record.CreatedAt, record.UpdatedAt); err != nil {
		batch.Abort()
		return err
	}
	return batch.Send()
}
