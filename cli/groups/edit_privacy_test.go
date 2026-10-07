package groups

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ohstr/ncli/client"
)

// groupShowJSON decodes "groups show"'s --json output into just the fields
// these tests check -- a lean mirror of show.go's own groupDetail, not an
// import of it, so a future field added there doesn't change what these
// tests assert on.
type groupShowJSON struct {
	Metadata *struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Private  bool     `json:"private"`
		Closed   bool     `json:"closed"`
		Children []string `json:"children"`
	} `json:"metadata"`
}

func decodeGroupShow(t *testing.T, stdout string) groupShowJSON {
	t.Helper()
	var out groupShowJSON
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("unmarshal groups show JSON (%q): %v", stdout, err)
	}
	return out
}

// TestGroupsEdit_PreservesPrivacyOnUnrelatedField is the regression test for
// a real-relay repro: "groups edit <private-group> --name X", changing
// nothing about visibility, used to silently flip the group from
// private+closed to public+open. The cause was currentGroupMetadata (edit.go)
// reading the group's current kind:39000 with an unauthenticated query --
// which a private group (the default on creation) always denies -- so
// mergeEditParams merged the caller's --name onto a blank GroupMetadata{}
// instead of the real one, and the resulting kind:9002 replaced Private/
// Closed/Parent/Children with their zero values. Fixed by authenticating
// that read as the editor.
func TestGroupsEdit_PreservesPrivacyOnUnrelatedField(t *testing.T) {
	relayURL := newGroupsTestRelay(t)

	creator, err := client.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity (creator): %v", err)
	}

	const groupID = "edit-privacy-group"
	if _, err := execGroupsCmd(t, relayURL, "create", groupID, "--identity", creator.Nsec); err != nil {
		t.Fatalf("create %s: %v", groupID, err)
	}

	if _, err := execGroupsCmd(t, relayURL, "edit", groupID, "--name", "Renamed", "--identity", creator.Nsec); err != nil {
		t.Fatalf("edit %s --name: %v", groupID, err)
	}

	stdout, err := execGroupsCmd(t, relayURL, "show", groupID, "--identity", creator.Nsec)
	if err != nil {
		t.Fatalf("show %s: %v", groupID, err)
	}
	got := decodeGroupShow(t, stdout)
	if got.Metadata == nil {
		t.Fatalf("show %s = %q, want metadata present", groupID, stdout)
	}
	if got.Metadata.Name != "Renamed" {
		t.Errorf("name = %q, want %q", got.Metadata.Name, "Renamed")
	}
	if !got.Metadata.Private || !got.Metadata.Closed {
		t.Errorf("private=%v closed=%v after an unrelated --name edit, want both still true", got.Metadata.Private, got.Metadata.Closed)
	}

	// Still private: an anonymous show is refused outright, not answered.
	anonStdout, err := execGroupsCmd(t, relayURL, "show", groupID)
	if !errors.Is(err, client.ErrRestricted) {
		t.Errorf("anonymous show %s: err = %v, stdout %q -- want the relay's restriction, the group must still be private", groupID, err, anonStdout)
	}
}

// TestGroupsEdit_PreservesChildrenOnUnrelatedField is the other symptom of
// the same root cause: a parent group with subgroups rejected any edit
// that didn't re-list every current child, because the unauthenticated
// read in currentGroupMetadata came back blank (Children: nil) for the
// (private-by-default) parent, and the relay refuses an edit that drops a
// subgroup link silently.
func TestGroupsEdit_PreservesChildrenOnUnrelatedField(t *testing.T) {
	relayURL := newGroupsTestRelay(t)

	creator, err := client.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity (creator): %v", err)
	}

	const parentID = "edit-children-parent"
	const childID = "edit-children-child"
	if _, err := execGroupsCmd(t, relayURL, "create", parentID, "--identity", creator.Nsec); err != nil {
		t.Fatalf("create %s: %v", parentID, err)
	}
	if _, err := execGroupsCmd(t, relayURL, "create", childID, "--identity", creator.Nsec, "--parent", parentID); err != nil {
		t.Fatalf("create %s --parent %s: %v", childID, parentID, err)
	}

	if _, err := execGroupsCmd(t, relayURL, "edit", parentID, "--name", "Renamed Parent", "--identity", creator.Nsec); err != nil {
		t.Fatalf("edit %s --name: %v", parentID, err)
	}

	stdout, err := execGroupsCmd(t, relayURL, "show", parentID, "--identity", creator.Nsec)
	if err != nil {
		t.Fatalf("show %s: %v", parentID, err)
	}
	got := decodeGroupShow(t, stdout)
	if got.Metadata == nil {
		t.Fatalf("show %s = %q, want metadata present", parentID, stdout)
	}
	if got.Metadata.Name != "Renamed Parent" {
		t.Errorf("name = %q, want %q", got.Metadata.Name, "Renamed Parent")
	}
	if len(got.Metadata.Children) != 1 || got.Metadata.Children[0] != childID {
		t.Errorf("children = %v after an unrelated --name edit, want [%s]", got.Metadata.Children, childID)
	}
}
