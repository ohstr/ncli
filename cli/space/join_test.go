package space

import (
	"context"
	"testing"
)

func TestNewJoinCommand_AcceptsZeroOrOneArg(t *testing.T) {
	cmd := newJoinCommand()
	if err := cmd.Args(cmd, []string{}); err != nil {
		t.Errorf("join with no args should be accepted (sole-space fallback): %v", err)
	}
	if err := cmd.Args(cmd, []string{"standup"}); err != nil {
		t.Errorf("join with one arg should be accepted: %v", err)
	}
	if err := cmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Error("join with two args should be rejected")
	}
}

func TestSoleOpenSpaceAddress_NoRelayConfigured(t *testing.T) {
	withTempConfigDir(t)
	cmd := newJoinCommand()
	cmd.SetContext(context.Background())

	if _, err := soleOpenSpaceAddress(cmd); err == nil {
		t.Fatal("soleOpenSpaceAddress() with no --relay and no configured prefs = nil error, want an error")
	}
}
