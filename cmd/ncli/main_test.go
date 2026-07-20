package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// resolve walks rootCmd's tree for args (e.g. "relay", "admin", "stats")
// without executing anything, and fails the test if it doesn't fully
// resolve to a command whose own Name() matches the last arg -- guarding
// against both a missing command and cobra falling back to a shallower
// partial match.
func resolve(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd, _, err := rootCmd.Find(args)
	if err != nil {
		t.Fatalf("Find(%v) returned error: %v", args, err)
	}
	want := args[len(args)-1]
	if cmd.Name() != want {
		t.Fatalf("Find(%v) resolved to %q, want a command named %q", args, cmd.Name(), want)
	}
	return cmd
}

// TestCommandTree_RelayFlattensAdmin guards the "admin" layer's removal
// (ncli relay admin stats -> ncli relay stats) against silently regressing
// back to a nested "admin" tree, or silently losing a subcommand during a
// future refactor. stats/reindex/clear still work by making NIP-98
// authenticated HTTP requests to a running relay -- flattening only
// dropped the "admin" path segment, not their online-only nature (there is
// still no offline reindex; see TestCommandTree_OfflineReindexRemoved).
func TestCommandTree_RelayFlattensAdmin(t *testing.T) {
	paths := [][]string{
		{"relay", "stats"},
		{"relay", "reindex", "search"},
		{"relay", "reindex", "zaps"},
		{"relay", "clear", "search"},
		{"relay", "clear", "zaps"},
	}
	for _, p := range paths {
		resolve(t, p...)
	}
}

// TestCommandTree_AdminRemoved guards against "admin" reappearing anywhere
// in the tree, nested or not, now that its stats/reindex/clear children
// live directly under "relay". Find() falls back to the deepest command it
// can still resolve, so on an absent "admin" it should stop at "relay"
// rather than reach an "admin" or "stats" command.
func TestCommandTree_AdminRemoved(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"relay", "admin", "stats"})
	if err != nil {
		t.Fatalf("Find returned error: %v", err)
	}
	if cmd.Name() == "admin" || cmd.Name() == "stats" {
		t.Fatalf("resolved to %q; \"relay admin\" should no longer exist", cmd.Name())
	}
}

// TestCommandTree_OfflineReindexRemoved guards against a *second*,
// offline "reindex" implementation reappearing under "relay": reindexing
// only happens through a live relay via "ncli relay reindex", which
// exercises the same underlying engine
// (cli/reindex.ExecuteSearchReindex/ExecuteZapReindex) that used to live
// one level deeper at "ncli relay admin reindex" before the admin flatten.
func TestCommandTree_OfflineReindexRemoved(t *testing.T) {
	cmd := resolve(t, "relay", "reindex", "search")
	if cmd.RunE == nil {
		t.Fatalf("relay reindex search has no RunE; expected the NIP-98 admin-request implementation")
	}
}

// TestCommandTree_RelayMembershipAdmin guards the NIP-43 membership admin
// commands' wiring directly onto "relay" (same flattened shape as
// stats/reindex/clear -- three parent groups, "members"/"invites"/"roles",
// each with its own children) against a future refactor silently dropping
// one.
func TestCommandTree_RelayMembershipAdmin(t *testing.T) {
	paths := [][]string{
		{"relay", "members", "list"},
		{"relay", "members", "show"},
		{"relay", "members", "add"},
		{"relay", "members", "remove"},
		{"relay", "invites", "create"},
		{"relay", "invites", "list"},
		{"relay", "invites", "revoke"},
		{"relay", "roles", "list"},
		{"relay", "roles", "create"},
	}
	for _, p := range paths {
		cmd := resolve(t, p...)
		if cmd.RunE == nil {
			t.Errorf("%v: no RunE; expected the NIP-98 admin-request implementation", p)
		}
	}
}

// TestCommandTree_GroupCommandsRequireSubcommand guards every "group"
// command -- one whose entire job is dispatching to children, with no
// RunE of its own -- against silently regressing to cobra's default
// no-RunE behavior: printing help and exiting 0 for a bare or misspelled
// invocation, even under --json. Each of these must instead resolve to a
// command wired with common.RequireSubcommand (cli/common/args.go), so
// that case comes back as a proper "usage" CLIError (exit 2, stderr-only,
// structured under --json) like every other invocation mistake -- see
// TestRequireSubcommand (cli/common/args_test.go) for the error shape
// itself.
func TestCommandTree_GroupCommandsRequireSubcommand(t *testing.T) {
	paths := [][]string{
		{"relay", "reindex"},
		{"relay", "clear"},
		{"relay", "members"},
		{"relay", "invites"},
		{"relay", "roles"},
		{"prefs"},
		{"prefs", "relays"},
		{"miner"},
	}
	for _, p := range paths {
		cmd := resolve(t, p...)
		if cmd.RunE == nil {
			t.Errorf("%v: no RunE; expected common.RequireSubcommand", p)
		}
	}
}

// TestCommandTree_IDNestsDelegate guards the "delegate" move under "id"
// (ncli delegate -> ncli id delegate), and the flag-collision fix that move
// required: idCmd's --save/--label/--reveal must be local flags, not
// PersistentFlags, or they'd leak into delegate's flag set. --json is no
// longer local to either -- it's a root-level PersistentFlag (see
// cli/ncli/root.go) inherited by every command, delegate included, so it's
// checked via InheritedFlags (which triggers the same persistent-flag
// merge cobra performs at Execute time) rather than Flags.
func TestCommandTree_IDNestsDelegate(t *testing.T) {
	delegateCmd := resolve(t, "id", "delegate")

	for _, want := range []string{"issuer-key", "relay-key", "kinds", "duration"} {
		if delegateCmd.Flags().Lookup(want) == nil {
			t.Errorf("id delegate: expected --%s flag, not found", want)
		}
	}
	if delegateCmd.InheritedFlags().Lookup("json") == nil {
		t.Error("id delegate: expected --json to be inherited from the root persistent flag, not found")
	}
	for _, unwanted := range []string{"save", "label", "reveal"} {
		if delegateCmd.Flags().Lookup(unwanted) != nil {
			t.Errorf("id delegate: --%s leaked in from id's flags (should be local to id, not inherited)", unwanted)
		}
	}
}

// TestCommandTree_OldFlatPathsRemoved guards against "admin", "reindex",
// and "delegate" reappearing as direct root subcommands. "delegate" has a
// nested replacement (id delegate); "admin" was flattened directly onto
// "relay" (relay admin stats -> relay stats, no "admin" segment left
// anywhere) and "reindex" was removed outright, folded into what's now
// "relay reindex". Deliberately no back-compat aliases at any of the old
// paths.
func TestCommandTree_OldFlatPathsRemoved(t *testing.T) {
	for _, name := range []string{"admin", "reindex", "delegate"} {
		for _, c := range rootCmd.Commands() {
			if c.Name() == name {
				t.Errorf("root command %q still exists as a top-level subcommand; expected it to only exist nested (relay %s / id %s)", name, name, name)
			}
		}
	}
}
