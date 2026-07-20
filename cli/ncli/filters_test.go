package ncli

import (
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func newTestFilterCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	registerInlineFilterFlags(cmd)
	return cmd
}

func TestInlineFilterFlagsChanged(t *testing.T) {
	cmd := newTestFilterCmd()
	if inlineFilterFlagsChanged(cmd) {
		t.Fatal("expected no inline filter flags to be changed by default")
	}
	if err := cmd.Flags().Set("kinds", "1,7"); err != nil {
		t.Fatal(err)
	}
	if !inlineFilterFlagsChanged(cmd) {
		t.Fatal("expected inlineFilterFlagsChanged to report true after --kinds is set")
	}
}

func TestBuildInlineFilterSpecNilWhenUnset(t *testing.T) {
	cmd := newTestFilterCmd()
	spec, err := buildInlineFilterSpec(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec != nil {
		t.Fatalf("expected a nil spec when no inline flags are set, got %+v", spec)
	}
}

func TestBuildInlineFilterSpecKindsAuthorsIDs(t *testing.T) {
	cmd := newTestFilterCmd()
	mustSet(t, cmd, "kinds", " 1, 7 ")
	mustSet(t, cmd, "authors", "abc, def")
	mustSet(t, cmd, "ids", "111,222")

	spec, err := buildInlineFilterSpec(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(spec.Kinds) != 2 || spec.Kinds[0] != 1 || spec.Kinds[1] != 7 {
		t.Fatalf("unexpected kinds: %v", spec.Kinds)
	}
	if len(spec.Authors) != 2 || spec.Authors[0] != "abc" || spec.Authors[1] != "def" {
		t.Fatalf("unexpected authors: %v", spec.Authors)
	}
	if len(spec.IDs) != 2 || spec.IDs[0] != "111" || spec.IDs[1] != "222" {
		t.Fatalf("unexpected ids: %v", spec.IDs)
	}
}

func TestBuildInlineFilterSpecInvalidKind(t *testing.T) {
	cmd := newTestFilterCmd()
	mustSet(t, cmd, "kinds", "abc")
	if _, err := buildInlineFilterSpec(cmd); err == nil {
		t.Fatal("expected an error for a non-integer --kinds value")
	}
}

func TestBuildInlineFilterSpecSinceUntil(t *testing.T) {
	cmd := newTestFilterCmd()
	mustSet(t, cmd, "since", "1h")

	before := time.Now().Add(-time.Hour).Unix()
	spec, err := buildInlineFilterSpec(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	after := time.Now().Add(-time.Hour).Unix()

	if int64(spec.Since) < before-2 || int64(spec.Since) > after+2 {
		t.Fatalf("since=%d not close to expected ~%d", spec.Since, before)
	}
}

func TestBuildInlineFilterSpecTags(t *testing.T) {
	cmd := newTestFilterCmd()
	mustSet(t, cmd, "tag", "e=a")
	mustSet(t, cmd, "tag", "e=b")
	mustSet(t, cmd, "tag", "p=pubkey1")

	spec, err := buildInlineFilterSpec(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := spec.Tags["e"]; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("unexpected #e tags: %v", got)
	}
	if got := spec.Tags["p"]; len(got) != 1 || got[0] != "pubkey1" {
		t.Fatalf("unexpected #p tags: %v", got)
	}
}

func TestBuildInlineFilterSpecInvalidTag(t *testing.T) {
	cmd := newTestFilterCmd()
	mustSet(t, cmd, "tag", "no-equals-sign")
	if _, err := buildInlineFilterSpec(cmd); err == nil {
		t.Fatal("expected an error for a malformed --tag value")
	}
}

func TestBuildInlineFilterSpecLimitZeroIsExplicit(t *testing.T) {
	cmd := newTestFilterCmd()
	mustSet(t, cmd, "limit", "0")

	spec, err := buildInlineFilterSpec(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec == nil {
		t.Fatal("expected a non-nil spec since --limit was explicitly set, even though its value is the zero value")
	}
}

func TestInlineFilterSpecSliceNilWhenUnset(t *testing.T) {
	cmd := newTestFilterCmd()
	specs, err := inlineFilterSpecSlice(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if specs != nil {
		t.Fatalf("expected a nil slice when no inline flags are set, got %+v", specs)
	}
}

func TestInlineFilterSpecSliceWrapsSingleFilter(t *testing.T) {
	cmd := newTestFilterCmd()
	mustSet(t, cmd, "kinds", "1,7")

	specs, err := inlineFilterSpecSlice(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected exactly one filter, got %d", len(specs))
	}
	if len(specs[0].Kinds) != 2 || specs[0].Kinds[0] != 1 || specs[0].Kinds[1] != 7 {
		t.Fatalf("unexpected kinds: %v", specs[0].Kinds)
	}
}

func mustSet(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	if err := cmd.Flags().Set(name, value); err != nil {
		t.Fatalf("failed to set --%s=%q: %v", name, value, err)
	}
}
