package signer

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// UsedFileName is the used-attestation log inside --state-dir.
const UsedFileName = "used-attestations.ndjson"

// usedRetention is how long a used id is kept. Anything older is past
// MaxAttestationAge plus any sane clock skew, so it can't validate again.
const usedRetention = MaxAttestationAge + time.Hour

// UsedEntry is one consumed attestation.
type UsedEntry struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
}

// UsedSet records consumed attestation ids, optionally persisted as
// append-only NDJSON so a restart can't replay them.
type UsedSet struct {
	mu  sync.Mutex
	ids map[string]int64
	f   *os.File // nil: memory only
}

// NewMemoryUsedSet keeps ids in memory only.
func NewMemoryUsedSet() *UsedSet {
	return &UsedSet{ids: map[string]int64{}}
}

// OpenUsedSet loads dir/used-attestations.ndjson, drops entries older than
// the retention window, rewrites it compacted and opens it for append.
func OpenUsedSet(dir string, now time.Time) (*UsedSet, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, UsedFileName)
	u := NewMemoryUsedSet()

	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var e UsedEntry
			if json.Unmarshal(sc.Bytes(), &e) != nil || e.ID == "" {
				continue // a torn last line from a crash
			}
			u.ids[e.ID] = e.CreatedAt
		}
		_ = f.Close()
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	cutoff := now.Add(-usedRetention).Unix()
	for id, at := range u.ids {
		if at < cutoff {
			delete(u.ids, id)
		}
	}

	tmp := path + ".tmp"
	tf, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	enc := json.NewEncoder(tf)
	for id, at := range u.ids {
		if err := enc.Encode(UsedEntry{ID: id, CreatedAt: at}); err != nil {
			_ = tf.Close()
			return nil, err
		}
	}
	if err := tf.Sync(); err != nil {
		_ = tf.Close()
		return nil, err
	}
	if err := tf.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}

	u.f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// Persistent reports whether ids survive a restart.
func (u *UsedSet) Persistent() bool {
	return u.f != nil
}

// Has reports whether id was consumed.
func (u *UsedSet) Has(id string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	_, ok := u.ids[id]
	return ok
}

// Add records entries, fsyncing before it returns. On error nothing is
// recorded and the caller must not sign.
func (u *UsedSet) Add(entries []UsedEntry) error {
	if len(entries) == 0 {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.f != nil {
		var buf []byte
		for _, e := range entries {
			line, err := json.Marshal(e)
			if err != nil {
				return err
			}
			buf = append(append(buf, line...), '\n')
		}
		if _, err := u.f.Write(buf); err != nil {
			return err
		}
		if err := u.f.Sync(); err != nil {
			return err
		}
	}
	for _, e := range entries {
		u.ids[e.ID] = e.CreatedAt
	}
	return nil
}

// Close closes the backing file.
func (u *UsedSet) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.f == nil {
		return nil
	}
	err := u.f.Close()
	u.f = nil
	return err
}
