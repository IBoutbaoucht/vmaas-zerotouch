// Package ipalloc is a tiny IP pool allocator backed by bbolt.
//
// On disk: one key per *allocated* IP in the "ipalloc" bucket. The value is
// the VM ID that holds that IP. Free IPs are simply absent from the bucket.
//
// The pool range comes from config (PoolStart..PoolEnd inclusive, /24 logic
// — we increment the last octet by 1 each step). The allocator never returns
// an address outside the range and never double-allocates.
//
// Concurrency: every Acquire/Release runs inside a bbolt Update transaction,
// which is single-writer by design — so the read-test-write is atomic.
package ipalloc

import (
	"errors"
	"fmt"
	"net/netip"

	bolt "go.etcd.io/bbolt"

	"github.com/cuneyt/vmaas-engine/internal/config"
	"github.com/cuneyt/vmaas-engine/internal/store"
)

// ErrPoolExhausted is returned by Acquire when every IP in the pool is in use.
var ErrPoolExhausted = errors.New("ip pool exhausted")

// ErrIPNotInPool is returned by Release when the given IP is outside the pool.
var ErrIPNotInPool = errors.New("ip outside pool range")

// Allocator hands out and returns IPs from a configured pool.
type Allocator struct {
	db    *bolt.DB
	start netip.Addr
	end   netip.Addr
}

// New builds an Allocator from the network config.
func New(s *store.Store, nc *config.NetworkConfig) (*Allocator, error) {
	start, err := netip.ParseAddr(nc.PoolStart)
	if err != nil {
		return nil, fmt.Errorf("pool_start: %w", err)
	}
	end, err := netip.ParseAddr(nc.PoolEnd)
	if err != nil {
		return nil, fmt.Errorf("pool_end: %w", err)
	}
	if start.Compare(end) > 0 {
		return nil, errors.New("pool_start must be <= pool_end")
	}
	return &Allocator{db: s.DB(), start: start, end: end}, nil
}

// Acquire returns the first free IP, marking it owned by vmID.
func (a *Allocator) Acquire(vmID string) (netip.Addr, error) {
	var got netip.Addr
	err := a.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(store.IPAllocBucket))
		for ip := a.start; ip.Compare(a.end) <= 0; ip = ip.Next() {
			if b.Get([]byte(ip.String())) == nil {
				if err := b.Put([]byte(ip.String()), []byte(vmID)); err != nil {
					return err
				}
				got = ip
				return nil
			}
		}
		return ErrPoolExhausted
	})
	return got, err
}

// Release returns an IP to the pool. No-op if not allocated.
func (a *Allocator) Release(ip netip.Addr) error {
	if ip.Compare(a.start) < 0 || ip.Compare(a.end) > 0 {
		return ErrIPNotInPool
	}
	return a.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(store.IPAllocBucket)).Delete([]byte(ip.String()))
	})
}

// Used returns the list of IPs currently allocated (with owning vmID).
func (a *Allocator) Used() (map[string]string, error) {
	out := map[string]string{}
	err := a.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(store.IPAllocBucket)).ForEach(func(k, v []byte) error {
			out[string(k)] = string(v)
			return nil
		})
	})
	return out, err
}

// Free returns the list of currently-free IPs in the pool.
func (a *Allocator) Free() ([]string, error) {
	used, err := a.Used()
	if err != nil {
		return nil, err
	}
	var out []string
	for ip := a.start; ip.Compare(a.end) <= 0; ip = ip.Next() {
		if _, taken := used[ip.String()]; !taken {
			out = append(out, ip.String())
		}
	}
	return out, nil
}

// Total returns the total pool size.
func (a *Allocator) Total() int {
	n := 0
	for ip := a.start; ip.Compare(a.end) <= 0; ip = ip.Next() {
		n++
	}
	return n
}
