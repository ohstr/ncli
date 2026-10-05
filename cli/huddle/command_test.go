package huddle

import (
	"net/url"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/ohstr/ncli/huddleclient"
	"github.com/ohstr/nmilat/huddle/wsaudio"
	"github.com/spf13/cobra"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func TestHuddleEndpoint(t *testing.T) {
	tests := []struct {
		name string
		base string
		room string
		want string
	}{
		{"plain host", "wss://relay.example", "standup", "wss://relay.example/huddle/standup/audio"},
		{"trailing slash is not doubled", "wss://relay.example/", "standup", "wss://relay.example/huddle/standup/audio"},
		{"relay behind a base path", "wss://relay.example/nostr", "standup", "wss://relay.example/nostr/huddle/standup/audio"},
		{"insecure scheme preserved", "ws://localhost:7777", "standup", "ws://localhost:7777/huddle/standup/audio"},
		{"room id is surrounding-space tolerant", "wss://relay.example", "  standup  ", "wss://relay.example/huddle/standup/audio"},
		{"uuid room id", "wss://relay.example", "8f14e45f-ea8b-4f1b-9e0c-1a2b3c4d5e6f",
			"wss://relay.example/huddle/8f14e45f-ea8b-4f1b-9e0c-1a2b3c4d5e6f/audio"},
		// A query or fragment on the configured relay URL has no meaning on the
		// huddle endpoint, so it is dropped rather than carried along.
		{"query and fragment dropped", "wss://relay.example/?x=1#f", "standup", "wss://relay.example/huddle/standup/audio"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Endpoint(mustURL(t, tt.base), tt.room)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Endpoint(%q, %q) = %q, want %q", tt.base, tt.room, got, tt.want)
			}
		})
	}
}

func TestHuddleEndpointRejectsAmbiguousRoomIDs(t *testing.T) {
	// Each of these would change the URL's path shape. Escaping instead of
	// rejecting would leave the client asking for one room and the relay
	// matching another.
	for _, room := range []string{"", "   ", "a/b", "a?b", "a#b", "a%2Fb", "../admin"} {
		if got, err := Endpoint(mustURL(t, "wss://relay.example"), room); err == nil {
			t.Errorf("room %q should have been rejected, got %q", room, got)
		}
	}
}

func TestRefusalHint(t *testing.T) {
	codes := []string{
		wsaudio.CodeAudioUnavailable,
		wsaudio.CodeRoomFull,
		wsaudio.CodeRoomEnded,
		wsaudio.CodeRoomUnavailable,
		wsaudio.CodeUpgradeRequired,
		wsaudio.CodeJoinRejected,
		wsaudio.CodeAuthFailed,
	}
	seen := map[string]string{}
	for _, code := range codes {
		hint := refusalHint(&huddleclient.RefusedError{Code: code})
		if hint == "" {
			t.Errorf("code %q has no hint", code)
			continue
		}
		if prev, dup := seen[hint]; dup && prev != wsaudio.CodeJoinRejected && code != wsaudio.CodeJoinRejected {
			t.Errorf("codes %q and %q share the hint %q", prev, code, hint)
		}
		seen[hint] = code
	}

	// An unrecognized code must yield nothing, so the caller shows the relay's
	// own message rather than a made-up explanation.
	if hint := refusalHint(&huddleclient.RefusedError{Code: "something_new"}); hint != "" {
		t.Errorf("want no hint for an unknown code, got %q", hint)
	}

	// The version mismatch is the one hint with real detail to carry.
	v := uint8(2)
	withVersion := refusalHint(&huddleclient.RefusedError{Code: wsaudio.CodeUpgradeRequired, CurrentVersion: &v})
	if !strings.Contains(withVersion, "v2") {
		t.Errorf("want the room's version in the hint, got %q", withVersion)
	}
	without := refusalHint(&huddleclient.RefusedError{Code: wsaudio.CodeUpgradeRequired})
	if without == withVersion {
		t.Error("the hint should say more when the relay reported its version")
	}
}

func TestHuddleCommandShape(t *testing.T) {
	cmd := NewHuddleCommand()
	if cmd.Use != "huddle" {
		t.Errorf("want the command named huddle, got %q", cmd.Use)
	}
	if cmd.PersistentFlags().Lookup("identity") == nil {
		t.Error("want a persistent --identity flag")
	}

	var join *cobra.Command
	for _, c := range cmd.Commands() {
		if strings.HasPrefix(c.Use, "join") {
			join = c
		}
	}
	if join == nil {
		t.Fatal("want a join subcommand")
	}
	if join.Flags().Lookup("relay") == nil {
		t.Error("want a --relay flag on join")
	}
	if join.RunE == nil {
		t.Error("join should have a RunE")
	}
	// Exactly one room, so neither a bare "join" nor a second positional slips
	// through to the dial.
	if err := join.Args(join, []string{}); err == nil {
		t.Error("join with no room should be rejected")
	}
	if err := join.Args(join, []string{"a", "b"}); err == nil {
		t.Error("join with two rooms should be rejected")
	}
	if err := join.Args(join, []string{"standup"}); err != nil {
		t.Errorf("join with one room should be accepted: %v", err)
	}
}

func TestResolveRelayHonorsTheFlag(t *testing.T) {
	cmd := NewJoinCommand()

	// A bare host is accepted the same bare-host-friendly way every other ncli
	// relay input is, and defaults to wss.
	got, err := resolveRelay(cmd, "relay.example")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.String() != "wss://relay.example" {
		t.Errorf("want wss://relay.example, got %q", got)
	}

	// An explicit scheme is taken at face value -- ws:// is how a local relay is
	// reached, and silently upgrading it would break that.
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

func TestHandleKeyFallsThroughWithoutAnApp(t *testing.T) {
	fc := newFakeClient()
	b := NewBoard(nil, fc, "room-1")

	// With no app there is no confirm dialog to show, so the keystroke must pass
	// on to tview rather than be swallowed and leave the user with a dead key.
	quit := tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone)
	if got := b.handleKey(quit); got != quit {
		t.Error("q should fall through when no app is attached")
	}
	// An unrelated key is never claimed.
	other := tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone)
	if got := b.handleKey(other); got != other {
		t.Error("an unhandled rune must be passed on untouched")
	}
	if fc.closeCount() != 0 {
		t.Error("no keystroke here should have closed the call")
	}
}
