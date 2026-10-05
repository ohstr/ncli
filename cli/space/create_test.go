package space

import (
	"context"
	"testing"
)

// TestRunCreate_RejectsAmbiguousID checks that a bad id is caught before
// ever touching identity resolution or publishing -- relay resolution via
// an explicit --relay succeeds locally, so a test vault/network isn't
// needed to reach the id check.
func TestRunCreate_RejectsAmbiguousID(t *testing.T) {
	for _, id := range []string{"a/b", "a?b", "a#b", "a%2Fb", ""} {
		cmd := newCreateCommand()
		cmd.SetContext(context.Background())
		cmd.Flags().String("relay", "", "")
		_ = cmd.Flags().Set("relay", "wss://relay.example")
		cmd.Flags().String("identity", "", "")
		cmd.Flags().Bool("json", false, "")

		if err := cmd.RunE(cmd, []string{id}); err == nil {
			t.Errorf("runCreate() with id %q = nil error, want an error", id)
		}
	}
}
