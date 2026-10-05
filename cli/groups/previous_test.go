package groups

import (
	"testing"

	"github.com/ohstr/nmilat/nip01"
)

func TestRecentEventIDs_Empty(t *testing.T) {
	if got := recentEventIDs(nil, 3); got != nil {
		t.Fatalf("recentEventIDs(nil) = %v, want nil", got)
	}
}

func TestRecentEventIDs_NewestFirst(t *testing.T) {
	events := []*nip01.Event{
		{ID: "oldest", CreatedAt: 100},
		{ID: "newest", CreatedAt: 300},
		{ID: "middle", CreatedAt: 200},
	}

	got := recentEventIDs(events, 3)
	want := []string{"newest", "middle", "oldest"}
	if len(got) != len(want) {
		t.Fatalf("recentEventIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recentEventIDs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRecentEventIDs_TruncatesToMax(t *testing.T) {
	events := []*nip01.Event{
		{ID: "a", CreatedAt: 400},
		{ID: "b", CreatedAt: 300},
		{ID: "c", CreatedAt: 200},
		{ID: "d", CreatedAt: 100},
	}

	got := recentEventIDs(events, 2)
	want := []string{"a", "b"}
	if len(got) != len(want) {
		t.Fatalf("recentEventIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recentEventIDs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
