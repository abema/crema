package gomemcache

import (
	"sync"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
)

type testMemcacheClient struct {
	mu        sync.Mutex
	items     map[string]testMemcacheItem
	getItem   *memcache.Item
	getErr    error
	deleteErr error
}

type testMemcacheItem struct {
	value     []byte
	expiresAt time.Time
}

func newTestMemcacheClient() *testMemcacheClient {
	return &testMemcacheClient{items: make(map[string]testMemcacheItem)}
}

func (t *testMemcacheClient) Get(key string) (*memcache.Item, error) {
	if t.getErr != nil {
		return nil, t.getErr
	}
	if t.getItem != nil || t.getErr != nil {
		return t.getItem, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	item, ok := t.items[key]
	if !ok {
		return nil, memcache.ErrCacheMiss
	}
	if !item.expiresAt.IsZero() && time.Now().After(item.expiresAt) {
		delete(t.items, key)

		return nil, memcache.ErrCacheMiss
	}

	return &memcache.Item{Key: key, Value: append([]byte(nil), item.value...)}, nil
}

func (t *testMemcacheClient) Set(item *memcache.Item) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	stored := testMemcacheItem{
		value: append([]byte(nil), item.Value...),
	}
	if item.Expiration > 0 {
		stored.expiresAt = time.Now().Add(time.Duration(item.Expiration) * time.Second)
	}
	t.items[item.Key] = stored

	return nil
}

func (t *testMemcacheClient) Delete(key string) error {
	if t.deleteErr != nil {
		return t.deleteErr
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	item, ok := t.items[key]
	if !ok {
		return memcache.ErrCacheMiss
	}
	if !item.expiresAt.IsZero() && time.Now().After(item.expiresAt) {
		delete(t.items, key)

		return memcache.ErrCacheMiss
	}
	delete(t.items, key)

	return nil
}
