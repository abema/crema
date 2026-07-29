//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

//nolint:gocyclo // One state machine intentionally interprets the complete operation byte stream.
func FuzzProviderStateMachine(f *testing.F) {
	f.Add([]byte{0, 0, 5, 'a', 1, 0, 0, 0, 2, 0, 0, 0})
	f.Add([]byte{0, 1, 255, 'x', 5, 1, 0, 0, 1, 1, 0, 0})
	f.Add([]byte{0, 0, 151, 0xa5, 0, 1, 151, 0x5a, 5, 0, 2, 0})
	f.Add([]byte(
		"70007B\xf2QW\xc9\xf97uxz\xf8\"\xf6a\xb4霏DL\x9bf\xc5\x01&2\xc7\xceF" +
			"\xbf\xc0\x04\x02\bbz^u\xc9\xf1x\xf5\x90 >\xac\xbc\x910\xa3\xfa\xd9" +
			"\xc0\xf9\x15y\xf3?\x98A\xa1\xa2}\xb4\xc5I]Bķ\x13\x99\xa2g\xbd\xba" +
			"W\x7f\xc4\xe5\x890(0\x00\x80\x000",
	))

	f.Fuzz(func(t *testing.T, operations []byte) {
		if len(operations) > 1024 {
			operations = operations[:1024]
		}
		provider, err := NewProvider(Config{
			CapacityBytes: unix.Getpagesize() * 16,
			ShardCount:    4,
		})
		if err != nil {
			t.Fatal(err)
		}
		closed := false
		defer func() {
			if !closed {
				_ = provider.Close()
			}
		}()

		expected := make(map[string][]byte)
		for offset := 0; offset+3 < len(operations); offset += 4 {
			op := operations[offset] % 8
			key := string([]byte{'k', '0' + operations[offset+1]%8})
			valueLength := fuzzValueLength(operations[offset+2])
			value := bytes.Repeat([]byte{operations[offset+3]}, valueLength)

			switch op {
			case 0:
				err := provider.Set(context.Background(), key, value, 0)
				if err == nil {
					expected[key] = bytes.Clone(value)
				} else if !errors.Is(err, ErrCapacity) {
					t.Fatalf("Set(%q): %v", key, err)
				}
			case 1:
				got, ok, err := provider.Get(context.Background(), key)
				if err != nil {
					t.Fatalf("Get(%q): %v", key, err)
				}
				if ok {
					want, exists := expected[key]
					if !exists || !bytes.Equal(got, want) {
						t.Fatalf("Get(%q) = %x, want %x (exists=%v)", key, got, want, exists)
					}
				} else {
					delete(expected, key)
				}
			case 2:
				if err := provider.Delete(context.Background(), key); err != nil {
					t.Fatalf("Delete(%q): %v", key, err)
				}
				delete(expected, key)
			case 3:
				if err := provider.Purge(); err != nil {
					t.Fatalf("Purge(): %v", err)
				}
				clear(expected)
			case 4:
				target := int64(operations[offset+2]%4+1) * int64(provider.pageSize)
				if _, err := provider.Trim(target); err != nil {
					t.Fatalf("Trim(%d): %v", target, err)
				}
			case 5:
				item := provider.lookup(key)
				if item != nil {
					pageOffset := uint32(operations[offset+2]) % item.pageCount
					forced, affectsTarget, err := forceReclaimForFuzz(provider, item, pageOffset)
					if err != nil {
						t.Fatalf("forced MADV_DONTNEED: %v", err)
					}
					if forced {
						got, ok, err := provider.Get(context.Background(), key)
						if err != nil {
							t.Fatalf("Get(%q) after reclaim: %v", key, err)
						}
						if affectsTarget {
							if ok {
								t.Fatalf("Get(%q) after payload reclaim = (_, true, nil), want miss", key)
							}
							delete(expected, key)
						} else {
							want, exists := expected[key]
							if !ok || !exists || !bytes.Equal(got, want) {
								t.Fatalf(
									"Get(%q) after unrelated-page reclaim = (%x, %v), want %x",
									key,
									got,
									ok,
									want,
								)
							}
						}
					}
				}
			case 6:
				if err := provider.Set(context.Background(), key, value, time.Nanosecond); err == nil {
					expected[key] = bytes.Clone(value)
					time.Sleep(time.Nanosecond)
					provider.expireReady()
					delete(expected, key)
				} else if !errors.Is(err, ErrCapacity) {
					t.Fatalf("Set(%q) with TTL: %v", key, err)
				}
			case 7:
				// Stats invariants are checked after every operation.
			}
			assertProviderStats(t, provider)
		}

		if err := provider.Purge(); err != nil {
			t.Fatal(err)
		}
		stats := provider.Stats()
		if stats.Entries != 0 || stats.LogicalBytes != 0 ||
			stats.ReservedBytes != 0 || stats.FreeBytes != int64(provider.capacity) {
			t.Fatalf("stats after Purge = %+v; state=%s", stats, debugProviderState(provider))
		}
		if err := provider.Close(); err != nil {
			t.Fatal(err)
		}
		closed = true
		select {
		case <-provider.expiryDone:
		default:
			t.Fatal("expiration worker still running after Close")
		}
		if provider.arena != nil {
			t.Fatal("arena retained after Close")
		}
	})
}

func forceReclaimForFuzz(
	provider *Provider,
	item *cacheEntry,
	pageOffset uint32,
) (forced bool, affectsTarget bool, err error) {
	item.mu.Lock()
	defer item.mu.Unlock()
	if item.state != entryLive || item.refs != 0 {
		return false, false, nil
	}

	if item.kind == allocationSmall {
		meta := item.smallMeta
		meta.mu.Lock()
		defer meta.mu.Unlock()
		slot := int(item.slot)
		if meta.state != smallPageLive ||
			meta.generation != item.generation ||
			meta.refs != 0 ||
			slot >= len(meta.entries) ||
			meta.entries[slot] != item {
			return false, false, nil
		}
		layout := provider.layouts[item.classID]
		payloadPages, valid := layout.slotPayloadPageMask(slot, item.length)
		if !valid {
			return false, false, errors.New("invalid small payload page mask")
		}
		affectsTarget = payloadPages&(uint32(1)<<pageOffset) != 0
	} else {
		affectsTarget = true
	}

	if err := simulateReclaim(provider.page(item.startPage + pageOffset)); err != nil {
		return false, false, err
	}

	return true, affectsTarget, nil
}

func fuzzValueLength(encoded byte) int {
	if encoded < 128 {
		return int(encoded % 64)
	}

	return int(encoded-127) << 8
}

func debugProviderState(provider *Provider) string {
	var result strings.Builder
	provider.smallMu.RLock()
	for pageID := range provider.smallPages {
		meta := provider.smallPages[pageID]
		meta.mu.Lock()
		if meta.state != smallPageDead || meta.used != 0 || meta.refs != 0 || len(meta.entries) != 0 {
			fmt.Fprintf(
				&result,
				" page=%d state=%d gen=%d class=%d refs=%d used=%d slots=%d",
				pageID,
				meta.state,
				meta.generation,
				meta.classID,
				meta.refs,
				meta.used,
				len(meta.entries),
			)
		}
		meta.mu.Unlock()
	}
	provider.smallMu.RUnlock()
	provider.allocatorMu.Lock()
	fmt.Fprintf(&result, " free=%#v", provider.allocator.free)
	provider.allocatorMu.Unlock()

	return result.String()
}

func assertProviderStats(t *testing.T, provider *Provider) {
	t.Helper()
	stats := provider.Stats()
	if stats.Entries < 0 || stats.LogicalBytes < 0 || stats.ReservedBytes < 0 || stats.FreeBytes < 0 {
		t.Fatalf("negative stats: %+v", stats)
	}
	if stats.ReservedBytes+stats.FreeBytes != int64(provider.capacity) {
		t.Fatalf("arena accounting mismatch: %+v, capacity=%d", stats, provider.capacity)
	}
	if stats.LogicalBytes > stats.ReservedBytes {
		t.Fatalf(
			"logical bytes exceed reserved bytes: %+v; state=%s",
			stats,
			debugProviderState(provider),
		)
	}
}
