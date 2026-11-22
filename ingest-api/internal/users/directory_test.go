package users

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

type fakeRowStore struct {
	mutex              sync.Mutex
	listed             []Record
	listError          error
	insertError        error
	inserted           []Record
	firstInsertGate    chan struct{}
	firstInsertStarted chan struct{}
	gateUsed           bool
}

func (store *fakeRowStore) List(ctx context.Context) ([]Record, error) {
	return store.listed, store.listError
}

func (store *fakeRowStore) Insert(ctx context.Context, record Record) error {
	store.mutex.Lock()
	holdAtGate := store.firstInsertGate != nil && !store.gateUsed
	store.gateUsed = true
	store.mutex.Unlock()
	if holdAtGate {
		close(store.firstInsertStarted)
		<-store.firstInsertGate
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.insertError != nil {
		return store.insertError
	}
	store.inserted = append(store.inserted, record)
	return nil
}

func loadedDirectory(test *testing.T, records ...Record) (*Directory, *fakeRowStore) {
	test.Helper()
	rows := &fakeRowStore{listed: records}
	directory := NewDirectory(rows)
	if err := directory.Load(context.Background()); err != nil {
		test.Fatalf("Load: %v", err)
	}
	return directory, rows
}

func hashedRecord(test *testing.T, userID string, username string, password string) Record {
	test.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		test.Fatalf("HashPassword: %v", err)
	}
	return Record{UserID: userID, Username: username, PasswordHash: hash, Permissions: []string{ViewRequests}, SessionVersion: 3, UpdatedAt: time.Now().UTC().Truncate(time.Millisecond)}
}

func TestLoadFailureLeavesDirectoryUnavailable(test *testing.T) {
	rows := &fakeRowStore{listError: errors.New("clickhouse down")}
	directory := NewDirectory(rows)
	if err := directory.Load(context.Background()); err == nil {
		test.Fatal("Load returned no error")
	}
	if _, err := directory.Lookup("any"); !errors.Is(err, ErrUnavailable) {
		test.Fatalf("Lookup error = %v", err)
	}
	if _, err := directory.VerifyLogin("any", "password value"); !errors.Is(err, ErrUnavailable) {
		test.Fatalf("VerifyLogin error = %v", err)
	}
	if _, err := directory.Create(context.Background(), "new", "long enough password", nil); !errors.Is(err, ErrUnavailable) {
		test.Fatalf("Create error = %v", err)
	}
	if _, err := directory.Update(context.Background(), "any", Change{}); !errors.Is(err, ErrUnavailable) {
		test.Fatalf("Update error = %v", err)
	}
	if len(rows.inserted) != 0 {
		test.Fatal("unavailable directory inserted rows")
	}
}

func TestLookup(test *testing.T) {
	directory, _ := loadedDirectory(test, hashedRecord(test, "id-1", "alice", "alice password 1"))
	record, err := directory.Lookup("id-1")
	if err != nil || record.Username != "alice" {
		test.Fatalf("Lookup = %+v, %v", record, err)
	}
	if _, err := directory.Lookup("missing"); !errors.Is(err, ErrUserNotFound) {
		test.Fatalf("Lookup missing error = %v", err)
	}
}

func TestVerifyLogin(test *testing.T) {
	disabled := hashedRecord(test, "id-2", "bob", "bob password 123")
	disabled.Disabled = true
	directory, _ := loadedDirectory(test, hashedRecord(test, "id-1", "alice", "alice password 1"), disabled)

	cases := map[string]struct {
		username  string
		password  string
		wantError error
	}{
		"success":          {"alice", "alice password 1", nil},
		"normalised":       {"  ALICE ", "alice password 1", nil},
		"unknown user":     {"nobody", "alice password 1", ErrWrongCredentials},
		"wrong password":   {"alice", "wrong password 12", ErrWrongCredentials},
		"disabled correct": {"bob", "bob password 123", ErrWrongCredentials},
	}
	for name, testCase := range cases {
		test.Run(name, func(test *testing.T) {
			record, err := directory.VerifyLogin(testCase.username, testCase.password)
			if !errors.Is(err, testCase.wantError) {
				test.Fatalf("error = %v, want %v", err, testCase.wantError)
			}
			if err == nil && record.UserID != "id-1" {
				test.Fatalf("user id = %q", record.UserID)
			}
		})
	}
}

func TestCreate(test *testing.T) {
	disabled := hashedRecord(test, "id-2", "bob", "bob password 123")
	disabled.Disabled = true
	cases := map[string]struct {
		username    string
		password    string
		permissions []string
		wantError   error
	}{
		"empty username":     {"  ", "long enough password", nil, ErrUsernameRequired},
		"reserved":           {"Admin", "long enough password", nil, ErrUsernameReserved},
		"duplicate":          {"ALICE", "long enough password", nil, ErrUsernameTaken},
		"duplicate disabled": {"bob", "long enough password", nil, ErrUsernameTaken},
		"short password":     {"carol", "short", nil, ErrPasswordTooShort},
		"manage users":       {"carol", "long enough password", []string{"manage_users"}, ErrUnknownPermission},
		"admin permission":   {"carol", "long enough password", []string{"admin"}, ErrUnknownPermission},
	}
	for name, testCase := range cases {
		test.Run(name, func(test *testing.T) {
			directory, rows := loadedDirectory(test, hashedRecord(test, "id-1", "alice", "alice password 1"), disabled)
			if _, err := directory.Create(context.Background(), testCase.username, testCase.password, testCase.permissions); !errors.Is(err, testCase.wantError) {
				test.Fatalf("error = %v, want %v", err, testCase.wantError)
			}
			if len(rows.inserted) != 0 {
				test.Fatal("rejected create inserted a row")
			}
		})
	}

	test.Run("success", func(test *testing.T) {
		directory, rows := loadedDirectory(test)
		created, err := directory.Create(context.Background(), " Carol ", "long enough password", []string{ManageRestores, ViewRequests, ViewRequests})
		if err != nil {
			test.Fatalf("Create: %v", err)
		}
		if created.Username != "carol" || created.SessionVersion != 0 || created.UserID == "" || created.UserID == AdminUsername {
			test.Fatalf("created = %+v", created)
		}
		if !slices.Equal(created.Permissions, []string{ViewRequests, ManageRestores}) {
			test.Fatalf("permissions = %v", created.Permissions)
		}
		if len(rows.inserted) != 1 || !recordsEqual(rows.inserted[0], created) {
			test.Fatalf("inserted = %+v", rows.inserted)
		}
		cached, err := directory.Lookup(created.UserID)
		if err != nil || !recordsEqual(cached, created) {
			test.Fatalf("cached = %+v, %v", cached, err)
		}
		if _, err := directory.VerifyLogin("carol", "long enough password"); err != nil {
			test.Fatalf("VerifyLogin: %v", err)
		}
	})
}

func recordsEqual(first Record, second Record) bool {
	return first.UserID == second.UserID && first.Username == second.Username && first.PasswordHash == second.PasswordHash &&
		slices.Equal(first.Permissions, second.Permissions) && first.Disabled == second.Disabled &&
		first.SessionVersion == second.SessionVersion && first.CreatedAt.Equal(second.CreatedAt) && first.UpdatedAt.Equal(second.UpdatedAt)
}

func TestUpdateSessionVersion(test *testing.T) {
	cases := map[string]struct {
		start       func(*Record)
		change      Change
		wantVersion uint32
		wantHashNew bool
	}{
		"permission edit keeps version": {nil, Change{Permissions: []string{ViewBandwidth}}, 3, false},
		"password reset bumps":          {nil, Change{Permissions: []string{ViewRequests}, Password: "another long password"}, 4, true},
		"disable bumps":                 {nil, Change{Permissions: []string{ViewRequests}, Disabled: true}, 4, false},
		"enable keeps version":          {func(record *Record) { record.Disabled = true }, Change{Permissions: []string{ViewRequests}}, 3, false},
		"reset and disable bump once":   {nil, Change{Permissions: []string{ViewRequests}, Password: "another long password", Disabled: true}, 4, true},
	}
	for name, testCase := range cases {
		test.Run(name, func(test *testing.T) {
			original := hashedRecord(test, "id-1", "alice", "alice password 1")
			if testCase.start != nil {
				testCase.start(&original)
			}
			directory, rows := loadedDirectory(test, original)
			updated, err := directory.Update(context.Background(), "id-1", testCase.change)
			if err != nil {
				test.Fatalf("Update: %v", err)
			}
			if updated.SessionVersion != testCase.wantVersion {
				test.Fatalf("version = %d, want %d", updated.SessionVersion, testCase.wantVersion)
			}
			if (updated.PasswordHash != original.PasswordHash) != testCase.wantHashNew {
				test.Fatalf("password hash changed = %v, want %v", updated.PasswordHash != original.PasswordHash, testCase.wantHashNew)
			}
			if len(rows.inserted) != 1 || !recordsEqual(rows.inserted[0], updated) {
				test.Fatalf("inserted = %+v", rows.inserted)
			}
		})
	}
}

func TestUpdateRejections(test *testing.T) {
	directory, rows := loadedDirectory(test, hashedRecord(test, "id-1", "alice", "alice password 1"))
	if _, err := directory.Update(context.Background(), "missing", Change{}); !errors.Is(err, ErrUserNotFound) {
		test.Fatalf("missing id error = %v", err)
	}
	if _, err := directory.Update(context.Background(), "id-1", Change{Permissions: []string{"manage_users"}}); !errors.Is(err, ErrUnknownPermission) {
		test.Fatalf("unknown permission error = %v", err)
	}
	if _, err := directory.Update(context.Background(), "id-1", Change{Password: "short"}); !errors.Is(err, ErrPasswordTooShort) {
		test.Fatalf("short password error = %v", err)
	}
	if len(rows.inserted) != 0 {
		test.Fatal("rejected update inserted a row")
	}
}

func TestUpdateInsertErrorLeavesCacheUnchanged(test *testing.T) {
	original := hashedRecord(test, "id-1", "alice", "alice password 1")
	directory, rows := loadedDirectory(test, original)
	rows.insertError = errors.New("insert failed")
	if _, err := directory.Update(context.Background(), "id-1", Change{Permissions: []string{ViewAlerts}, Disabled: true}); err == nil {
		test.Fatal("Update returned no error")
	}
	cached, _ := directory.Lookup("id-1")
	if !recordsEqual(cached, original) {
		test.Fatalf("cache changed to %+v", cached)
	}
}

func TestUpdatedAtStrictlyIncreases(test *testing.T) {
	original := hashedRecord(test, "id-1", "alice", "alice password 1")
	original.UpdatedAt = time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	directory, _ := loadedDirectory(test, original)
	updated, err := directory.Update(context.Background(), "id-1", Change{Permissions: []string{ViewRequests}})
	if err != nil {
		test.Fatalf("Update: %v", err)
	}
	if !updated.UpdatedAt.After(original.UpdatedAt) {
		test.Fatalf("UpdatedAt %v not after %v", updated.UpdatedAt, original.UpdatedAt)
	}
}

func TestUpdateSerialisesConcurrentWrites(test *testing.T) {
	original := hashedRecord(test, "id-1", "alice", "alice password 1")
	directory, rows := loadedDirectory(test, original)
	rows.firstInsertGate = make(chan struct{})
	rows.firstInsertStarted = make(chan struct{})

	var waitGroup sync.WaitGroup
	errorsSeen := make(chan error, 2)
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		_, err := directory.Update(context.Background(), "id-1", Change{Permissions: []string{ViewRequests}, Password: "reset password long"})
		errorsSeen <- err
	}()
	<-rows.firstInsertStarted
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		_, err := directory.Update(context.Background(), "id-1", Change{Permissions: []string{ViewBandwidth}})
		errorsSeen <- err
	}()
	time.Sleep(50 * time.Millisecond)
	close(rows.firstInsertGate)
	waitGroup.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			test.Fatalf("Update: %v", err)
		}
	}

	cached, _ := directory.Lookup("id-1")
	if cached.SessionVersion != original.SessionVersion+1 {
		test.Fatalf("version = %d, want %d", cached.SessionVersion, original.SessionVersion+1)
	}
	if len(rows.inserted) != 2 {
		test.Fatalf("inserted %d rows", len(rows.inserted))
	}
	if !recordsEqual(cached, rows.inserted[1]) {
		test.Fatalf("cache %+v differs from last insert %+v", cached, rows.inserted[1])
	}
	if !rows.inserted[1].UpdatedAt.After(rows.inserted[0].UpdatedAt) {
		test.Fatal("UpdatedAt did not strictly increase")
	}
	if rows.inserted[1].SessionVersion < rows.inserted[0].SessionVersion {
		test.Fatal("SessionVersion decreased")
	}
}
