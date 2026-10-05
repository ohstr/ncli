package groups

import "testing"

func TestVisibilityLabel(t *testing.T) {
	if got := visibilityLabel(true); got != "private" {
		t.Fatalf("visibilityLabel(true) = %q, want %q", got, "private")
	}
	if got := visibilityLabel(false); got != "public" {
		t.Fatalf("visibilityLabel(false) = %q, want %q", got, "public")
	}
}

func TestMembershipLabel(t *testing.T) {
	if got := membershipLabel(true); got != "closed" {
		t.Fatalf("membershipLabel(true) = %q, want %q", got, "closed")
	}
	if got := membershipLabel(false); got != "open" {
		t.Fatalf("membershipLabel(false) = %q, want %q", got, "open")
	}
}

func TestDashIfEmpty(t *testing.T) {
	if got := dashIfEmpty(""); got != "-" {
		t.Fatalf("dashIfEmpty(\"\") = %q, want %q", got, "-")
	}
	if got := dashIfEmpty("x"); got != "x" {
		t.Fatalf("dashIfEmpty(%q) = %q, want unchanged", "x", got)
	}
}
