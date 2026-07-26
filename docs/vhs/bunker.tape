# Records the full `ncli bunker` story as one real, causally-connected
# flow, entirely from inside the TUI: create an identity, open `ncli
# bunker`, pair an app by pasting a nostrconnect:// URI it already
# generated, approve its first signing request with a kind-scoped "Always"
# grant, then watch a second request outside that grant's scope get
# rejected -- finishing with a short tour of Trusted Apps/Request History
# to show what got remembered. Run via `just vhs bunker`.
#
# Every command the viewer actually sees is a plain `ncli` invocation or a
# keystroke inside its TUI -- no `go run`, no `export`/`FOO=$(...)` shell
# plumbing, nothing that only makes sense to someone reading the tape
# script rather than watching the recording. Everything mechanical (an
# isolated state dir, the relay this recording talks to, the counterpart
# "app" actually completing the pairing) happens off camera, in the Hidden
# block below.
#
# ncli has no NIP-46 client of its own (id sign/publish/apply only ever
# sign locally), so unlike every other tape here, showing the real payoff
# needs a second actor playing that role -- see skills/ncli-bunker/
# SKILL.md's "Using bunker with an AI agent". The throwaway
# docs/vhs/bunker-demo-client.go (go:build ignore, never shipped) plays it,
# started hidden, well before the visible recording begins: it generates
# its own nostrconnect:// URI from a FIXED, demo-only keypair+secret (never
# funded, never saved to any vault, never reused outside this recording),
# then waits. That fixed URI is why this works at all -- VHS has no way to
# interpolate a runtime value into a keystroke sequence, so the literal
# string typed into the TUI below has to be known in advance and must
# match, byte for byte, the one the demo client actually built (see that
# file's own doc comment for the consequence of changing one without the
# other). Once pairing completes, the same demo client sends a kind 1
# sign_event request (approved below, with a remembered "kind 1 only"
# grant) and then a kind 7 one (rejected -- it falls outside that grant, so
# it queues for a fresh decision same as the first one did, proving a
# kind-scoped grant doesn't leak to other kinds).
#
# Uses wss://relay.ohstr.com -- the same live relay
# cli/bunker/daemon_integration_test.go's own liveTestClient already
# depends on for this exact protocol, not a bigger public relay, since
# that's the one this wire path is actually proven against.
#
# The identity created on screen is a fresh vault entry under a throwaway
# vault (XDG_CONFIG_HOME redirected to /tmp/ncli-demo/config for the whole
# recording -- common.AppConfigDir's own resolution root for every one of
# vault.json/sessions.yaml/bunker.sock/daemon.log/events.wal), saved with a
# vault password sourced from NCLI_VAULT_PASSWORD (set hidden, never
# typed/shown) purely so `ncli id --save` and the later vault unlock behind
# `ncli bunker` need no interactive password prompt on screen -- without
# the isolated state dir, `ncli bunker`'s own "the first call reattaches to
# whatever's already running, ignoring --identity/--relay" behavior (see
# SKILL.md's gotchas) means a real pre-existing daemon on the recording
# machine gets silently taken over instead of a fresh one started, and
# Trusted Apps starts at a real 0 instead of carrying over whatever else
# was ever paired on that machine.
#
# `ncli bunker` itself is deliberately bare -- no --identity, no --relay --
# relying on the two fallbacks SKILL.md documents: the vault's sole entry
# (exactly one identity exists in this fresh vault: the one just created)
# and the configured `ncli prefs relays add` list (seeded hidden, right
# alongside the demo client, to the one relay this whole recording uses).
# That bareness is the point: it's what makes "create an identity, then
# just run `ncli bunker`" the on-screen story, instead of threading a raw
# nsec through a command-line flag.
#
# Same Width/FontSize/Theme as apply-stream.tape (this board is built on
# the same client/tui App -- see cli/bunker/board.go's own imports -- so it
# uses the same theme, not a generic vhs theme; mainly for the black
# background match apply-stream.tape's own comments explain -- Trusted
# Apps' own column count is why this uses apply-stream's wide layout, not
# ping's narrower one), but taller: identity(2) + logger(16, fixed) +
# sessions(8, one trusted app) already eats ~26 rows before
# historyPendingRow (Request History/Pending Requests, a proportional
# Flex item -- see NewBunkerBoard) gets anything, so apply's own Height
# 800 leaves it squeezed to nothing. 1200 leaves it a real, visible share.

Output docs/vhs/bunker.gif

Set Shell "bash"
Set FontSize 18
Set Width 1600
Set Height 1200
Set Padding 20
Set Theme {"name": "vscode-dark-modern", "black": "#000000", "red": "#cd3131", "green": "#0dbc79", "yellow": "#e5e510", "blue": "#2472c8", "magenta": "#bc3fbc", "cyan": "#11a8cd", "white": "#e5e5e5", "brightBlack": "#666666", "brightRed": "#f14c4c", "brightGreen": "#23d18b", "brightYellow": "#f5f543", "brightBlue": "#3b8eea", "brightMagenta": "#d670d6", "brightCyan": "#29b8db", "brightWhite": "#e5e5e5", "background": "#000000", "foreground": "#cccccc", "cursor": "#ffffff", "selection": "#264f78"}
Set TypingSpeed 40ms

# --- Off camera: isolated state dir, the one relay this recording uses,
#     and the counterpart "app" already generating its nostrconnect:// URI
#     and waiting -- nothing here is `ncli`'s own bunker story, so none of
#     it belongs on screen. ---

Hide
Type "rm -rf /tmp/ncli-demo && mkdir -p /tmp/ncli-demo && REPO=$(pwd)"
Enter
Sleep 300ms

Type "export XDG_CONFIG_HOME=/tmp/ncli-demo/config NCLI_VAULT_PASSWORD=vhs-demo-password"
Enter
Sleep 300ms

Type "ncli prefs relays add wss://relay.ohstr.com"
Enter
Sleep 1s

Type 'go run "$REPO/docs/vhs/bunker-demo-client.go" > /tmp/ncli-demo/client.log 2>&1 &'
Enter
Sleep 1s

Type "clear"
Enter
Show

# --- On camera: create an identity, open the bunker TUI, pair the app
#     that's already waiting by pasting its URI, then approve/reject its
#     requests -- one continuous, real flow. ---

Sleep 1s

Type "ncli id --save --label agent-key"
Enter
Sleep 5s

Type "clear"
Enter
Sleep 2000ms

Type "ncli bunker"
Enter
Sleep 4s

Type "c"
Sleep 2000ms
Tab
Sleep 2000ms
Enter
Sleep 2000ms

Type@1ms "nostrconnect://61ffd6c45416a60c586decb4e69a5bd5df553ec1a0c1f18b8d1bf71181c5182a?metadata=%7B%22name%22%3A%22Demo+App%22%2C%22url%22%3A%22https%3A%2F%2Fncli.dev%22%2C%22description%22%3A%22ncli+bunker+VHS+demo%22%7D&relay=wss%3A%2F%2Frelay.ohstr.com&secret=1f1c2d205d715375f192c87468490d82"
Sleep 2000ms
Tab
Sleep 2000ms
Enter
Sleep 6s

Tab
Sleep 800ms
Enter
Sleep 2s

Tab
Sleep 800ms
Enter
Sleep 2s

Sleep 4s
Tab
Sleep 2000ms
Tab
Sleep 2000ms
Tab
Sleep 2000ms
Enter
Sleep 3s

Tab
Sleep 3s
Tab
Sleep 5s
Tab
Sleep 4s

Ctrl+C
Sleep 1s
