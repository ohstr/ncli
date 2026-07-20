package ncli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/spf13/cobra"
)

func newTestFindCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "find"}
	registerQueryFlags(cmd, "")
	cmd.Flags().StringP("out", "o", "", "")
	return cmd
}

func TestFindArgs_TooManyPositional(t *testing.T) {
	cmd := newTestFindCmd()
	if err := findCmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Fatal("Args(2 positional) error = nil, want an error")
	}
}

func TestFindArgs_NoneGivenErrors(t *testing.T) {
	cmd := newTestFindCmd()
	if err := findCmd.Args(cmd, nil); err == nil {
		t.Fatal("Args(no identifier, no filter flag, no --targets) error = nil, want an error")
	}
}

func TestFindArgs_OnePositionalIsEnough(t *testing.T) {
	cmd := newTestFindCmd()
	if err := findCmd.Args(cmd, []string{"abc123"}); err != nil {
		t.Fatalf("Args(1 positional) error = %v, want nil", err)
	}
}

func TestFindArgs_InlineFilterFlagIsEnough(t *testing.T) {
	cmd := newTestFindCmd()
	mustSet(t, cmd, "kinds", "1")
	if err := findCmd.Args(cmd, nil); err != nil {
		t.Fatalf("Args(--kinds set, no positional) error = %v, want nil", err)
	}
}

func TestFindArgs_TargetsIsEnough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.yaml")
	if err := os.WriteFile(path, []byte("kind: targets\nspec:\n  relays: []\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := newTestFindCmd()
	mustSet(t, cmd, "targets", path)
	if err := findCmd.Args(cmd, nil); err != nil {
		t.Fatalf("Args(--targets set, no positional) error = %v, want nil", err)
	}
}

func TestFindArgs_TargetsWithRelaysIsMutuallyExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.yaml")
	if err := os.WriteFile(path, []byte("kind: targets\nspec:\n  relays: []\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := newTestFindCmd()
	mustSet(t, cmd, "targets", path)
	mustSet(t, cmd, "relays", "wss://relay.damus.io")
	if err := findCmd.Args(cmd, nil); err == nil {
		t.Fatal("Args(--targets + --relays) error = nil, want a mutual-exclusion error")
	}
}

// TestMergeFindIdentifier_* lock in the AND-vs-OR distinction between an
// event-shaped and an author-shaped identifier: an event ID stands alone as
// its own filter (harmless to OR, since an ID already pins one exact
// event), while an author must be ANDed into every existing filter so
// `ncli find alice@example.com --kinds 1` means "this person's kind-1
// notes," not "this person's anything, OR any kind-1 note from anyone" --
// the bug caught by manual testing before this coverage existed.

func TestMergeFindIdentifier_Nil(t *testing.T) {
	idFilter, filters := mergeFindIdentifier(nil, nil)
	if idFilter != nil {
		t.Fatalf("idFilter = %+v, want nil", idFilter)
	}
	if filters != nil {
		t.Fatalf("filters = %+v, want nil", filters)
	}
}

func TestMergeFindIdentifier_EventID_StandsAloneOred(t *testing.T) {
	existing := []*client.FilterSpec{client.NewFilterSpec(&nip01.SubscriptionFilter{Kinds: []int{1}, Limit: 5})}

	idFilter, filters := mergeFindIdentifier(&client.FindIdentifier{ID: "abc123"}, existing)

	if idFilter == nil || len(idFilter.IDs) != 1 || idFilter.IDs[0] != "abc123" {
		t.Fatalf("idFilter = %+v, want IDs=[abc123]", idFilter)
	}
	if len(filters) != 1 || len(filters[0].Authors) != 0 {
		t.Fatalf("filters mutated unexpectedly for an event-shaped identifier: %+v", filters)
	}
	if len(filters[0].Kinds) != 1 || filters[0].Kinds[0] != 1 || filters[0].Limit != 5 {
		t.Fatalf("existing filter's fields changed: %+v", filters[0])
	}
}

func TestMergeFindIdentifier_Author_ANDsIntoExistingFilter(t *testing.T) {
	existing := []*client.FilterSpec{client.NewFilterSpec(&nip01.SubscriptionFilter{Kinds: []int{1}, Limit: 5})}

	idFilter, filters := mergeFindIdentifier(&client.FindIdentifier{Author: "deadbeef"}, existing)

	if idFilter != nil {
		t.Fatalf("idFilter = %+v, want nil for an author-shaped identifier", idFilter)
	}
	if len(filters) != 1 {
		t.Fatalf("expected the existing filter to be reused (ANDed), got %d filters", len(filters))
	}
	if len(filters[0].Authors) != 1 || filters[0].Authors[0] != "deadbeef" {
		t.Fatalf("filters[0].Authors = %v, want [deadbeef]", filters[0].Authors)
	}
	if len(filters[0].Kinds) != 1 || filters[0].Kinds[0] != 1 || filters[0].Limit != 5 {
		t.Fatalf("existing filter's other fields changed: %+v", filters[0])
	}
}

// TestMergeFindIdentifier_Author_NoFilters_DefaultsToProfile locks in that
// a bare `ncli find <npub>` (no --kinds/--limit/--targets at all) fetches
// just that author's profile (kind 0), not an unbounded dump of everything
// they've ever published -- the behavior a live test against
// wss://relay.damus.io showed returns 147 events of every kind before this
// default existed.
func TestMergeFindIdentifier_Author_NoFilters_DefaultsToProfile(t *testing.T) {
	idFilter, filters := mergeFindIdentifier(&client.FindIdentifier{Author: "deadbeef"}, nil)

	if idFilter != nil {
		t.Fatalf("idFilter = %+v, want nil", idFilter)
	}
	if len(filters) != 1 {
		t.Fatalf("filters = %+v, want exactly one fresh filter", filters)
	}
	if len(filters[0].Authors) != 1 || filters[0].Authors[0] != "deadbeef" {
		t.Fatalf("filters[0].Authors = %v, want [deadbeef]", filters[0].Authors)
	}
	if len(filters[0].Kinds) != 1 || filters[0].Kinds[0] != 0 {
		t.Fatalf("filters[0].Kinds = %v, want [0] (profile-only default)", filters[0].Kinds)
	}
}

func TestMergeFindIdentifier_Author_ANDsIntoEveryORBranch(t *testing.T) {
	// A --targets file can declare multiple OR'd filters. The author must
	// AND into EVERY branch, not just the first, so "(kind1 OR kind7) AND
	// author=X" holds rather than "kind1 OR (kind7 AND author=X)".
	existing := []*client.FilterSpec{
		client.NewFilterSpec(&nip01.SubscriptionFilter{Kinds: []int{1}}),
		client.NewFilterSpec(&nip01.SubscriptionFilter{Kinds: []int{7}}),
	}

	_, filters := mergeFindIdentifier(&client.FindIdentifier{Author: "deadbeef"}, existing)

	for i, f := range filters {
		if len(f.Authors) != 1 || f.Authors[0] != "deadbeef" {
			t.Fatalf("filters[%d].Authors = %v, want [deadbeef]", i, f.Authors)
		}
	}
}
