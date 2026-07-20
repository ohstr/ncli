---
name: ncli-apply
description: Author and run ncli apply workflow specs (stream, sync, or inspect kind) that tail live events, sync via NIP-77 negentropy, or read-only query across relays/local stores. Use when writing or debugging a stream.yaml/sync.yaml/inspect.yaml spec file, choosing between the three apply workflow kinds, wiring from/to targets and filters, or running `ncli apply -f <file>`.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's examples/apply/*.yaml and client/spec.go behavior
as of writing. This skill is self-contained by design and won't see repo
changes automatically — update by hand if flags/schemas change. -->

# ncli apply

`ncli apply -f <file>` runs one of three workflows, dispatched by the
spec's top-level `kind:` field. `-f`/`--file` is always required — there is
no fallback file and no `ncli prefs` integration for `apply`. `apply` does
not probe relay reachability before running — it trusts that the relays
you've configured already work; run `ncli ping` separately first if you
want to verify that (see `skills/ncli-query/SKILL.md`). One flag,
`-q/--quiet` (default `false`), takes effect only if
passed explicitly; it's a global flag (shared with every other ncli
command) that suppresses info-level narration on stderr, and `apply`
additionally maps it onto its own `Quiet` workflow option (disables
verbose UI logging — also counts as headless, see "TUI" below).

`apply` also takes `--strict-pow` (default `false`), which controls how
strictly `stream`/`sync` enforce NIP-13 proof-of-work on incoming events.
The same knob is also a spec field, `strictPow: true/false` (default
`false`), settable per-workflow inside `stream.yaml`/`sync.yaml` itself; the
CLI flag, when passed explicitly, overrides whatever the spec file says for
that run, so a spec's own `strictPow` is the "usual" setting and
`--strict-pow`/`--strict-pow=false` is a one-off override.

Only an event that actually **declares** a `nonce` tag is judged at all —
NIP-13 PoW is optional per-event, so most events carry no `nonce` tag, and
that's still perfectly valid regardless of this setting. By **default,
apply accepts every event**, including one whose `nonce` tag declares a
difficulty it doesn't actually meet. Enable `strictPow`/`--strict-pow` to
flip that: an untrusted event whose `nonce` tag doesn't hold up (or has a
malformed shape) is then rejected instead — for `stream` this counts as a
verification failure (same bucket as a bad signature, see the flow's
Failures counter); for `sync` it's silently dropped from the pulled batch
(never written to the local store), with a warning logged either way. A
`trusted: true` flow is never subject to this check, regardless of
`strictPow` — same as it already skips signature verification.

| kind | what it does | writes anything? |
|---|---|---|
| `stream` | Tails live events matching `filters` from every `from` flow to every `to` flow, until interrupted | Yes, continuously |
| `sync` | NIP-77 negentropy diff/sync between exactly one local store and one remote relay | Yes, once (converges then exits) |
| `inspect` | Read-only query across mixed relay/local-store `targets` | No |

## Flow shorthand (used by `from`/`to`/`targets` entries in all three kinds)

A flow entry is either a bare string or an object:
- Bare string: an existing local path wins the ambiguity; otherwise it's a
  remote relay. The `ws(s)://` scheme is optional on a relay — a bare host
  (`relay.primal.net`) tries `wss://` first, falling back to `ws://` only if
  that fails to connect. Writing the scheme explicitly is taken at face
  value with no fallback.
- Object: `{relay: "wss://..."}` for remote (same scheme-optional rule
  applies to `relay:`), or `{path: "./data/db.db"}` for local, plus
  `trusted: true/false` and, for local paths, `ensure: exists` (default —
  fails to load if the path is missing) or `ensure: create`.

## `stream`

```yaml
kind: stream
spec:
  from:
    - relay: "wss://relay.damus.io"
      trusted: true
    - "wss://relay.snort.social"   # shorthand form

  to:
    - relay: "wss://relay.nostr.band"
      trusted: true
    - path: "./data/relay2"
      ensure: "create"

  filters:                 # optional; omit entirely to stream everything
    - kinds: [1]
      limit: 100
    - kinds: [7]
      since: "1h"           # looks backward by default — see ncli-query

  timeouts:                 # optional, Go duration strings
    handshake: "5s"
    ping: "30s"
    pong: "60s"
    write: "60s"

  recovery:                 # optional; recovery is always on regardless
    max_retries: 5
    retry_interval: "30s"

  raw: false                # true disables the TUI, logs to stdout instead
  strictPow: false          # optional, default false -- see "--strict-pow" above
```

```sh
ncli apply -f stream.yaml
```

`recovery` persists events that failed to publish and retries them in the
background — it's always active even if this block is omitted; only its
tuning (and, via `store_path`, whether the retry store is durable/fixed vs.
the OS temp dir default) is configurable here.

## `sync`

```yaml
kind: sync
spec:
  from:
    type: local
    path: ./data/db/notes.db
    ensure: create

  to:
    relay: wss://relay.ohstr.com
    trusted: true

  direction: down            # both (default) | up | down
  maxReconcileRounds: 20      # optional, default 20
  pullBatchSize: 100          # optional, default 100
  strictPow: false            # optional, default false -- see "--strict-pow" above

  filters:                    # optional, defaults to matching everything
    - kinds: [35500]
      since: "40d"
```

```sh
ncli apply -f sync.yaml
```

**Hard rule**: across `from` + `to` combined there must be exactly one local
flow and exactly one remote flow — which list each goes in doesn't matter,
`direction` alone controls which way data moves (`up` pushes local→remote,
`down` pulls remote→local, `both` reconciles in both directions). This is
enforced at spec-load time, before anything runs.

## `inspect`

```yaml
kind: inspect
spec:
  targets:
    - wss://relay.damus.io
    - relay: wss://relay.snort.social
      trusted: true
    - path: ./data/db/notes.db

  filters:                    # optional, defaults to matching everything
    - kinds: [1]
      limit: 10
    - kinds: [7]
      since: "1d"
      "#e":
        - "0000000000000000000000000000000000000000000000000000000000000001"
```

```sh
ncli apply -f inspect.yaml
```

Never publishes or writes to a target — purely a query across `targets`,
same filter semantics as `ncli find`/`ncli dump` (see `ncli-query`). The
TUI does let you save individual matched events you select to a local
JSON file (see TUI below); that's the one place `inspect` writes anything,
and it's always local, user-initiated, and never touches a `target`.

## TUI

All three kinds open a full-screen interactive TUI by default: `Tab` to
switch panes, `r` to restart. Only `StreamSpec` has a `raw` field — set
`raw: true` to disable the TUI and just log to stdout, which is required
for `stream` under systemd/docker/CI or when driving `ncli` from another
agent, since the TUI assumes a real tty.

`stream`/`sync` show a sortable metrics table per flow (sort-by-letter on
a column, `d` removes a flow with confirmation) next to a live log pane;
`Ctrl+S` snapshots the current spec (incl. any live edits) to a new YAML
file. `inspect`'s Targets panel is the same sortable/`d`-removable
metrics table, one row per target, but with Pubkeys/Kinds columns (unique
counts) in place of Synced -- Synced is a destination-only concept (an
event the other side already had on publish) that `inspect` never
reaches, since it never publishes; Pubkeys/Kinds diversity, on the other
hand, is genuinely different per target here, unlike a stream
destination that mirrors the same merged stream as every other
destination.

`inspect`'s layout is stacked, not the side-by-side split `stream`/`sync`
use, and top-to-bottom order is: Targets (a compact strip sized to its own
target count, not a fixed share of the screen), the ambient log (matching
Targets' height), then the full-width table of individual matched events
-- last, at the bottom, deliberately: it's the panel actually read and
interacted with, so it
belongs where the eye and hands naturally rest (the same bottom-anchored
convention as a shell prompt or a chat client's message view), while
Targets/the log are glance-at status that stack above it. Full width for
Events matters too -- Content is the column actually worth reading, and a
50/50 split (the old layout) starved it exactly where a narrow terminal
needs the room most. Events are never aggregated/coalesced the way a
fast-arriving `stream` burst is, so every match gets its own row (arrow
keys move the selection):

- `Enter` opens the selected event in a large, scrollable view of its
  full JSON, pretty-printed and syntax-colored entirely by the TUI itself
  (keys purple, strings white, numbers blue, true/false/null yellow) --
  no external editor or pager, nothing to install, read-only. Arrow/Page
  Up/Page Down/Home/End scroll it. `Close` is the button selected by
  default (so a stray Enter can't save), and `Escape` always closes
  regardless of which button is currently selected.
- `Ctrl+S` saves the selected event straight to a local JSON file (same
  shape `LoadEvents`/`miner check`/`publish` already accept: a JSON array
  of events) -- works both directly from the events table and from
  inside the detail view above, without needing to navigate to the
  `Save` button first. Every save in the same session appends to the
  same file (`<spec-file>-events-<timestamp>.json`, next to the spec
  file) instead of creating a new one each time.
- `Ctrl+S` while the Targets panel (not the events table or an open
  detail view) is focused falls back to the spec-snapshot behavior
  described above for `stream`/`sync`.
- `w`/`s` are the same Wrap/Autoscroll toggle `stream`/`sync` use on their
  log, but scoped to whichever panel is currently focused (via
  `Box.SetInputCapture` on each widget, not a global `App`-level
  capture): on the ambient log it's both Wrap and Autoscroll, on the
  events table it's Autoscroll only, and toggling one never affects the
  other. There's no Wrap for the events table itself -- `tview.Table`
  cells don't wrap, so showing full content there would just widen the
  column rather than wrapping it, which wasn't useful enough to keep as a
  toggle; open the detail view instead for the full, untruncated value.
- Autoscroll on the events table keeps the row *selection* pinned to the
  newest event, not just the scroll position -- so the highlighted row
  always matches what's on screen instead of silently drifting out of
  view as new events push the viewport down. Any manual Up/Down/Page
  Up/Page Down/Home/End turns Autoscroll off first, so browsing an older
  row never gets yanked back to the tail mid-read; `s` re-enables it and
  jumps back to the newest row immediately.
- Received events are also kept in a temporary session-scoped store,
  auto-deleted when the session exits — this only backs the live event
  table/detail view and is not something you interact with directly.

**`sync` and `inspect` currently cannot run headlessly at all** — there is
no `raw` field for them, and if stdout isn't a real terminal (piped,
`--quiet`, no tty), `ncli apply` fails fast at spec-load time with `"this
workflow's kind requires an interactive terminal (TUI) and can't run
headlessly yet; rerun in a terminal, or use a stream workflow (with raw:
true) for unattended/agent use"` (`client/client.go`'s `init()`). For
scripted/CI/agent use of a one-shot local↔remote reconciliation or a
read-only multi-target query, there is currently no headless path —
either run it in a real tty, or model it as a `stream` instead if the
data flow can be expressed that way.

## Full field reference

See `references/kinds-reference.md` for the complete per-kind field table
and the shared duration-unit grammar used in `filters`.

## Gotchas learned

- `sync`'s `from`/`to` are each a single flow object, not a list — one must
  resolve to local, the other to remote (either way round). Validation
  errors, verbatim, at spec-load time (before any network activity):
  `"undefined \`from\` flow"` / `"undefined \`to\` flow"` (key missing
  entirely), `"multiple local stores defined (only one allowed for sync)"`,
  `"multiple remote relays defined (only one allowed for sync)"`,
  `"undefined local store in flows"`, `"undefined remote relay in flows"`
  (key present but wrong type), `"invalid direction: <value> (must be both,
  up, or down)"`.
- `stream` requires non-empty `from` and `to` — an empty or missing list on
  either errors at load (`"undefined \`from\` flow"` / `"undefined \`to\`
  flow"`).
- `apply` always requires `-f`; there's no `ncli prefs` fallback the way
  `dump`/`find` have one.
- For `stream`, `raw: true` is the only sane choice for scripted/agent/CI
  use — the default TUI needs a real terminal. For `sync`/`inspect` there
  is no `raw` field at all, and running either headlessly (piped stdout,
  `--quiet`, no tty) is a hard error, not a degraded/misbehaving TUI — see
  "TUI" above.
- `since`/`until` inside `filters` follow the same sign-direction rules as
  `ncli dump`/`find` filters — see `references/kinds-reference.md` for the
  full table. This bites people most often inside `stream`/`sync` filters
  because a wrong sign silently changes "the last N" into "starting N from
  now," which for a `stream` just means an empty window rather than an
  obvious error.
- `strictPow` can come from either the spec file or `--strict-pow`, and the
  flag wins over the spec's value when passed explicitly. If you're chasing
  "why did stream/sync drop this event" and it isn't a signature or
  store-level rejection, check for `pow check failed` (stream) or `dropping
  pulled event ... pow check failed` (sync) in the logs, and whether
  `strictPow`/`--strict-pow` is actually in effect for this run — without
  it, a bad nonce tag is never the cause.
