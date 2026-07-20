package ncli

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

// prefsTestEnv isolates a spawned ncli process's prefs.yaml to a fresh temp
// dir, so these tests never touch the real user's config.
func prefsTestEnv(t *testing.T) []string {
	t.Helper()
	return append(os.Environ(), "XDG_CONFIG_HOME="+t.TempDir())
}

// TestPrefsRelaysJSONMode is the regression test for "prefs relays
// add"/"remove"/"list"/"clear" and "prefs path" silently ignoring --json
// (the documented global flag every subcommand is supposed to honor,
// per AGENTS.md) and producing no stdout output at all -- unlike every
// other mutating command with a --json mode (e.g. "id --save --json").
func TestPrefsRelaysJSONMode(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the ncli binary; skipped in -short mode")
	}
	bin := buildTestBinary(t)
	env := prefsTestEnv(t)

	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%v failed: %v", args, err)
		}
		return out
	}

	t.Run("list --json on an empty prefs file is [] not null", func(t *testing.T) {
		var got struct {
			Relays []string `json:"relays"`
		}
		if err := json.Unmarshal(run("prefs", "relays", "list", "--json"), &got); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if got.Relays == nil {
			t.Error("relays = null, want []")
		}
		if len(got.Relays) != 0 {
			t.Errorf("relays = %v, want empty", got.Relays)
		}
	})

	t.Run("add --json reports added:true then added:false", func(t *testing.T) {
		var first struct {
			Relay string `json:"relay"`
			Added bool   `json:"added"`
		}
		if err := json.Unmarshal(run("prefs", "relays", "add", "relay.example.com", "--json"), &first); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if !first.Added || first.Relay != "relay.example.com" {
			t.Errorf("first add = %+v, want added=true relay=relay.example.com", first)
		}

		var second struct {
			Added bool `json:"added"`
		}
		if err := json.Unmarshal(run("prefs", "relays", "add", "relay.example.com", "--json"), &second); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if second.Added {
			t.Error("re-adding the same relay: added = true, want false (already configured)")
		}
	})

	t.Run("list --json now reflects the add", func(t *testing.T) {
		var got struct {
			Relays []string `json:"relays"`
		}
		if err := json.Unmarshal(run("prefs", "relays", "list", "--json"), &got); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if len(got.Relays) != 1 || got.Relays[0] != "relay.example.com" {
			t.Errorf("relays = %v, want [relay.example.com]", got.Relays)
		}
	})

	t.Run("path --json", func(t *testing.T) {
		var got struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(run("prefs", "path", "--json"), &got); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if got.Path == "" {
			t.Error("path = \"\", want the prefs.yaml path")
		}
	})

	t.Run("remove --json reports removed:true then removed:false", func(t *testing.T) {
		var first struct {
			Removed bool `json:"removed"`
		}
		if err := json.Unmarshal(run("prefs", "relays", "remove", "relay.example.com", "--json"), &first); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if !first.Removed {
			t.Error("removed = false, want true")
		}

		var second struct {
			Removed bool `json:"removed"`
		}
		if err := json.Unmarshal(run("prefs", "relays", "remove", "relay.example.com", "--json"), &second); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if second.Removed {
			t.Error("removing an already-absent relay: removed = true, want false")
		}
	})

	t.Run("clear --json", func(t *testing.T) {
		run("prefs", "relays", "add", "relay.example.com", "--json")
		var got struct {
			Cleared bool `json:"cleared"`
		}
		if err := json.Unmarshal(run("prefs", "relays", "clear", "--json"), &got); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if !got.Cleared {
			t.Error("cleared = false, want true")
		}
	})
}
