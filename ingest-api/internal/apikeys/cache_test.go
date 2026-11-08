package apikeys

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeLister struct {
	responses [][]Record
	errors    []error
	calls     int
}

func (lister *fakeLister) List(ctx context.Context) ([]Record, error) {
	index := lister.calls
	lister.calls++
	if index < len(lister.errors) && lister.errors[index] != nil {
		return nil, lister.errors[index]
	}
	return lister.responses[index], nil
}

func newTestKey(t *testing.T, project string) (Record, string) {
	t.Helper()
	keyID, fullKey, err := Generate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return Record{KeyID: keyID, Hash: Hash(fullKey), Project: project}, fullKey
}

func refreshedCache(t *testing.T, lister *fakeLister) *Cache {
	t.Helper()
	cache := NewCache(lister)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return cache
}

func TestAuthenticateAcceptsListedKey(t *testing.T) {
	record, fullKey := newTestKey(t, "acme")
	cache := refreshedCache(t, &fakeLister{responses: [][]Record{{record}}})

	identity, ok := cache.Authenticate(fullKey)
	if !ok || identity.KeyID != record.KeyID || identity.Project != "acme" {
		t.Fatalf("got %+v, %v", identity, ok)
	}
}

func TestAuthenticateRejectsInvalidTokens(t *testing.T) {
	record, fullKey := newTestKey(t, "acme")
	cache := refreshedCache(t, &fakeLister{responses: [][]Record{{record}}})

	wrongSecret := record.KeyID + "_" + strings.Repeat("z", 43)
	unknownKeyID := "sl_" + strings.Repeat("y", 12) + fullKey[len(record.KeyID):]
	for name, token := range map[string]string{
		"wrong secret": wrongSecret,
		"unknown id":   unknownKeyID,
		"malformed":    "not-a-key",
		"empty":        "",
	} {
		if _, ok := cache.Authenticate(token); ok {
			t.Errorf("%s: token was accepted", name)
		}
	}
}

func TestAuthenticateRejectsKeyAfterRefreshSeesRevocation(t *testing.T) {
	record, fullKey := newTestKey(t, "acme")
	revokedRecord := record
	revokedRecord.Revoked = true
	lister := &fakeLister{responses: [][]Record{{record}, {revokedRecord}}}
	cache := refreshedCache(t, lister)

	if _, ok := cache.Authenticate(fullKey); !ok {
		t.Fatal("key should authenticate before revocation")
	}
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := cache.Authenticate(fullKey); ok {
		t.Fatal("revoked key still authenticates")
	}
}

func TestRefreshErrorKeepsPreviousKeys(t *testing.T) {
	record, fullKey := newTestKey(t, "acme")
	lister := &fakeLister{
		responses: [][]Record{{record}, nil},
		errors:    []error{nil, errors.New("clickhouse down")},
	}
	cache := refreshedCache(t, lister)

	if err := cache.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	if _, ok := cache.Authenticate(fullKey); !ok {
		t.Fatal("previous key was dropped after a failed refresh")
	}
}

func TestRefreshRejectsInvalidStoredHashAndKeepsPreviousKeys(t *testing.T) {
	record, fullKey := newTestKey(t, "acme")
	corruptRecord := record
	corruptRecord.Hash = "abcd"
	cache := refreshedCache(t, &fakeLister{responses: [][]Record{{record}, {corruptRecord}}})

	if err := cache.Refresh(context.Background()); err == nil {
		t.Fatal("expected error for invalid hash")
	}
	if _, ok := cache.Authenticate(fullKey); !ok {
		t.Fatal("previous key was dropped")
	}
}
