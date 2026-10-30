package fakeip

import (
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/profile/cachefile"

	"github.com/metacubex/bbolt"
)

func TestPoolCloneFromSelfPreservesState(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		name := "memory"
		if persistent {
			name = "persistent"
		}
		t.Run(name, func(t *testing.T) {
			pool, err := New(Options{IPNet: netip.MustParsePrefix("192.0.2.0/29"), Size: 10})
			if err != nil {
				t.Fatal(err)
			}
			if persistent {
				db, err := bbolt.Open(filepath.Join(t.TempDir(), "cache.db"), 0600, &bbolt.Options{Timeout: time.Second, NoStatistics: true})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				pool.store = newCachefileStore(&cachefile.CacheFile{DB: db}, pool.ipnet)
			}
			pool.Lookup("first.example")
			pool.Lookup("second.example")
			done := make(chan struct{})
			go func() {
				pool.CloneFrom(pool)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("CloneFrom(self) deadlocked")
			}
			if got := pool.Lookup("first.example"); got != netip.MustParseAddr("192.0.2.4") {
				t.Fatalf("existing mapping changed: %s", got)
			}
			if host, ok := pool.LookBack(netip.MustParseAddr("192.0.2.5")); !ok || host != "second.example" {
				t.Fatalf("reverse mapping changed: %q, %t", host, ok)
			}
			if got := pool.Lookup("third.example"); got != netip.MustParseAddr("192.0.2.6") {
				t.Fatalf("allocation state changed: next address %s", got)
			}
		})
	}
}
