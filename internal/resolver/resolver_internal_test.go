package resolver

import (
	"fmt"
	"net"
	"testing"
	"time"
)

// TestStoreLockedEvictsExpiredWhenFull verifies the cache cap drops expired
// entries first when the map overflows, keeping it bounded.
func TestStoreLockedEvictsExpiredWhenFull(t *testing.T) {
	r := &Resolver{cache: make(map[string]entry)}
	now := time.Now()
	for i := 0; i < maxCacheEntries; i++ {
		r.cache[fmt.Sprintf("expired-%d", i)] = entry{expires: now.Add(-time.Minute)}
	}

	r.storeLocked("fresh.example.com", entry{ips: []net.IP{net.ParseIP("1.2.3.4")}, expires: now.Add(time.Minute)}, now)

	if len(r.cache) >= maxCacheEntries {
		t.Fatalf("expired entries were not evicted: size %d", len(r.cache))
	}
	if _, ok := r.cache["fresh.example.com"]; !ok {
		t.Fatalf("new entry was not stored")
	}
}

// TestStoreLockedEvictsSoonestWhenAllLive verifies that when the cache is full
// and every entry is still live, the entry closest to expiring is dropped first.
func TestStoreLockedEvictsSoonestWhenAllLive(t *testing.T) {
	r := &Resolver{cache: make(map[string]entry)}
	now := time.Now()
	r.cache["soonest"] = entry{expires: now.Add(time.Minute)}
	for i := 0; i < maxCacheEntries-1; i++ {
		r.cache[fmt.Sprintf("live-%d", i)] = entry{expires: now.Add(time.Hour)}
	}

	r.storeLocked("new.example.com", entry{expires: now.Add(time.Hour)}, now)

	if len(r.cache) > maxCacheEntries {
		t.Fatalf("cache exceeded cap: %d", len(r.cache))
	}
	if _, ok := r.cache["soonest"]; ok {
		t.Fatal("soonest-to-expire entry should have been evicted first")
	}
	if _, ok := r.cache["new.example.com"]; !ok {
		t.Fatal("new entry was not stored")
	}
}

// TestStoreLockedBoundsSizeWhenAllLive verifies that even when every entry is
// still live, the cache never grows past the cap.
func TestStoreLockedBoundsSizeWhenAllLive(t *testing.T) {
	r := &Resolver{cache: make(map[string]entry)}
	now := time.Now()
	for i := 0; i < maxCacheEntries; i++ {
		r.cache[fmt.Sprintf("live-%d", i)] = entry{expires: now.Add(time.Hour)}
	}

	r.storeLocked("another.example.com", entry{expires: now.Add(time.Hour)}, now)

	if len(r.cache) > maxCacheEntries {
		t.Fatalf("cache exceeded cap with all-live entries: %d > %d", len(r.cache), maxCacheEntries)
	}
}
