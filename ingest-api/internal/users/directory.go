package users

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"uuid"
)

const AdminUsername = "admin"

const (
	ViewRequests     = "view_requests"
	ViewBandwidth    = "view_bandwidth"
	ViewAlerts       = "view_alerts"
	ManageAlerts     = "manage_alerts"
	ViewArchives     = "view_archives"
	EstimateRestores = "estimate_restores"
	ManageRestores   = "manage_restores"
)

var AllPermissions = []string{ViewRequests, ViewBandwidth, ViewAlerts, ManageAlerts, ViewArchives, EstimateRestores, ManageRestores}

const minimumPasswordLength = 12

var (
	ErrUnavailable       = errors.New("user accounts unavailable: apply clickhouse/initdb/005_users.sql and the scrapelog.users grant, then restart ingest-api")
	ErrUserNotFound      = errors.New("user not found")
	ErrWrongCredentials  = errors.New("wrong username or password")
	ErrUsernameRequired  = errors.New("username is required")
	ErrUsernameReserved  = errors.New("username is reserved")
	ErrUsernameTaken     = errors.New("username is already taken")
	ErrPasswordTooShort  = errors.New("password must be at least 12 characters")
	ErrUnknownPermission = errors.New("unknown permission")
)

type rowStore interface {
	List(ctx context.Context) ([]Record, error)
	Insert(ctx context.Context, record Record) error
}

type Change struct {
	Permissions []string
	Password    string
	Disabled    bool
}

type directorySnapshot struct {
	usersByID         map[string]Record
	userIDsByUsername map[string]string
}

type Directory struct {
	rows       rowStore
	writeMutex sync.Mutex
	snapshot   atomic.Pointer[directorySnapshot]
}

func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func NewDirectory(rows rowStore) *Directory {
	return &Directory{rows: rows}
}

func (directory *Directory) Load(ctx context.Context) error {
	records, err := directory.rows.List(ctx)
	if err != nil {
		return err
	}
	snapshot := &directorySnapshot{
		usersByID:         make(map[string]Record, len(records)),
		userIDsByUsername: make(map[string]string, len(records)),
	}
	for _, record := range records {
		snapshot.usersByID[record.UserID] = record
		snapshot.userIDsByUsername[record.Username] = record.UserID
	}
	directory.snapshot.Store(snapshot)
	return nil
}

func (directory *Directory) Lookup(userID string) (Record, error) {
	snapshot := directory.snapshot.Load()
	if snapshot == nil {
		return Record{}, ErrUnavailable
	}
	record, found := snapshot.usersByID[userID]
	if !found {
		return Record{}, ErrUserNotFound
	}
	return record, nil
}

func (directory *Directory) VerifyLogin(username string, password string) (Record, error) {
	snapshot := directory.snapshot.Load()
	if snapshot == nil {
		return Record{}, ErrUnavailable
	}
	var record Record
	userID, found := snapshot.userIDsByUsername[NormalizeUsername(username)]
	if found {
		record = snapshot.usersByID[userID]
	}
	storedHash := dummyPasswordHash
	if found {
		storedHash = record.PasswordHash
	}
	matches := passwordMatches(storedHash, password)
	if !found || !matches || record.Disabled {
		return Record{}, ErrWrongCredentials
	}
	return record, nil
}

func (directory *Directory) Create(ctx context.Context, username string, password string, permissions []string) (Record, error) {
	normalizedUsername := NormalizeUsername(username)
	if normalizedUsername == "" {
		return Record{}, ErrUsernameRequired
	}
	if normalizedUsername == AdminUsername {
		return Record{}, ErrUsernameReserved
	}
	if len(password) < minimumPasswordLength {
		return Record{}, ErrPasswordTooShort
	}
	canonical, err := canonicalPermissions(permissions)
	if err != nil {
		return Record{}, err
	}
	passwordHash, err := HashPassword(password)
	if err != nil {
		return Record{}, err
	}

	directory.writeMutex.Lock()
	defer directory.writeMutex.Unlock()
	snapshot := directory.snapshot.Load()
	if snapshot == nil {
		return Record{}, ErrUnavailable
	}
	if _, taken := snapshot.userIDsByUsername[normalizedUsername]; taken {
		return Record{}, ErrUsernameTaken
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	record := Record{
		UserID:       uuid.NewV7().String(),
		Username:     normalizedUsername,
		PasswordHash: passwordHash,
		Permissions:  canonical,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := directory.rows.Insert(ctx, record); err != nil {
		return Record{}, err
	}
	directory.publish(snapshot, record)
	return record, nil
}

func (directory *Directory) Update(ctx context.Context, userID string, change Change) (Record, error) {
	canonical, err := canonicalPermissions(change.Permissions)
	if err != nil {
		return Record{}, err
	}
	if change.Password != "" && len(change.Password) < minimumPasswordLength {
		return Record{}, ErrPasswordTooShort
	}
	newPasswordHash := ""
	if change.Password != "" {
		newPasswordHash, err = HashPassword(change.Password)
		if err != nil {
			return Record{}, err
		}
	}

	directory.writeMutex.Lock()
	defer directory.writeMutex.Unlock()
	snapshot := directory.snapshot.Load()
	if snapshot == nil {
		return Record{}, ErrUnavailable
	}
	current, found := snapshot.usersByID[userID]
	if !found {
		return Record{}, ErrUserNotFound
	}
	next := current
	next.Permissions = canonical
	next.Disabled = change.Disabled
	passwordChanged := newPasswordHash != ""
	if passwordChanged {
		next.PasswordHash = newPasswordHash
	}
	if passwordChanged || (change.Disabled && !current.Disabled) {
		next.SessionVersion = current.SessionVersion + 1
	}
	next.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
	earliestWinningTime := current.UpdatedAt.Add(time.Millisecond)
	if next.UpdatedAt.Before(earliestWinningTime) {
		next.UpdatedAt = earliestWinningTime
	}
	if err := directory.rows.Insert(ctx, next); err != nil {
		return Record{}, err
	}
	directory.publish(snapshot, next)
	return next, nil
}

func (directory *Directory) publish(previous *directorySnapshot, record Record) {
	next := &directorySnapshot{
		usersByID:         make(map[string]Record, len(previous.usersByID)+1),
		userIDsByUsername: make(map[string]string, len(previous.userIDsByUsername)+1),
	}
	for userID, existing := range previous.usersByID {
		next.usersByID[userID] = existing
	}
	for username, userID := range previous.userIDsByUsername {
		next.userIDsByUsername[username] = userID
	}
	next.usersByID[record.UserID] = record
	next.userIDsByUsername[record.Username] = record.UserID
	directory.snapshot.Store(next)
}

func canonicalPermissions(requested []string) ([]string, error) {
	for _, permission := range requested {
		if !slices.Contains(AllPermissions, permission) {
			return nil, ErrUnknownPermission
		}
	}
	canonical := []string{}
	for _, permission := range AllPermissions {
		if slices.Contains(requested, permission) {
			canonical = append(canonical, permission)
		}
	}
	return canonical, nil
}
