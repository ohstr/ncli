# >_ ncli

[![CI](https://github.com/ohstr/ncli/actions/workflows/ci.yml/badge.svg)](https://github.com/ohstr/ncli/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/ohstr/ncli.svg)](https://pkg.go.dev/github.com/ohstr/ncli)
[![License: Unlicense](https://img.shields.io/badge/license-Unlicense-blue.svg)](LICENSE)

**A Nostr relay server, and a command-line client toolkit for streaming,
syncing, inspecting, querying, and mining events.**

## Features

- [`ncli relay`](#relay-run-a-nostr-relay-server) — Run a fast, low-level Nostr relay server
- [`ncli relay context`](#relay-context-stop-retyping---config) — Save and switch between named relay `--config` targets
- [`ncli relay members/invites/roles`](#relay-membersinvitesroles-nip-43-membership-admin) — Manage a running relay's NIP-43 membership over NIP-98 HTTP
- [`ncli relay stats/reindex/clear`](#relay-statsreindexclear-operate-a-running-relay) — Manage a running relay over NIP-98 HTTP
- [`ncli apply`](#apply-stream-sync-inspect) — Stream, sync, or inspect events, with a live TUI and hot-reloading config
- [`ncli find`](#find-query-events) — Query events
- [`ncli ping`](#ping-test-relay-connectivity) — Check if relays/targets are reachable
- [`ncli dump`](#dump-export-events-to-json) — Export events to JSON
- [`ncli publish`](#publish-send-events-to-relays) — Publish signed events to one or more relays
- [`ncli prefs`](#prefs-default-relays-for-finddumpminerpublish) — Set default relays for `find`/`dump`/`miner check`/`publish`
- [`ncli miner`](#miner-mine-and-verify-proof-of-work) — Mine NIP-13 proof-of-work into an event, or verify it on published events
- [`ncli id`](#id-generate-or-inspect-a-nostr-identity) — Generate or inspect a Nostr keypair
- [`ncli decode`](#decode-decode-a-nip-19-entity) — Decode any NIP-19 bech32 entity (npub/nsec/note/nprofile/nevent/naddr)

`--json` and `-q/--quiet` are global flags for scripted/agent use — see
[AGENTS.md](AGENTS.md) for the full output/error contract. Run
`ncli --help` for the full command tree.

## Installation

**macOS / Linux:**

```sh
curl -fsSL https://ohstr.github.io/ncli/install.sh | sh
```

Detects your OS and CPU (amd64/arm64 — including 64-bit Raspberry Pi OS) and
installs to `/usr/local/bin` if writable, otherwise `~/.local/bin`.

**Homebrew** (macOS/Linux):

```sh
brew install ohstr/tap/ncli
```

**Windows (PowerShell):**

```powershell
irm https://ohstr.github.io/ncli/install.ps1 | iex
```

Installs to a per-user directory and updates your user `PATH` — no
Administrator prompt.

**go install**:

```sh
go install github.com/ohstr/ncli/cmd/ncli@latest
```

**Docker** — no toolchain required:

```sh
# :latest tracks the newest release, :edge tracks main — see Docker section below.
docker run --rm ghcr.io/ohstr/ncli:latest --help
```

**From source** — see [Development](#development).

## `relay`: run a Nostr relay server

<details>
<summary>Supported NIPs</summary>

| NIP | Description |
|---|---|
| [01](https://github.com/nostr-protocol/nips/blob/master/01.md) | Core event, filter, and subscription types |
| [05](https://github.com/nostr-protocol/nips/blob/master/05.md) | NIP-05 identity verification |
| [09](https://github.com/nostr-protocol/nips/blob/master/09.md) | Event deletion |
| [11](https://github.com/nostr-protocol/nips/blob/master/11.md) | Relay information document |
| [13](https://github.com/nostr-protocol/nips/blob/master/13.md) | Proof of work |
| [16](https://github.com/nostr-protocol/nips/blob/master/16.md) | Event treatment (regular/replaceable/ephemeral kinds) |
| [19](https://github.com/nostr-protocol/nips/blob/master/19.md) | Bech32-encoded entities: npub, nsec, note, nprofile, nevent, naddr |
| [26](https://github.com/nostr-protocol/nips/blob/master/26.md) | Event delegation |
| [33](https://github.com/nostr-protocol/nips/blob/master/33.md) | Parameterized replaceable events |
| [40](https://github.com/nostr-protocol/nips/blob/master/40.md) | Event expiration |
| [42](https://github.com/nostr-protocol/nips/blob/master/42.md) | Relay authentication |
| [43](https://github.com/nostr-protocol/nips/blob/master/43.md) | Relay membership — off by default; see [`relay members`](#relay-membersinvitesroles-nip-43-membership-admin) |
| [44](https://github.com/nostr-protocol/nips/blob/master/44.md) | Versioned encryption |
| [49](https://github.com/nostr-protocol/nips/blob/master/49.md) | Encrypted private key storage |
| [50](https://github.com/nostr-protocol/nips/blob/master/50.md) | Search — people search, not note content; see below |
| [57](https://github.com/nostr-protocol/nips/blob/master/57.md) | Lightning zaps |
| [65](https://github.com/nostr-protocol/nips/blob/master/65.md) | Relay list metadata |
| [77](https://github.com/nostr-protocol/nips/blob/master/77.md) | Negentropy sync |
| [98](https://github.com/nostr-protocol/nips/blob/master/98.md) | HTTP authentication |
| [AA](https://github.com/block/buzz/blob/main/docs/nips/NIP-AA.md) | Agent auth — requires NIP-43 membership; see `agent_auth` below |
| [OA](https://github.com/block/buzz/blob/main/docs/nips/NIP-OA.md) | Owner attestation — verified as part of NIP-AA |

</details>

**Run a relay** — every field `RelayConfig` understands, at its default
value (see [`examples/relay/full.yaml`](examples/relay/full.yaml) itself
for the commented version explaining each one):

```yaml
# examples/relay/full.yaml
port: 5500

nip11:
  name: ncli-dev-relay
  pubkey: 3c1db3dd55e2ff09ba5317dd8eec2339797e9e2ddf74591172735c47f3a2ad6e
  contact: admin@example.com
  description: "A community relay"
  limitation:
    auth_required: false
    membership_required: false
    max_limit: 555555
    max_message_length: 1101005
    max_subscriptions: 355
    max_indexable_tags: 5

store: ./data/db/notes.db
logdir: ./data/logs

pow:
  strict: false
  min: 0

cache:
  topZapped:
    enabled: false
    window: 24h
  search:
    enabled: true
    readonly: false
    host: http://localhost:7700
    key: masterKey
    index_name: ncli_events
    batch_size: 100
    max_ch_size: 1000

membership:
  enabled: false
  inviteTTL: 24h
  inviteMaxUses: 1
  publishAddRemoveEvents: false

agent_auth:
  enabled: false
  freshnessWindow: 120s
  kindEnforcement: false

handshakeTimeout: 5s
pingInterval: 30s
pongTimeout: 60s
writeTimeout: 60s

outgoingBufferSize: 1024
maxConcurrentStoreTasks: 2048
verificationWorkers: 50

logs:
  filename: ./data/logs/nrelay.log
  maxSize: 100
  maxBackups: 3
  maxAge: 28
  compress: true
```

```sh
ncli relay --config examples/relay/full.yaml
```

Only `store` and a `pubkey` (or a `privkey`, to derive one) are actually
required — `logdir` defaults to the current directory, and everything else
defaults too, down to this minimal config:

```yaml
# examples/relay/minimal.yaml
port: 5500

nip11:
  name: ncli-dev-relay
  pubkey: 9b14dff44fb8d74e2b90d5ae1501b935e073a5245749458a3e261021646f4e11
  contact: admin@example.com

store: ./data/db/notes.db
logdir: ./data/logs
```

```sh
ncli relay --config examples/relay/minimal.yaml
```

Once it's running, operate it with NIP-98-signed admin commands — see
[`relay stats`/`reindex`/`clear`](#relay-statsreindexclear-operate-a-running-relay)
below for the full set:

```sh
# live reindexer + verification-worker metrics
ncli relay stats --config examples/relay/full.yaml
```

With search enabled, `--search` on `find`/`dump` is a **people** search
(NIP-50), not event content.

```sh
# kind-1 notes from profiles matching "jack"
ncli find --search "jack" -k 1 -l 5 -s localhost:5500
```

Require NIP-13 proof-of-work on published events with a `pow:` block —
`min: 0` (the default) accepts everything; a non-zero `min` is advertised to
clients via NIP-11 either way, and `strict: true` is what actually turns on
rejecting events that fall short:

```yaml
pow:
  strict: false # true rejects under-difficulty events; false just advertises min
  min: 20       # required leading-zero-bit difficulty; 0 = no requirement
```

See [`examples/relay/pow.yaml`](examples/relay/pow.yaml) for a relay with
this actually enforced (`strict: true`). More presets (auth-required,
membership, ephemeral, cache+search) live under
[`examples/relay/`](examples/relay/).

## `relay context`: stop retyping `--config`

Managing more than one relay means retyping `--config path/to/relay.yaml`
on every `stats`/`members`/`invites`/... call. Save each relay's config
under a short name instead, and switch between them:

```sh
ncli relay context add bee_community ~/relays/bee-community.yaml
ncli relay context add outbox ~/relays/outbox.yaml
ncli relay context use bee_community

# targets bee_community, no --config needed
ncli relay stats
# same
ncli relay members list

# list saved contexts, "*" marks the current one
ncli relay context
ncli relay context remove outbox
```

`--config`, if given, always wins. Otherwise a `ncli.yaml`/`relay.yaml` in
the current directory still takes priority (unchanged from before contexts
existed); the current context is consulted only when neither is present.

## `relay members`/`invites`/`roles`: NIP-43 membership admin

Same NIP-98-signed mechanism as `relay stats`/`reindex`/`clear` below,
gated on `membership.enabled: true`. Enroll members, hand out invite
codes, and define roles on a running relay:

```sh
# enroll a pubkey directly -- no invite code needed
ncli relay members add <pubkey> --role vip --config examples/relay/membership.yaml

# who's currently enrolled
ncli relay members list --config examples/relay/membership.yaml

# issue a code to hand out out-of-band (a signup email, a Discord invite)
ncli relay invites create --ttl 24h --max-uses 1 --config examples/relay/membership.yaml

# define a role
ncli relay roles create vip --label "VIP" --color 280 --config examples/relay/membership.yaml
```

NIP-43 has no "delete role" event, so `roles create` re-run with the same
`id` supersedes it rather than a separate `roles delete`.

**NIP-AA agent auth** (optional, on top of membership) lets an agent key
gain virtual membership from its owner's NIP-43 membership by presenting a
valid NIP-OA "auth" tag credential during AUTH, without separately
enrolling the agent key itself:

```yaml
# requires nip11.limitation.membership_required (which itself requires
# membership.enabled and limitation.auth_required)
agent_auth:
  enabled: true
  freshnessWindow: "120s"
  kindEnforcement: false
```

See the commented-out block in
[`examples/relay/membership.yaml`](examples/relay/membership.yaml) or the
fully-documented one in
[`examples/relay/full.yaml`](examples/relay/full.yaml).

## `relay stats`/`reindex`/`clear`: operate a running relay

NIP-98-signed HTTP requests to an already-running relay, using the same
`--config` as the server:

```sh
# live reindexer + verification-worker metrics
ncli relay stats --config examples/relay/full.yaml

# trigger a live profile-search reindex (async, kind 0 only)
ncli relay reindex search --config examples/relay/full.yaml

# trigger a live zap-stats reindex
ncli relay reindex zaps --config examples/relay/full.yaml

# delete the profile search index on the live relay
ncli relay clear search --config examples/relay/full.yaml

# delete zap counters on the live relay
ncli relay clear zaps --config examples/relay/full.yaml
```

Every subcommand takes `--json`; requires `nip11.privkey` in `--config`.
Poll `ncli relay stats` for `reindex` progress.

## `prefs`: default relays for `find`/`ping`/`dump`/`miner`/`publish`

A default relay list — `find`, `ping`, `dump`, `miner check`, and
`publish` all use it automatically whenever you don't pass your own
relays.

```sh
ncli prefs relays add relay.damus.io
ncli prefs relays list

# no -s -- falls back to the prefs relay list above
ncli find jack@primal.net -k 1 -l 2

ncli prefs relays remove relay.damus.io
ncli prefs relays clear   # remove all of them

# print where prefs.yaml lives
ncli prefs path
```

![`ncli prefs relays add`, then `ncli find` with no -s at all](docs/vhs/prefs.gif)

## `find`: query events

Looks up an event by ID and/or filter, stopping at the **first** matching
target (`dump` merges across every target instead). The identifier can be
an event (hex ID, `note1...`/`nevent1...`) or an author (`npub1...`,
`nprofile1...`, nip-05 `name@domain`) — author-only defaults to their
profile (kind 0).

```sh
# nip-05 address -> their profile (kind 0)
ncli find jack@primal.net -s relay.ohstr.com

# their 5 most recent notes
ncli find jack@primal.net -k 1 -l 5 -s relay.ohstr.com

# npub/nprofile/note1/nevent1 all work the same way
ncli find npub1...

# look up one event by ID, targets from a file
ncli find <event-id> -t examples/targets.yaml

# multiple relays, first match wins
ncli find --kinds 1 --limit 5 -s relay.damus.io,relay.primal.net
```

![`ncli find` resolving a nip-05 address and its recent notes](docs/vhs/find.gif)

More filter flags (`-a/--authors`, `-i/--ids`, `--since`/`--until`/
`--search`/`--tag`) live behind `ncli find --help`.

A `--targets` file isn't just a relay list — it can carry `filters` too, so
a single YAML file replaces both `-s` and the inline filter flags at once
(an author given as the positional identifier, like below, still ANDs in
on top of the file's own filters):

```sh
ncli find jack@primal.net -t examples/targets.yaml
```

```yaml
# examples/targets.yaml
kind: targets
spec:
  relays:
    - wss://relay.damus.io
    - relay: wss://relay.snort.social
      trusted: true  # skip re-verifying this source's signatures (default false)
    - path: ./data/db/notes.db
      ensure: create # create if missing (default "exists", errors if missing)

  # filters (optional, omit entirely to match everything): same NIP-01
  # fields as examples/filters.yaml. Multiple filters are OR'd together;
  # within one filter every field set must match (AND).
  filters:
    - kinds: [1]
      limit: 5
```

## `ping`: test relay connectivity

Confirms a relay actually speaks the protocol, not just accepts a
connection. Fetches no events, and exits non-zero if any relay is
unreachable.

Results narrate as plain log lines by default. Pass `--tui` for a live
interactive board instead -- only takes effect in a real terminal and
without `--json`/`--quiet`, falling back to plain narration otherwise.

```sh
# relays are plain arguments, no flag needed -- scheme optional too
ncli ping relay.damus.io relay.snort.social

# relays from a file (same shape as find/dump's --targets)
ncli ping -t examples/targets.yaml

# every `ncli prefs` relay
ncli ping

# structured report on stdout, for scripting
ncli ping relay.damus.io --json

# live interactive board instead of plain log lines
ncli ping relay.damus.io relay.snort.social --tui
```

![`ncli ping` checking two relays' reachability](docs/vhs/ping.gif)

## `dump`: export events to JSON

Same targets/filters as `find`, but merges results across **every**
target instead of stopping at the first match.

```sh
# export a relay's events to JSON
ncli dump -s relay.ohstr.com -o events.json

# merge a relay + a local store
ncli dump -s relay.damus.io,./data/db/notes.db -o out.json

# recent notes, every `ncli prefs` relay
ncli dump -k 1 --since 24h -o recent.json

# relays + filters from one YAML file
ncli dump -t examples/targets.yaml -o out.json
```

![`ncli dump` exporting events to JSON](docs/vhs/dump.gif)

`-o/--out` is required (`.json` or `.jsonp`).

## `publish`: send events to relays

The write-side counterpart to `dump`: sends signed events to relays and
reports each one's `OK`.

```sh
# mine + auto-sign, then publish
ncli miner mine --content "hello from ncli" --identity mykey -d 24 -o signed.json
ncli publish -e signed.json -s wss://relay.damus.io

# publish several events to several relays at once
ncli publish -e events.json -s relay.damus.io,relay.snort.social --json
```

Exits non-zero if any (event, relay) pair fails — the same
composes-into-CI/scripts convention as `miner check`.

## `apply`: stream, sync, inspect

`ncli apply -f <file>` runs one of three workflows, chosen by the file's
`kind`. Fully annotated versions of each live under
[`examples/apply/`](examples/apply/) — the snippets below are trimmed to
the essentials.

`--strict-pow` is a flag on `apply` (default `false`) that applies to both
`stream` and `sync`: since NIP-13 proof-of-work is optional per event, an
untrusted event with a `nonce` tag that doesn't meet its declared
difficulty is accepted by default, same as one with no `nonce` tag at all.
Enable strict checking either via the flag or the spec's own `strictPow:
true` field (both kinds support it) to reject it instead — the flag, when
passed explicitly, overrides whatever the spec file says. A `trusted: true`
flow is never subject to this check either way.

**`stream`** — tail events live, forwarding everything matching `filters`
from every `from` flow to every `to` flow until interrupted (any number of
sources and destinations, mixed relays/stores):

```yaml
kind: stream
spec:
  from:
    - relay: "wss://relay.damus.io"
      trusted: true  # skip re-verifying this source's signatures (default false)
    - wss://relay.snort.social      # bare-string shorthand for a remote relay also works
  to:
    - path: "./data/mirror.db"
      ensure: create
  filters:
    - kinds: [1]
```

```sh
ncli apply -f examples/apply/stream.yaml
```

**`sync`** — NIP-77 negentropy reconciliation between exactly one local
store and one remote relay (`direction: both|up|down`):

```yaml
kind: sync
spec:
  from:
    type: local
    path: "./data/db/notes.db"
    ensure: create
  to:
    relay: "wss://relay.ohstr.com"
    trusted: true  # skip re-verifying this source's signatures (default false)
  direction: both
```

```sh
ncli apply -f examples/apply/sync.yaml
```

**`inspect`** — read-only query across any number of targets, any mix of
relays/stores; never publishes or writes to a target:

```yaml
kind: inspect
spec:
  targets:
    - "wss://relay.damus.io"
    - "wss://relay.snort.social"
    - path: "./data/db/notes.db"
      ensure: create
  filters:
    - kinds: [1]
      limit: 10
```

```sh
ncli apply -f examples/apply/inspect.yaml
```

### TUI

`ncli apply` opens a full-screen terminal dashboard by default — a live,
sortable table of per-flow metrics (events, errors, throughput) next to a
live log pane, for `stream`/`sync`. Some keys:

- `Tab` / `Shift+Tab` — move focus between panels
- a column's highlighted letter — sort by that column
- `d` — remove a flow from the running stream/sync (with confirmation)
- `Ctrl+S` — snapshot the current spec (incl. any live edits) to a new YAML file
- `r` — restart; editing and saving the spec file on disk also prompts a reload

![`ncli apply` streaming live events into the TUI dashboard](docs/vhs/apply-stream.gif)

![`ncli apply` reconciling a local store against a relay (sync)](docs/vhs/apply-sync.gif)

`inspect` uses a different layout: a compact Targets strip, the ambient
log, then a full-width table of matched events anchored at the bottom.
Some keys:

- `Enter` — open the selected event in a scrollable, read-only JSON detail view
- `Ctrl+S` — save the selected event to a local JSON file, from the table
  or the detail view; repeated saves in a session append to the same file
  (`<spec-file>-events-<timestamp>.json`)
- `w` / `s` — Wrap/Autoscroll toggle for whichever panel is focused (the
  events table only has Autoscroll — its cells are always single-line, so
  open the detail view for the full value)
- any arrow/Page/Home/End move pins the selection and turns off
  Autoscroll; press `s` to resume following the tail

Add `raw: true` to a `stream` spec to skip the TUI and just log to stdout —
useful when piping output or running under a process supervisor.

![`ncli apply` inspecting matched events across relays and a local store](docs/vhs/apply-inspect.gif)

## `miner`: mine and verify proof-of-work

`ncli miner mine` finds a NIP-13 proof-of-work nonce for an event, searching
across every CPU core by default. Author the note inline with
`--content`/`--content-file` (fills in `created_at`/`kind` for you) plus
`--identity` for the pubkey — no hand-written event file needed:

```sh
ncli miner mine --content "hello from ncli" --tag t=nostr --identity mykey -d 24 -o mined.json
```

```
2026-07-08T23:31:27Z INF mining... 1,804,600 hashes tried, 300ms elapsed, 6.01 MH/s across 8 worker(s)
2026-07-08T23:31:27Z INF mining... 2,843,604 hashes tried, 500ms elapsed, 6.31 MH/s across 8 worker(s)
2026-07-08T23:31:27Z INF id: 000000530da8dc65159359f6ef14118d6a82ee2d337660f3ad8fbc60e57b5073
2026-07-08T23:31:27Z INF nonce: 3641608
2026-07-08T23:31:27Z INF difficulty: 24
```

![`ncli miner mine` finding a proof-of-work nonce](docs/vhs/miner.gif)

If `--identity` resolves to a private key, the mined event is **signed
automatically** — ready for `ncli publish` as-is. A pubkey-only identity
mines but can't sign.

For anything `--content`/`--content-file` can't express, pass a
structured event file instead:

```yaml
# examples/event.yaml
pubkey: 3c1db3dd55e2ff09ba5317dd8eec2339797e9e2ddf74591172735c47f3a2ad6e
created_at: 1719759720 # <-- replace with the current unix time, e.g. `date +%s`
kind: 1
tags:
  - ["t", "nostr"]
content: "hello from ncli"
```

```sh
ncli miner mine -e examples/event.yaml -o mined.json -d 24
```

It never overwrites `-e`'s file unless you pass `--in-place`. `--json`
reports the mined result as structured JSON.

`ncli miner check` verifies PoW on already-mined events — from a file:

```sh
ncli miner check -e events.json
```

or fetched live (same filters as above), optionally narrowed to one
identity's own events — a one-liner PoW compliance audit:

```sh
ncli miner check -s relay.damus.io --identity mykey --kinds 1 --since 7d --json
```

It exits non-zero the moment any checked event fails, so it composes
directly into CI/cron:

```sh
ncli miner check -e events.json || alert-oncall "PoW compliance regression"
```

## `id`: generate or inspect a Nostr identity

No argument generates a new keypair (hex, nsec, npub); an argument
resolves/inspects an existing one.

```sh
# generate an identity, save it to the local vault
ncli id --save --label mykey

# inspect an identity by npub
ncli id npub1...

# ...or a saved vault label, with privkey/nsec
ncli id mykey --reveal

# list every saved vault identity
ncli id list --reveal

# non-interactive, vault password from NCLI_VAULT_PASSWORD
ncli id --json --save --label mykey
```

The vault is encrypted under a password. `ncli id delegate` mints a
NIP-26 delegation token from an identity.

![`ncli id` generating a keypair, then inspecting a nip-05 address](docs/vhs/id.gif)

## `decode`: decode a NIP-19 entity

```sh
# npub/nsec/note/nprofile/nevent/naddr -> hex fields
# (+ relay hints/kind if embedded)
ncli decode npub1...

# same, as structured JSON
ncli decode npub1... --json
```

![`ncli decode` resolving a npub to its hex pubkey](docs/vhs/decode.gif)


## Agent skills

For coding agents: [`AGENTS.md`](AGENTS.md) points to the matching skill
under [`skills/`](skills/) (one per command group, usable standalone with
just the `ncli` binary on `PATH`).

```
# any agentskills.io-compatible agent
npx skills add ohstr/ncli --all -y

# Claude Code only
/plugin marketplace add ohstr/ncli

# ...one command group at a time
/plugin install ncli-apply@ncli
```

## Configuration

Config is loaded via [viper](https://github.com/spf13/viper) from a YAML
file or `NCLI_`-prefixed environment variables. Without `--config`, ncli
looks for `ncli.yaml`/`relay.yaml` in the current directory, then the
current [`relay context`](#relay-context-stop-retyping---config) (if any),
then `$HOME`. Every YAML input `ncli` accepts has a documented sample
under [`examples/`](examples/).

## Docker

Runs the published `ghcr.io/ohstr/ncli` image alongside Meilisearch via
[`build/relay/docker-compose.prod.yaml`](build/relay/docker-compose.prod.yaml).
Meilisearch's port isn't exposed to the host and there's no default master
key — set one yourself:

```sh
cp build/relay/.env.example build/relay/.env
# edit build/relay/.env: set MEILI_MASTER_KEY, optionally RELAY_CONFIG

docker compose -f build/relay/docker-compose.prod.yaml --env-file build/relay/.env up -d
```

`build/relay/.env` is gitignored — never commit it.

## Development

This project uses [`just`](https://github.com/casey/just) as its task runner:

```sh
# go build -o bin/ncli ./cmd/ncli
just build

# go test -short -race ./... (skips live-relay tests)
just test

# vet + test
just check

# run the stream pipeline benchmarks
just bench

# run the relay server against examples/relay/minimal.yaml
just dev relay

# run the relay + Meilisearch in Docker
just dev up

# stop the Docker dev stack
just dev down

# regenerate this README's demo GIFs (needs vhs/ttyd/ffmpeg on PATH)
just vhs
```

> Building from source needs one extra one-time setup step — see
> [CONTRIBUTING.md](CONTRIBUTING.md#getting-set-up).

Releases are cut manually via the `workflow_dispatch`-only
[`.github/workflows/release.yml`](.github/workflows/release.yml).

## License

[Unlicense](LICENSE) — public domain.
