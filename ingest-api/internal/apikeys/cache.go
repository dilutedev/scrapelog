package apikeys

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

const (
	refreshInterval = 30 * time.Second
	refreshTimeout  = 10 * time.Second
)

type Identity struct {
	KeyID   string
	Project string
}

type keyLister interface {
	List(ctx context.Context) ([]Record, error)
}

type cachedKey struct {
	hash    [32]byte
	project string
}

type Cache struct {
	lister keyLister
	keys   atomic.Pointer[map[string]cachedKey]
}

func NewCache(lister keyLister) *Cache {
	cache := &Cache{lister: lister}
	emptyKeys := map[string]cachedKey{}
	cache.keys.Store(&emptyKeys)
	return cache
}

func (cache *Cache) Refresh(ctx context.Context) error {
	records, err := cache.lister.List(ctx)
	if err != nil {
		return err
	}
	activeKeys := make(map[string]cachedKey, len(records))
	for _, record := range records {
		if record.Revoked {
			continue
		}
		decodedHash, err := hex.DecodeString(record.Hash)
		if err != nil || len(decodedHash) != sha256.Size {
			return fmt.Errorf("api key %s has an invalid hash", record.KeyID)
		}
		entry := cachedKey{project: record.Project}
		copy(entry.hash[:], decodedHash)
		activeKeys[record.KeyID] = entry
	}
	cache.keys.Store(&activeKeys)
	return nil
}

func (cache *Cache) RunRefreshLoop(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshContext, cancel := context.WithTimeout(ctx, refreshTimeout)
			if err := cache.Refresh(refreshContext); err != nil {
				logger.Warn("refreshing api keys failed", "error", err)
			}
			cancel()
		}
	}
}

func (cache *Cache) Authenticate(fullKey string) (Identity, bool) {
	keyID, ok := ParseKeyID(fullKey)
	if !ok {
		return Identity{}, false
	}
	entry, found := (*cache.keys.Load())[keyID]
	if !found {
		return Identity{}, false
	}
	presentedHash := sha256.Sum256([]byte(fullKey))
	if subtle.ConstantTimeCompare(presentedHash[:], entry.hash[:]) != 1 {
		return Identity{}, false
	}
	return Identity{KeyID: keyID, Project: entry.project}, true
}
