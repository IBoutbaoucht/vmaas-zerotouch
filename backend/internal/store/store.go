// Package store is a thin wrapper around bbolt that owns the on-disk state
// for VMaaS: per-VM records, the IP-allocator map, and clone-step checkpoints.
//
// Every read/write goes through a bbolt transaction (one writer at a time,
// many readers). Buckets used:
//
//	vms      → vm-id  ->  JSON(VM)             (one record per provisioned VM)
//	ipalloc  → ip     ->  vm-id                (which VM holds which IP)
//	clones   → vm-id  ->  JSON(map[step]bool)  (idempotency for the 5-step clone)
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Status is the lifecycle state of a VM record.
type Status string

const (
	StatusPending    Status = "pending"
	StatusAllocating Status = "allocating"
	StatusCloning    Status = "cloning"
	StatusInjecting  Status = "injecting"
	StatusStarting   Status = "starting"
	StatusReady      Status = "ready"
	StatusFailed     Status = "failed"
	StatusDeleting   Status = "deleting"
)

// VM is the per-VM record persisted in the "vms" bucket.
type VM struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Status      Status          `json:"status"`
	IP          string          `json:"ip,omitempty"`
	GoldVM      string          `json:"gold_vm,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Error       string          `json:"error,omitempty"`
	Checkpoints map[string]bool `json:"checkpoints,omitempty"`
}

// ErrNotFound is returned by Get when the VM ID is unknown.
var ErrNotFound = errors.New("vm not found")

// Bucket names.
const (
	bucketVMs     = "vms"
	bucketIPAlloc = "ipalloc"
	bucketClones  = "clones"
)

// Store holds the bbolt database handle.
type Store struct {
	db *bolt.DB
}

// Open opens (or creates) the bbolt file at path and ensures all buckets exist.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir store dir: %w", err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 3 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range []string{bucketVMs, bucketIPAlloc, bucketClones} {
			if _, err := tx.CreateBucketIfNotExists([]byte(b)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close flushes and closes the bbolt database.
func (s *Store) Close() error { return s.db.Close() }

// DB returns the underlying bbolt handle. Used by ipalloc which manages its
// own bucket directly to keep allocations atomic with the VM record write.
func (s *Store) DB() *bolt.DB { return s.db }

// Put writes (or overwrites) a VM record.
func (s *Store) Put(v VM) error {
	if v.ID == "" {
		return errors.New("vm id required")
	}
	v.UpdatedAt = time.Now().UTC()
	if v.CreatedAt.IsZero() {
		v.CreatedAt = v.UpdatedAt
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketVMs))
		buf, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return b.Put([]byte(v.ID), buf)
	})
}

// Get retrieves a VM record by ID.
func (s *Store) Get(id string) (VM, error) {
	var v VM
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketVMs))
		raw := b.Get([]byte(id))
		if raw == nil {
			return ErrNotFound
		}
		return json.Unmarshal(raw, &v)
	})
	return v, err
}

// Delete removes a VM record by ID. No error if it didn't exist.
func (s *Store) Delete(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucketVMs)).Delete([]byte(id))
	})
}

// List returns all VM records sorted by CreatedAt descending.
func (s *Store) List() ([]VM, error) {
	var out []VM
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketVMs))
		return b.ForEach(func(_, v []byte) error {
			var x VM
			if err := json.Unmarshal(v, &x); err != nil {
				return err
			}
			out = append(out, x)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// UpdateStatus is a convenience wrapper that loads, mutates, and saves.
// If a non-empty errString is passed and status == StatusFailed, it's set.
func (s *Store) UpdateStatus(id string, status Status, errString string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketVMs))
		raw := b.Get([]byte(id))
		if raw == nil {
			return ErrNotFound
		}
		var v VM
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		v.Status = status
		if errString != "" {
			v.Error = errString
		}
		v.UpdatedAt = time.Now().UTC()
		buf, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return b.Put([]byte(v.ID), buf)
	})
}

// SetIP atomically records the IP onto a VM record.
func (s *Store) SetIP(id, ip string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketVMs))
		raw := b.Get([]byte(id))
		if raw == nil {
			return ErrNotFound
		}
		var v VM
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		v.IP = ip
		v.UpdatedAt = time.Now().UTC()
		buf, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return b.Put([]byte(v.ID), buf)
	})
}

// CheckpointSet marks a clone step as completed for a given VM ID.
func (s *Store) CheckpointSet(vmID, step string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketClones))
		raw := b.Get([]byte(vmID))
		m := map[string]bool{}
		if raw != nil {
			_ = json.Unmarshal(raw, &m)
		}
		m[step] = true
		buf, _ := json.Marshal(m)
		return b.Put([]byte(vmID), buf)
	})
}

// CheckpointHas returns whether a clone step was completed for a VM ID.
func (s *Store) CheckpointHas(vmID, step string) (bool, error) {
	var ok bool
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket([]byte(bucketClones)).Get([]byte(vmID))
		if raw == nil {
			return nil
		}
		m := map[string]bool{}
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		ok = m[step]
		return nil
	})
	return ok, err
}

// CheckpointClear removes all checkpoints for a VM (used on cleanup).
func (s *Store) CheckpointClear(vmID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucketClones)).Delete([]byte(vmID))
	})
}

// IPAllocBucket is the bucket name for the IP allocator. Exposed so ipalloc
// can do its own atomic writes.
const IPAllocBucket = bucketIPAlloc
