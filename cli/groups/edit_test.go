package groups

import (
	"testing"

	"github.com/ohstr/nmilat/nip29"
)

func TestMergeEditParams_UnchangedFlagsKeepCurrent(t *testing.T) {
	current := &nip29.GroupMetadata{
		Name: "Standup", About: "Daily sync", Private: true, Closed: true,
	}

	// Only --name was passed; every other field (including the two access
	// flags) must survive unchanged, not reset to the struct's zero value.
	params := mergeEditParams(current, "pubkey", "group1", editFlags{
		name: "New name", nameSet: true,
	})

	if params.Name != "New name" {
		t.Fatalf("params.Name = %q, want %q", params.Name, "New name")
	}
	if params.About != "Daily sync" {
		t.Fatalf("params.About = %q, want unchanged %q", params.About, "Daily sync")
	}
	if !params.Private {
		t.Fatal("params.Private = false, want unchanged true")
	}
	if !params.Closed {
		t.Fatal("params.Closed = false, want unchanged true")
	}
}

func TestMergeEditParams_NoFlagsChangedIsNoOp(t *testing.T) {
	current := &nip29.GroupMetadata{Name: "Standup", Private: true, Closed: true}

	params := mergeEditParams(current, "pubkey", "group1", editFlags{})

	if params.Name != current.Name || params.Private != current.Private || params.Closed != current.Closed {
		t.Fatalf("mergeEditParams() with no flags changed = %+v, want current preserved exactly", params)
	}
}

func TestMergeEditParams_PublicOverridesPrivate(t *testing.T) {
	current := &nip29.GroupMetadata{Private: true}

	params := mergeEditParams(current, "pubkey", "group1", editFlags{public: true})

	if params.Private {
		t.Fatal("params.Private = true after --public, want false")
	}
}

func TestMergeEditParams_OpenOverridesClosed(t *testing.T) {
	current := &nip29.GroupMetadata{Closed: true}

	params := mergeEditParams(current, "pubkey", "group1", editFlags{open: true})

	if params.Closed {
		t.Fatal("params.Closed = true after --open, want false")
	}
}

func TestMergeEditParams_IDAndPubkeyAlwaysSet(t *testing.T) {
	params := mergeEditParams(&nip29.GroupMetadata{}, "pubkey123", "group1", editFlags{})

	if params.SelfPubkey != "pubkey123" {
		t.Fatalf("params.SelfPubkey = %q, want %q", params.SelfPubkey, "pubkey123")
	}
	if params.ID != "group1" {
		t.Fatalf("params.ID = %q, want %q", params.ID, "group1")
	}
}
