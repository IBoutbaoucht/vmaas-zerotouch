package ipalloc

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/cuneyt/vmaas-engine/internal/config"
	"github.com/cuneyt/vmaas-engine/internal/store"
)

func newTestAlloc(t *testing.T) (*Allocator, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, err := New(s, &config.NetworkConfig{
		PoolStart: "192.0.2.62",
		PoolEnd:   "192.0.2.69",
		Prefix:    24,
		Gateway:   "192.0.2.254",
		DNS:       []string{"1.1.1.1"},
		NICName:   "ens34",
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, s
}

func TestAcquireReleaseRoundTrip(t *testing.T) {
	a, _ := newTestAlloc(t)
	ip, err := a.Acquire("vm-1")
	if err != nil {
		t.Fatal(err)
	}
	if ip.String() != "192.0.2.62" {
		t.Fatalf("want .62, got %s", ip)
	}
	free, _ := a.Free()
	if len(free) != 7 {
		t.Fatalf("want 7 free, got %d", len(free))
	}
	if err := a.Release(ip); err != nil {
		t.Fatal(err)
	}
	free, _ = a.Free()
	if len(free) != 8 {
		t.Fatalf("after release want 8 free, got %d", len(free))
	}
}

func TestExhaustion(t *testing.T) {
	a, _ := newTestAlloc(t)
	for i := 0; i < 8; i++ {
		if _, err := a.Acquire("vm"); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
	}
	if _, err := a.Acquire("vm"); err != ErrPoolExhausted {
		t.Fatalf("want exhausted, got %v", err)
	}
}

func TestConcurrentAcquireAreUnique(t *testing.T) {
	a, _ := newTestAlloc(t)
	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	got := make(chan string, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ip, err := a.Acquire("vm")
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			got <- ip.String()
		}()
	}
	wg.Wait()
	close(got)
	seen := map[string]bool{}
	for ip := range got {
		if seen[ip] {
			t.Fatalf("dup ip %s", ip)
		}
		seen[ip] = true
	}
	if len(seen) != n {
		t.Fatalf("want %d unique, got %d", n, len(seen))
	}
}
