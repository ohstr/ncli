---
name: ncli-bunker
description: Run ncli as a NIP-46 remote signer ("bunker") -- approve/reject other Nostr clients' signing requests from a live TUI, remember per-app permissions so you aren't re-prompted every time, and run it detached in the background (ncli bunker attach/status/stop/sessions/history/connect). Use when setting up remote signing, pairing a client via bunker:///nostrconnect://, pairing an AI agent as a NIP-46 client for unattended signing, managing remembered app permissions, reviewing what was approved/rejected/expired, or troubleshooting why a client isn't getting approved.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's cli/bunker/*.go as of writing. This skill is
self-contained by design and won't see repo changes automatically --
update by hand if flags/behavior change. -->

# ncli bunker

`ncli bunker` turns `ncli` into a working NIP-46 remote signer: your
private key never leaves this process. Other Nostr clients send it
encrypted signing requests over a relay; you approve or reject each one
(or a remembered rule does it automatically).

## Starting it

```sh
ncli bunker --identity agent-key                          # vault label
ncli bunker --identity nsec1...                            # raw nsec, no vault
ncli bunker                                                 # falls back to bunker.identity config / NCLI_BUNKER_IDENTITY / the vault's sole entry
ncli bunker --relay wss://relay.example --identity agent-key
```

`--identity` accepts the same shapes as `id sign`/`id delegate`: a vault
label, nsec, npub, hex, nprofile, or nip-05 -- it must resolve to a
**private** key. `--relay` is repeatable; omit it to fall back to
`ncli prefs relays add`'s configured list.

This needs a real interactive terminal (it's a TUI) -- `--json` or a
non-interactive stdin/stdout fails immediately with a clear usage error
instead of hanging, same as `id delegate`'s wizard.

## Background daemon (Linux/macOS)

The first `ncli bunker` call starts a detached background daemon and
attaches the TUI to it. A "Signing as: ..." line under the logo shows
which identity is running: a shortened npub (see `shortNpub`) always,
plus a resolved display name and/or nip05 once a best-effort kind:0
lookup against the configured relays finds one -- the npub keeps showing
even then, since a display name is self-reported profile data, not proof
of who's actually signing, while the npub is the one thing worth
cross-checking. Whenever the identity is also a saved vault entry
(whether `--identity` named the label directly, or named an npub/nsec/
nip-05 that just happens to already be saved), a trailing
`(vault: <label>)` is appended too -- e.g.
`Signing as: Alice (alice@example.com) (npub1qqs02u...mp3jqayth7d) (vault: agent-key)`.
Inside the TUI:

- Pending Requests auto-advances by default: the moment a request lands
  with nothing else already on screen, its approve/reject dialog pops up
  on its own -- pairing a new app walks straight through `connect` and
  its first few requests without you hunting down and selecting each row
  by hand. Toggle this with `p` (shown as "Auto-Prompt: On/Off" above the
  table); off falls back to fully manual triage. "Decide Later" on that
  dialog (also what `Esc` does) backs out of a request without deciding
  it and switches Auto-Prompt off itself, so it can't turn into an
  unclosable loop popping the same undecided request back up.
- `a`/`x` on a pending request approves/rejects it once, right from the
  table -- no dialog. `Enter` still opens the approve/reject dialog,
  needed for a remembered "Always ..." grant (see Remembering permissions
  below) but otherwise offering the same one-off Approve/Reject Once
  choices as `a`/`x`.
- `c` opens "Connect a new app" -- pair without leaving the TUI (see
  Pairing below), instead of needing a separate `ncli bunker connect` in
  another terminal. Works from any panel, the same as `b` below, not just
  while Pending Requests is focused.
- `b` opens a "Detach or stop?" dialog, from any panel -- **Detach**
  leaves the daemon running in the background (reattach any time with
  `ncli bunker attach`); **Stop bunker entirely** shuts it down. Unlike
  every other `ncli` TUI, closing this one never defaults to killing a
  live signer other apps might depend on.
- Tab/Shift+Tab cycles the four panels: activity log, then Trusted Apps,
  then Request History and Pending Requests together -- the latter two
  sit side by side in one row (Request History on the left, Pending
  Requests on the right) rather than stacked, since they're the two
  panels an operator's eye needs to move between constantly: what's
  still waiting vs. what was just decided.
- `Enter`, focused on Trusted Apps, opens **Manage Grants** for the
  selected app -- every one of its remembered grants listed individually
  (plain-language scope, Allowed/Blocked, expiry), with `x` to revoke just
  that one grant and `e` to extend/re-scope it (the same 1h/24h/7d/until
  revoked/next-10-uses choices the approval dialog's own duration step
  offers), without touching anything else the app holds.
- `r`, focused on Trusted Apps, revokes **all** of the selected app's
  permissions at once -- the bulk counterpart to Manage Grants' own
  per-grant `x`.
- `n`, focused on Trusted Apps, opens a modal to set (or clear) the
  selected app's own display name -- see "Trusted Apps vs. remembered
  permissions" below for where that name then shows up.
- Request History (to the left of Pending Requests) is a read-only log of
  what happened to every request that isn't pending anymore -- Approved
  (optionally "(always)", if it also created a remembered grant),
  Rejected (same), or Expired (nobody answered in time). Pending only
  ever shows what's still awaiting a decision, so once something's
  decided it moves here, not away entirely. Durably persisted (see
  Request History below) -- it survives a daemon restart.
- A red banner appears right under the identity line, above every panel,
  once no relay is connected -- the one failure mode that silently
  breaks bunker entirely, since a client's request just never arrives
  with nothing else on screen pointing at why. Placed first, not after
  Trusted Apps/the activity log, so it's seen immediately rather than
  requiring a scroll past them; takes no space at all while healthy. It
  withholds judgment for the first instant right after startup (every
  relay's own bullet next to the identity line shows yellow ◐ then,
  "still trying its first connection," not red ○) rather than flashing a
  false alarm before any relay has even had a chance to dial yet -- it
  only turns red/appears once a connection attempt has actually failed.

The footer at the bottom of the screen tracks whichever panel is
currently focused, showing only that panel's own keys (plus Switch Panel
and Background, which always work) rather than every key from every
panel at once.

```sh
ncli bunker attach              # reattach to an already-running daemon; errors clearly if none is running
ncli bunker status --json       # {"running": bool, "identity_pub", "identity_name", "identity_nip05", "vault_label", "relays", "pending_count", "session_count"}
ncli bunker stop                # shut it down
ncli bunker sessions list --json
ncli bunker sessions revoke <pubkey>              # every grant for this app, all at once
ncli bunker sessions grants <pubkey> --json       # this app's grants individually, not the summarized Grants column
ncli bunker sessions revoke-grant <pubkey> --method sign_event --kind 1   # just this one grant (omit --kind for the any-kind grant)
ncli bunker sessions rename <pubkey> <name>       # "" clears it
ncli bunker history --json      # recent resolved requests, most recent first
```

Plain-text `status`/`stop` (no `--json`) print the identity's full npub
(not the raw hex -- `id.go`'s own "npub:" precedent), then name/nip05/
vault only when actually resolved, then each configured relay with its
live connection state (`connected`/`down`) -- listed once, not also
repeated as a separate bare URL list.

**Windows**: the same TUI runs directly in the foreground with no
background/attach support -- `attach`/`status`/`stop`/`sessions`/`connect`
all require an already-running background daemon, which isn't available
there. Closing the TUI is the only way to stop it.

## Trusted Apps vs. remembered permissions

Every app that completes pairing shows up in Trusted Apps right away --
that panel tracks *which apps have connected*, not just the ones you've
granted a standing permission to. An app you've only ever answered with
one-off Approve/Reject Once still appears there, with its Grants column
showing `-`; that column fills in once you actually remember something
for it (see below). Revoking (`r`) removes the app from the list
entirely, permissions or not.

Rows sort by Last Request, most recently used app first, with apps that
have never made a request sinking to the bottom -- not by pairing order
or by whatever order `sessions list` returns.

App always shows the app's raw shortened pubkey -- a stable identifier
that never changes just because you (or the app) set a display name.
Setting your own name (`n`/`sessions rename`) is the only way to tell
apart two `bunker://` pairings, which have no self-reported name at all
and would otherwise both show up as bare, identical-looking pubkeys --
but that name shows up in the Name column (below), not App. Outside this
table, though, there's no separate name/pubkey column to split across --
so once set, that same name *does* replace the raw pubkey everywhere else
an app is identified: Pending Requests, the approval dialog, Request
History, `sessions list`/`history`'s own text output, and the daemon's
activity log. `sessions rename <pubkey> ""` clears it, reverting all of
those back to the app's self-reported name or pubkey.

Alongside App and Grants (the full detail for every remembered
permission), Trusted Apps also shows:

- **Name** -- the name you set yourself (`n`/`sessions rename`) if you
  have one, else the app's own self-reported name (and URL, in parens, if
  it gave one), e.g. `Damus (https://damus.io)`, else `-`. Self-reported
  names are only ever populated for a `nostrconnect://` pairing (the
  client generated the URI itself, with a `metadata` field baked in) -- a
  `bunker://` pairing has no equivalent at the NIP-46 protocol level, so
  it's always `-` there unless you've set your own name. Never verified,
  and captured once at pairing time, not kept fresh if the app changes
  its own metadata later.
- **Trusted Since** -- how long ago the app first paired (e.g. `3h ago`,
  `2d ago`), not when its most recent permission was granted.
- **Last Request** -- how long ago the app last actually made a request
  (from Request History, e.g. `10m ago`), or `-` if it hasn't asked for
  anything since pairing, or its most recent request has aged out of the
  200-entry history tail.
- **Kinds** -- which `sign_event` kinds it can currently sign without
  asking: `any`, a list like `1, 30023`, or `-` if it has no `sign_event`
  grant at all. Every other method's own grants still only show up in
  Grants' own full-detail column.

## Request History

`ncli bunker history` (or the TUI's own Request History panel) lists
every request this session has actually resolved -- Approved, Rejected,
Expired, or Auto-approved -- most recent first. It exists alongside
Pending Requests rather than replacing it: Pending only ever shows what's
still awaiting a decision, so a request disappears from there the moment
it's decided and reappears here instead. Each entry's Method and Kind are
their own columns (matching Pending's own shape), and Status distinguishes
a one-off Approve/Reject Once, one that also created a remembered grant
("Approved (always)"/"Rejected (always)"), and one that never needed a
decision at all because an existing grant already covered it
("Auto-approved" -- this includes every request made under a
`--grants`-pre-authorized pairing, not just the pairing handshake itself).
Durably persisted to
`events.wal` (alongside `sessions.yaml`/`daemon.log`/`bunker.sock`, under
ncli's shared bunker state directory), fsync'd on every write, and
bounded to the most recent 200 entries -- a daemon restart (or a crash)
does not lose it. A request still undecided when the daemon last stopped
running shows up after restart as **Expired** (the same status a real
5-minute Queue timeout produces) rather than staying pending forever or
vanishing without a trace -- it does not get silently re-approved or
resumed, since NIP-46 clients already resend an unanswered request on
their own.

Pressing `Enter` on any `sign_event` row opens its full event JSON
(colorized, scrollable), titled with that row's own status (e.g.
"Approved -- Signed Event") -- **Close** is focused by default so a
stray `Enter` can't copy anything by accident, with **Copy** right next
to it (the same OSC 52/native-clipboard mechanism the pairing URI's own
Copy button uses). If it was actually approved, this is the real signed
event; if it was rejected or expired, nothing was ever signed, so it
shows the original unsigned request instead (labeled accordingly) --
either way it's what the app was actually asking to sign. Rows for every
other method have nothing to show and do nothing on `Enter`.

## Remembering permissions ("don't ask every time")

For a `sign_event` request, the approval dialog shows the unsigned
event's full JSON above its buttons, so you can see exactly what you're
about to sign before deciding -- every other method still gets the plain
Approve/Reject dialog with no JSON preview (nothing to show).

Approving a request offers, beyond a one-off "Approve Once":

- **Scope**: `sign_event` gets "Always: kind N" (this exact kind only) or
  "Always: any kind"; every other method just gets "Always".
- **Duration** (a second step, after choosing "Always"): 1 hour, 24
  hours, 7 days, until revoked, or the next 10 uses.

A broad "Always: any kind" grant **never** covers kind 0 (metadata), 3
(contacts), or 5 (deletion) -- those always prompt again, even under a
wide-open grant, unless you explicitly grant that exact kind. This is
enforced in the policy engine itself, not just the UI.

Grants persist in `sessions.yaml` under ncli's app config directory and
survive a daemon restart.

Once remembered, a grant doesn't have to be all-or-nothing to undo: press
`Enter` on the app's row in Trusted Apps to open **Manage Grants**, listing
every grant it holds individually with `x` (revoke just that one) and `e`
(extend/re-scope it) -- `r` on the Trusted Apps row itself is still there
for revoking everything at once. `sessions grants <pubkey>`/`sessions
revoke-grant <pubkey> --method ... [--kind N]` are the CLI equivalents of
the same two actions.

## Pairing: `bunker://` vs `nostrconnect://`

Both are supported, matching real bunker apps (Amber, nsec.app). From
inside the TUI, press `c` -- **Show bunker:// URI** displays it on its own
selectable line plus a **Copy** button (tries OSC 52, a terminal escape
sequence most modern terminals forward to your *local* clipboard even
over SSH/tmux with nothing installed on the remote host, then falls back
to a native clipboard tool if one's available); **Enter nostrconnect://
URI** opens a text field to paste one in. Or, from another terminal /a
script:

```sh
ncli bunker connect              # generates and prints a fresh bunker:// URI -- paste this into the OTHER app
ncli bunker connect "nostrconnect://<client-pubkey>?relay=...&secret=...&metadata=..."
```

- `bunker://` (no argument): **this signer speaks second** -- it displays
  a `bunker://<pubkey>?relay=...&secret=...` URI; the connecting client
  pastes it in and sends the first `connect` request, which must present
  the matching secret (single-use, constant-time compared).
- `nostrconnect://` (a URI argument): **this signer speaks first** --
  given a URI the *other* app generated (e.g. shown as a QR code there),
  this signer sends the first `connect` request to that app's pubkey and
  waits (up to 60s) for it to echo the secret back.

Either way, a first-time `connect` from an unrecognized pubkey still goes
through the normal approval queue once the secret checks out -- knowing
the secret alone doesn't skip human approval, *unless* `--grants` (below)
staged a spec for this exact pairing attempt, which counts as that
approval already having been given in advance.

## Pre-authorizing a pairing: `connect --grants <file>`

`--grants <file>` on `connect` applies a declared set of permissions to
whichever app completes *this one pairing attempt*, the moment it pairs --
instead of every method it uses prompting interactively the first time.
Works with both directions above:

```sh
ncli bunker connect --grants examples/bunker/note-client.yaml
ncli bunker connect "nostrconnect://..." --grants examples/bunker/agent.yaml
```

The file is a `kind: bunker` YAML spec, the same `kind:`/`spec:` envelope
`ncli apply` uses for its own workflow configs:

```yaml
kind: bunker
spec:
  nickname: "My App"          # optional -- becomes this app's Trusted Apps name on pairing
  grants:
    - method: ping
    - method: get_public_key
    - method: sign_event
      kinds: [1, 6, 7]        # notes, reposts, reactions
      expires: 30d
    - method: sign_event
      kinds: any              # every kind except 0/3/5 -- see the sensitive-kinds rule above
      uses: 20
    - method: sign_event
      kinds: [3]
      verdict: deny           # block this specific kind outright, rather than leaving it to prompt
```

- `method` is the exact NIP-46 method name (`ping`, `get_public_key`,
  `get_relays`, `sign_event`, `nip04_encrypt`/`nip04_decrypt`,
  `nip44_encrypt`/`nip44_decrypt`). `connect` itself can't be granted --
  pairing is already gated by the URI's own single-use secret, not by a
  remembered grant; the spec is rejected at load time if it names one.
- `kinds` is required for `sign_event` (either a list of exact kind
  numbers, or the literal string `any`) and rejected for every other
  method, so a kind never applies where it'd be meaningless.
- `verdict` is `allow` (the default) or `deny` -- a kind-specific `deny`
  entry works the same as "Reject Always" on that exact kind, without
  ever having to see the request first.
- `expires`/`uses` are mutually exclusive and optional -- same duration
  strings as everywhere else in `ncli` (`24h`, `7d`, `2w`, ...); omitting
  both means "until revoked."
- Duplicate entries for the same method/kind are a load-time error, not a
  silent overwrite -- fix the file, don't guess which one would have won.
- Grants are resolved (`expires`'s clock starts) at the moment the app
  *actually pairs*, not when the file was loaded or `connect` was run --
  so a `bunker://` URI that sits unused for a while doesn't burn its own
  grant's clock before anyone's even connected.

`ncli bunker sessions grants <pubkey>` shows what actually landed once
paired, same as for a grant created interactively. See `examples/bunker/`
for a ready-to-copy everyday-client spec and an unattended-agent one.

## Using bunker with an AI agent

`ncli bunker` is the **signer** side of NIP-46 only -- it has no client
side. `ncli id sign`/`ncli publish`/`ncli apply` always sign with a local
vault entry or raw nsec directly; none of them speak NIP-46 as a client,
so pointing them "at" a running bunker isn't a thing -- there's no
`--bunker`/connection-string flag anywhere in `ncli` outside this
package. What bunker gives an agent is the other direction: **be** the
NIP-46 client. Any separate NIP-46-capable app -- an agent framework, a
script using a Nostr library with NIP-46 support, another `ncli bunker`
pairing to it -- can pair with a running `ncli bunker` and get events
signed without ever holding the raw private key itself.

What that buys an agent, once paired and granted:

- No raw key material in the agent's own process or config -- it only
  ever holds a pubkey and, transiently, the shared secret used during
  pairing. The nsec stays in this signer's vault/process the whole time.
- `connect --grants <file>` (see above) pre-authorizes the agent's whole
  starting scope *before it ever connects* -- no human has to be watching
  the TUI at pairing time at all, as long as the file already declares
  everything the agent needs on day one (`examples/bunker/agent.yaml` is
  a ready-to-copy starting point).
- Once granted -- interactively ("Always: kind N"/"any kind" for a
  duration or use-budget) or via `--grants` -- every matching request is
  answered with **zero human interaction** for as long as that grant
  lasts: the agent runs unattended for exactly the scope it was granted,
  no more.
- `ncli bunker sessions grants <pubkey> --json` / `sessions list --json`
  let you (or a supervising script) audit exactly what an agent app can
  currently do without a prompt, and `sessions revoke-grant`/`sessions
  revoke` pull that back if it misbehaves or its scope needs narrowing.
- `ncli bunker status --json`'s `pending_count` is the only scriptable
  signal that something is waiting on a decision `--grants` didn't
  already cover -- there's no push notification/webhook, so a supervising
  script has to poll it (or watch `history --json` for what already got
  decided).

What it can't do:

- **No headless approval of anything outside a grant.** Once a request
  actually reaches the pending queue -- because neither an existing grant
  nor a `--grants` spec covers it -- deciding it only happens through the
  interactive TUI (the approve/reject dialog, `a`/`x`, auto-prompt); there
  is no `ncli bunker pending approve`/`reject` or equivalent. `--grants`
  sidesteps this for whatever it declares in advance; it's not a general
  headless-approval mechanism for a request nobody anticipated.
- **`--grants` covers one pairing attempt, not "trust this pubkey
  forever, sight unseen."** It still requires generating (or accepting) a
  real pairing URI and the actual intended app completing that specific
  handshake -- there's no way to pre-grant a pubkey that never goes
  through pairing at all.
- **kind 0/3/5 always re-prompt**, even under a broad "any kind" grant --
  so an agent can never be silently handed standing permission to rewrite
  profile metadata, the contacts list, or issue deletions; that always
  needs an explicit, kind-specific grant, whether given interactively or
  named explicitly in a `--grants` spec.
- **Not a way to skip the vault password for `ncli`'s own commands.**
  `id sign`/`publish`/`apply` need direct key material regardless of
  whether a bunker is running -- see the first paragraph above.

To get an agent running unattended against bunker: either pair it
interactively once and approve its first request with the narrowest scope
and duration that actually covers what it needs to do (e.g. "Always: kind
1" for an agent that only posts notes, not "any kind" unless it genuinely
needs that breadth) -- that one approval is the only human step left in
the loop from then on -- or write a `--grants` spec up front and skip even
that first approval, as long as the file already covers everything the
agent will need on day one.

Not to be confused with **NIP-AA `agent_auth`** on `ncli relay` (see
README's relay section) -- that's a *relay membership* concern (an
agent key inheriting its owner's NIP-43 membership via a NIP-OA
credential), unrelated to signing.

## Gotchas learned

- `ncli bunker` reattaches to an already-running daemon if one exists,
  **ignoring** `--identity`/`--relay` for that invocation -- the same
  "attach to whatever's already there" behavior `tmux` gives a bare
  `tmux` command with a session already up. To run under a different
  identity, `ncli bunker stop` first.
- Pressing Ctrl+C in the TUI stops the daemon outright, the same as
  choosing "Stop bunker entirely"/"Stop" from `b`'s own dialog, but
  without asking first -- it does not just detach and leave a background
  daemon running unattended. Use `b` instead if you actually want to
  detach and keep it running (e.g. reattach later with
  `ncli bunker attach`).
- `ncli bunker status` never errors when nothing is running -- it's a
  clean `{"running": false}` (exit 0), so a script can poll it freely.
  `stop`/`sessions list`/`sessions revoke`/`connect` are the opposite:
  each fails with `code: "not_found"` (not a raw connection error) when no
  daemon is running, naming `ncli bunker` as the fix.
- A pending request nobody approves within 5 minutes auto-expires: the
  waiting client gets a real NIP-46 error response, not a silent drop or
  a hang.
- `sessions revoke <pubkey>` removes **every** grant for that app across
  every method/kind, all at once -- use `sessions revoke-grant <pubkey>
  --method ... [--kind N]` (or Manage Grants' own `x` in the TUI) for just
  one of them instead.
- A `--grants` spec's own `expires` clock starts when the app actually
  pairs, not when the spec was loaded or `connect` was run -- a
  `bunker://` URI generated via `connect --grants spec.yaml` (where
  `spec.yaml` declares `expires: 24h`) and left unused for a day already
  has an expired grant the instant the app finally connects. Regenerate
  the URI (a fresh `connect --grants ...`) rather than reusing an old one
  that's sat around.
