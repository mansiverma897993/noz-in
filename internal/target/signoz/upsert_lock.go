package signoz

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

type upsertLock struct {
	token chan struct{}
	refs  int
}

var upsertLockRegistry = struct {
	sync.Mutex
	entries map[string]*upsertLock
}{entries: make(map[string]*upsertLock)}

// acquireUpsertLocks serializes read-before-write reconciliation within one
// process, including across separately constructed clients. The target API
// does not expose an atomic upsert operation, so every stable object identity
// and user-visible name participating in a write is locked in sorted order.
func acquireUpsertLocks(ctx context.Context, keys ...string) (func(), error) {
	keys = uniqueSortedStrings(keys)
	if len(keys) == 0 {
		return func() {}, nil
	}

	locks := make([]*upsertLock, len(keys))
	upsertLockRegistry.Lock()
	for index, key := range keys {
		entry := upsertLockRegistry.entries[key]
		if entry == nil {
			entry = &upsertLock{token: make(chan struct{}, 1)}
			entry.token <- struct{}{}
			upsertLockRegistry.entries[key] = entry
		}
		entry.refs++
		locks[index] = entry
	}
	upsertLockRegistry.Unlock()

	acquired := 0
	for acquired < len(locks) {
		select {
		case <-ctx.Done():
			releaseUpsertLocks(keys, locks, acquired)
			return nil, fmt.Errorf("wait for concurrent SigNoz write: %w", ctx.Err())
		case <-locks[acquired].token:
			acquired++
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() { releaseUpsertLocks(keys, locks, len(locks)) })
	}, nil
}

func releaseUpsertLocks(keys []string, locks []*upsertLock, acquired int) {
	for index := acquired - 1; index >= 0; index-- {
		locks[index].token <- struct{}{}
	}
	upsertLockRegistry.Lock()
	defer upsertLockRegistry.Unlock()
	for index, key := range keys {
		locks[index].refs--
		if locks[index].refs == 0 {
			delete(upsertLockRegistry.entries, key)
		}
	}
}

func uniqueSortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	write := 0
	for _, value := range result {
		if write > 0 && result[write-1] == value {
			continue
		}
		result[write] = value
		write++
	}
	return result[:write]
}

func (client *Client) upsertLockKey(kind, identity string) string {
	return client.baseURL.String() + "\x00" + kind + "\x00" + identity
}
