package crema

import (
	"context"
	"sync"
	"time"
)

type testMemoryProvider[V any] struct {
	mu    sync.Mutex
	items map[string]CacheObject[V]
}

func (m *testMemoryProvider[V]) Get(_ context.Context, key string) (CacheObject[V], bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.items[key]

	return value, ok, nil
}

func (m *testMemoryProvider[V]) Set(_ context.Context, key string, value CacheObject[V], _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[key] = value

	return nil
}

func (m *testMemoryProvider[V]) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, key)

	return nil
}

type byteProvider struct {
	mu    sync.Mutex
	items map[string][]byte
}

func (b *byteProvider) Get(_ context.Context, key string) ([]byte, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	value, ok := b.items[key]

	return value, ok, nil
}

func (b *byteProvider) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items[key] = value

	return nil
}

func (b *byteProvider) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.items, key)

	return nil
}

type errorProvider[S any] struct {
	getErr    error
	setErr    error
	deleteErr error
}

func (p *errorProvider[S]) Get(_ context.Context, _ string) (S, bool, error) {
	var zero S
	if p.getErr != nil {
		return zero, false, p.getErr
	}

	return zero, false, nil
}

func (p *errorProvider[S]) Set(_ context.Context, _ string, _ S, _ time.Duration) error {
	if p.setErr != nil {
		return p.setErr
	}

	return nil
}

func (p *errorProvider[S]) Delete(_ context.Context, _ string) error {
	if p.deleteErr != nil {
		return p.deleteErr
	}

	return nil
}

func fakeRandom(value float64) func() float64 {
	return func() float64 {
		return value
	}
}
