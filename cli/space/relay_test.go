package space

import "testing"

func TestResolveRelayHonorsTheFlag(t *testing.T) {
	cmd := newTestCmd()

	got, err := resolveRelay(cmd, "relay.example")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.String() != "wss://relay.example" {
		t.Errorf("want wss://relay.example, got %q", got)
	}

	got, err = resolveRelay(cmd, "ws://localhost:7777")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.String() != "ws://localhost:7777" {
		t.Errorf("want ws://localhost:7777, got %q", got)
	}

	if _, err := resolveRelay(cmd, "http://relay.example"); err == nil {
		t.Error("a non-ws scheme should be rejected")
	}
}

func TestResolveRelay_NoFlagNoPrefsErrors(t *testing.T) {
	withTempConfigDir(t)

	if _, err := resolveRelay(newTestCmd(), ""); err == nil {
		t.Fatal("resolveRelay() with no flag and no configured prefs relays = nil error, want an error")
	}
}
