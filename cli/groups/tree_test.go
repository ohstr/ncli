package groups

import (
	"encoding/json"
	"testing"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip29"
)

// metadataEvent builds an unsigned kind:39000 event -- buildTree only
// ever calls nip29.ParseGroupMetadata (structural parsing), never
// ValidateGroupMetadata, so no signature is needed here.
func metadataEvent(id string, p nip29.GroupMetadataParams) *nip01.Event {
	p.ID = id
	return nip29.NewGroupMetadata(p)
}

func TestBuildTree_RootsHaveNoParent(t *testing.T) {
	events := []*nip01.Event{
		metadataEvent("standup", nip29.GroupMetadataParams{Name: "Standup"}),
		metadataEvent("retro", nip29.GroupMetadataParams{Name: "Retro"}),
	}

	result := buildTree(events)

	if len(result.Roots) != 2 {
		t.Fatalf("Roots = %v, want both groups (neither has a parent)", result.Roots)
	}
	if len(result.Nodes) != 2 {
		t.Fatalf("len(Nodes) = %d, want 2", len(result.Nodes))
	}
}

func TestBuildTree_ChildAssembledFromParentTag(t *testing.T) {
	events := []*nip01.Event{
		metadataEvent("standup", nip29.GroupMetadataParams{Name: "Standup"}),
		metadataEvent("standup-notes", nip29.GroupMetadataParams{Name: "Notes", Parent: "standup"}),
	}

	result := buildTree(events)

	if len(result.Roots) != 1 || result.Roots[0] != "standup" {
		t.Fatalf("Roots = %v, want [standup]", result.Roots)
	}
	parent := result.Nodes["standup"]
	if len(parent.Children) != 1 || parent.Children[0] != "standup-notes" {
		t.Fatalf("standup's Children = %v, want [standup-notes]", parent.Children)
	}
	child := result.Nodes["standup-notes"]
	if child.Parent != "standup" {
		t.Fatalf("standup-notes's Parent = %q, want %q", child.Parent, "standup")
	}
}

func TestBuildTree_ParentNotVisibleBecomesRoot(t *testing.T) {
	// Only the child was returned by the query (e.g. the parent is
	// private and this read is anonymous) -- it must not be silently
	// dropped just because its parent can't be resolved.
	events := []*nip01.Event{
		metadataEvent("standup-notes", nip29.GroupMetadataParams{Name: "Notes", Parent: "standup"}),
	}

	result := buildTree(events)

	if len(result.Roots) != 1 || result.Roots[0] != "standup-notes" {
		t.Fatalf("Roots = %v, want [standup-notes] (its unseen parent doesn't disqualify it)", result.Roots)
	}
}

func TestBuildTree_ChildrenAndRootsAreSorted(t *testing.T) {
	events := []*nip01.Event{
		metadataEvent("b-group", nip29.GroupMetadataParams{}),
		metadataEvent("a-group", nip29.GroupMetadataParams{}),
		metadataEvent("z-child", nip29.GroupMetadataParams{Parent: "a-group"}),
		metadataEvent("m-child", nip29.GroupMetadataParams{Parent: "a-group"}),
	}

	result := buildTree(events)

	if len(result.Roots) != 2 || result.Roots[0] != "a-group" || result.Roots[1] != "b-group" {
		t.Fatalf("Roots = %v, want sorted [a-group b-group]", result.Roots)
	}
	children := result.Nodes["a-group"].Children
	if len(children) != 2 || children[0] != "m-child" || children[1] != "z-child" {
		t.Fatalf("a-group's Children = %v, want sorted [m-child z-child]", children)
	}
}

func TestBuildTree_EmptyInput(t *testing.T) {
	result := buildTree(nil)

	if len(result.Roots) != 0 || len(result.Nodes) != 0 {
		t.Fatalf("buildTree(nil) = %+v, want empty", result)
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"roots":[],"nodes":{}}` {
		t.Errorf("empty tree JSON = %s, want roots [] not null", got)
	}
}
