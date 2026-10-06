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

func TestMergeEditParams_ChildrenAlwaysCarriedForward(t *testing.T) {
	// There is no --child flag -- Children must survive an edit that
	// never mentions it, same as every other untouched field. The relay
	// rejects an edit that omits an existing child, so forgetting this
	// would make every unrelated edit on a parent group fail outright.
	current := &nip29.GroupMetadata{Children: []string{"notes", "retro"}}

	params := mergeEditParams(current, "pubkey", "group1", editFlags{name: "Renamed", nameSet: true})

	if len(params.Children) != 2 || params.Children[0] != "notes" || params.Children[1] != "retro" {
		t.Fatalf("params.Children = %v, want unchanged [notes retro]", params.Children)
	}
}

func TestMergeEditParams_ParentSetOverridesCurrent(t *testing.T) {
	current := &nip29.GroupMetadata{Parent: "old-parent"}

	params := mergeEditParams(current, "pubkey", "group1", editFlags{parent: "new-parent", parentSet: true})

	if params.Parent != "new-parent" {
		t.Fatalf("params.Parent = %q, want %q", params.Parent, "new-parent")
	}
}

func TestMergeEditParams_ParentUnsetKeepsCurrent(t *testing.T) {
	current := &nip29.GroupMetadata{Parent: "standup"}

	params := mergeEditParams(current, "pubkey", "group1", editFlags{})

	if params.Parent != "standup" {
		t.Fatalf("params.Parent = %q, want unchanged %q", params.Parent, "standup")
	}
}

func TestMergeEditParams_ExplicitEmptyParentDetaches(t *testing.T) {
	current := &nip29.GroupMetadata{Parent: "standup"}

	// cmd.Flags().Changed("parent") is what disambiguates "not passed"
	// from "passed as an empty string" -- editFlags.parentSet models
	// that bit directly, independent of editFlags.parent's own value.
	params := mergeEditParams(current, "pubkey", "group1", editFlags{parent: "", parentSet: true})

	if params.Parent != "" {
		t.Fatalf("params.Parent = %q, want empty (detached to root)", params.Parent)
	}
}
