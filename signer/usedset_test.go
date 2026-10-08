package signer

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestUsedSetPersists(t *testing.T) {
	dir := t.TempDir()
	u, err := OpenUsedSet(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Add([]UsedEntry{{ID: "a", CreatedAt: t0.Unix()}, {ID: "old", CreatedAt: t0.Add(-20 * time.Hour).Unix()}}); err != nil {
		t.Fatal(err)
	}
	if !u.Has("a") || u.Has("b") {
		t.Fatal("Has after Add")
	}
	_ = u.Close()

	u, err = OpenUsedSet(dir, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !u.Has("a") || !u.Has("old") {
		t.Fatal("ids lost across reopen")
	}
	_ = u.Close()

	// Past the retention window entries are pruned, and the file compacted.
	u, err = OpenUsedSet(dir, t0.Add(10*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if u.Has("old") || !u.Has("a") {
		t.Fatal("prune")
	}
	_ = u.Close()
	data, _ := os.ReadFile(filepath.Join(dir, UsedFileName))
	if len(data) == 0 || string(data) != "{\"id\":\"a\",\"created_at\":"+strconv.FormatInt(t0.Unix(), 10)+"}\n" {
		t.Fatalf("compacted file = %q", data)
	}
}

func TestUsedSetTornLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, UsedFileName)
	if err := os.WriteFile(path, []byte("{\"id\":\"a\",\"created_at\":"+strconv.FormatInt(t0.Unix(), 10)+"}\n{\"id\":\"b"), 0o600); err != nil {
		t.Fatal(err)
	}
	u, err := OpenUsedSet(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = u.Close() }()
	if !u.Has("a") || u.Has("b") {
		t.Fatal("torn line handling")
	}
}

func TestUsedSetWriteError(t *testing.T) {
	dir := t.TempDir()
	u, err := OpenUsedSet(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	// Swap the append handle for a read-only one so the write fails.
	_ = u.f.Close()
	if u.f, err = os.Open(filepath.Join(dir, UsedFileName)); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = u.Close() }()
	if err := u.Add([]UsedEntry{{ID: "a", CreatedAt: t0.Unix()}}); err == nil {
		t.Fatal("Add succeeded on a read-only file")
	}
	if u.Has("a") {
		t.Fatal("a failed Add must not record the id")
	}
}
