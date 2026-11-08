# <picture><source media="(prefers-color-scheme: dark)" srcset="docs/brand/ncli-logo-on-dark.svg"><img alt="ncli" src="docs/brand/ncli-logo-on-light.svg" width="300"></picture>

[![Release](https://img.shields.io/github/v/release/ohstr/ncli)](https://github.com/ohstr/ncli/releases/latest)
[![CI](https://github.com/ohstr/ncli/actions/workflows/ci.yml/badge.svg)](https://github.com/ohstr/ncli/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/ohstr/ncli.svg)](https://pkg.go.dev/github.com/ohstr/ncli)
[![License: Unlicense](https://img.shields.io/badge/license-Unlicense-blue.svg)](LICENSE)

**Built for humans and agents, a Nostr relay, and a command-line
client toolkit for streaming, syncing, inspecting, querying, and mining
events.**

## Features

- [`ncli relay`](#relay) — Run your own Nostr relay:
  - Community — [`examples/relay/open-community-relay.yaml`](examples/relay/open-community-relay.yaml)
  - Media server (Blossom) — [`examples/relay/blossom-relay.yaml`](examples/relay/blossom-relay.yaml)
  - Public search relay (indexer) — [`examples/relay/public-search-relay.yaml`](examples/relay/public-search-relay.yaml)
  - Personal outbox — [`examples/relay/personal-relay.yaml`](examples/relay/personal-relay.yaml)
  - Voice and spaces (huddle) — [`examples/relay/community-voice-relay.yaml`](examples/relay/community-voice-relay.yaml)
- [`ncli apply`](#apply) — Stream, sync, or inspect events, with a live TUI and hot-reloading config
- [`ncli find`](#find) — Query events
- [`ncli profile`](#profile) — Look up one identity as a readable card
- [`ncli event`](#event) — Verify, fetch, show, rebroadcast or unwrap any event, no identity needed
- [`ncli ping`](#ping) — Check if relays/targets are reachable
- [`ncli dump`](#dump) — Export events to JSON
- [`ncli publish`](#publish) — Publish signed events to one or more relays
- [`ncli prefs`](#prefs) — Set default relays for `find`/`dump`/`miner check`/`publish`
- [`ncli miner`](#miner) — Mine NIP-13 proof-of-work into an event, or verify it on published events
- [`ncli bunker`](#bunker) — Run as a NIP-46 remote signer
- [`ncli signer`](#signer) — Sign for local processes over a unix socket, gated by a policy
- [`ncli space`](#space) — Create, find, and join NIP-53 meeting spaces, with voice and chat
- [`ncli huddle`](#huddle) — List the voice rooms a relay is hosting
- [`ncli groups`](#groups) — Create and run NIP-29 relay-hosted groups
- [`ncli blossom`](#blossom) — Upload, fetch, and manage content on Blossom media servers
- [`ncli id`](#id) — Generate or inspect a Nostr keypair
- [`ncli decode`](#decode) — Decode any NIP-19 bech32 entity (npub/nsec/note/nprofile/nevent/naddr)

`--json` and `-q/--quiet` are global flags for scripted/agent use — see
[AGENTS.md](AGENTS.md) for the full output/error contract. Run
`ncli --help` for the full command tree.

## Installation

**Agents:**

```
Fetch https://ohstr.github.io/ncli/PROMPT.md
```

See [Agent skills](#agent-skills).

**macOS / Linux:**

```sh
curl -fsSL https://ohstr.github.io/ncli/install.sh | sh
```

Detects OS and CPU (amd64/arm64, including 64-bit Raspberry Pi OS); installs
to `/usr/local/bin` if writable, else `~/.local/bin`.

**Homebrew** (macOS/Linux):

```sh
brew install ohstr/tap/ncli
```

**Windows (PowerShell):**

```powershell
irm https://ohstr.github.io/ncli/install.ps1 | iex
```

Installs per user and updates your user `PATH`; no Administrator prompt.

**go install**:

```sh
go install github.com/ohstr/ncli/cmd/ncli@latest
```

**Docker**:

```sh
# :latest tracks the newest release, :edge tracks main — see Docker section below.
docker run --rm ghcr.io/ohstr/ncli:latest --help
```

**From source** — see [Development](#development).

Show the version and where ncli keeps its files:

```sh
ncli version
```

## `relay`

Run a Nostr relay server.

<details>
<summary>Supported NIPs</summary>

| NIP | Description |
|---|---|
| [01](https://github.com/nostr-protocol/nips/blob/master/01.md) | Basic protocol flow description |
| [05](https://github.com/nostr-protocol/nips/blob/master/05.md) | Mapping Nostr keys to DNS-based internet identifiers |
| [09](https://github.com/nostr-protocol/nips/blob/master/09.md) | Event Deletion Request |
| [11](https://github.com/nostr-protocol/nips/blob/master/11.md) | Relay Information Document |
| [13](https://github.com/nostr-protocol/nips/blob/master/13.md) | Proof of Work |
| [16](https://github.com/nostr-protocol/nips/blob/master/16.md) | Event Treatment |
| [19](https://github.com/nostr-protocol/nips/blob/master/19.md) | bech32-encoded entities |
| [26](https://github.com/nostr-protocol/nips/blob/master/26.md) | Delegated Event Signing |
| [29](https://github.com/nostr-protocol/nips/blob/master/29.md) | Relay-based Groups |
| [33](https://github.com/nostr-protocol/nips/blob/master/33.md) | Parameterized Replaceable Events |
| [40](https://github.com/nostr-protocol/nips/blob/master/40.md) | Expiration Timestamp |
| [42](https://github.com/nostr-protocol/nips/blob/master/42.md) | Authentication of clients to relays |
| [43](https://github.com/nostr-protocol/nips/blob/master/43.md) | Relay Access Metadata and Requests — off by default; see [`relay members`](#relay-membersinvitesroles) |
| [44](https://github.com/nostr-protocol/nips/blob/master/44.md) | Encrypted Payloads (Versioned) |
| [47](https://github.com/nostr-protocol/nips/blob/master/47.md) | Nostr Wallet Connect |
| [48](https://github.com/nostr-protocol/nips/blob/master/48.md) | Bridged Events |
| [49](https://github.com/nostr-protocol/nips/blob/master/49.md) | Private Key Encryption (`ncryptsec`) |
| [50](https://github.com/nostr-protocol/nips/blob/master/50.md) | Search Capability — people search, not note content; see below |
| [53](https://github.com/nostr-protocol/nips/blob/master/53.md) | Live Streaming and Spaces |
| [57](https://github.com/nostr-protocol/nips/blob/master/57.md) | Lightning Zaps |
| [65](https://github.com/nostr-protocol/nips/blob/master/65.md) | Relay List Metadata |
| [71](https://github.com/nostr-protocol/nips/blob/master/71.md) | Video Events |
| [77](https://github.com/nostr-protocol/nips/blob/master/77.md) | Negentropy Syncing |
| [88](https://github.com/nostr-protocol/nips/blob/master/88.md) | Polls |
| [90](https://github.com/nostr-protocol/nips/blob/master/90.md) | Data Vending Machines |
| [98](https://github.com/nostr-protocol/nips/blob/master/98.md) | HTTP Auth |
| [A0](https://github.com/nostr-protocol/nips/blob/master/A0.md) | Voice Messages |
| [AA](https://github.com/block/buzz/blob/main/docs/nips/NIP-AA.md) | Agent auth — requires relay membership; see `agent_auth` below |
| [AZ](https://github.com/ohstr/nips/blob/main/NIP-AZ.md) | AltZap — zaps for energy-backed coins |
| [B0](https://github.com/nostr-protocol/nips/blob/master/B0.md) | Web Bookmarks |
| [B7](https://github.com/nostr-protocol/nips/blob/master/B7.md) | Blossom |
| [OA](https://github.com/block/buzz/blob/main/docs/nips/NIP-OA.md) | Owner attestation — verified as part of agent auth |

</details>

**Quickest start**:

```sh
ncli relay -c bee_community
```

Creates a minimal config ([`examples/relay/minimal.yaml`](examples/relay/minimal.yaml))
with a fresh identity and starts serving; see [`relay context`](#relay-context).

For full control, pass your own config with `--config`. Every field, at its
default, commented: [`examples/relay/full.yaml`](examples/relay/full.yaml)

```yaml
# examples/relay/full.yaml
port: 5500                 # every interface
# listen: 127.0.0.1:5500   # one address instead; wins over port

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
  profiles:
    search:
      enabled: true
      readonly: false
      meilisearch: {host: http://localhost:7700, key: masterKey, index: ncli_events}
      # profiles per request, and requests Meilisearch may merge
      batchSize: 10000
      maxInFlight: 4
    # NIP-85 user assertions (kind 30382); needs nip11.privkey
    stats:
      enabled: false
      maxTargets: 500
    # when NIP-05/lightning addresses are re-checked; never on a rebuild
    checks:
      ttl: 14d
      perHostInterval: 250ms
      workers: 50
      retry: {initial: 1h, max: 24h, maxAttempts: 5}
    # profile score weights, overriding the built-in ones
    scoring:
      social:
        followers_weight: 20
  # signed zap ranking; the store keeps a zap index only while it's on
  zaps:
    enabled: false
    windows: [24h, 7d, 30d, all]
    coins: [sats, loki]
  # the relay's own NIP-66 kind 30166; needs nip11.privkey and nip11.url
  relay:
    stats:
      enabled: false
      interval: 1m

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

logs:
  level: info
  filename: ./data/logs/nrelay.log
  maxSize: 100
  maxBackups: 3
  maxAge: 28
  compress: true
```

```sh
ncli relay --config examples/relay/full.yaml
```

Only `store` and a `pubkey` (or a `privkey` to derive one) are required;
`logdir` defaults to the current directory and the rest has defaults:

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

A key the relay doesn't know, such as a typo, is logged as a warning and
ignored. `--check` fails on it, naming every such key:

```sh
ncli relay --config relay.yaml --check
```

Once running, operate it from its admin listener (see
[relay monitoring](#relay-monitoring)):

```sh
# health and live metrics
ncli relay status --config examples/relay/minimal.yaml
```

With search enabled, `--search` on `find`/`dump` searches **people**
(NIP-50), not event content.

```sh
# kind-1 notes from profiles matching "jack"
ncli find --search "jack" -k 1 -l 5 -s localhost:5500
```

Require NIP-13 proof-of-work with a `pow:` block. `min: 0` (default)
accepts everything. A non-zero `min` is advertised in NIP-11; only
`strict: true` rejects events, both under-difficulty ones and ones whose
`nonce` tag is false:

```yaml
pow:
  # true rejects under-difficulty and self-contradictory nonce tags
  strict: false
  # required leading-zero-bit difficulty; 0 = no requirement
  min: 20
```

Enforced example (`strict: true`):
[`examples/relay/anti-spam-relay.yaml`](examples/relay/anti-spam-relay.yaml).

Host group voice calls (plus video and screen sharing for browsers) with a
`huddle:` block. Calls use their own WebSocket endpoint, next to the Nostr
socket:

```yaml
nip11:
  # required: a joining client's auth event names it
  url: "wss://relay.example"
huddle:
  enabled: true
  # also serve browsers, which is what carries video and screen sharing
  rtc: true
```

Preset: [`examples/relay/community-voice-relay.yaml`](examples/relay/community-voice-relay.yaml);
details in [`ncli huddle`](#huddle).

Host NIP-29 groups with your own roles, and moderate the relay from any
NIP-86 client:

```yaml
groups:
  roles:
    admin: [9000, 9001, 9002, 9005, 9008, 9009, 9010]
    moderator: [9005, 9010]
  roleDescriptions:
    moderator: keeps the peace
  admins: [npub1...]   # moderate every group, like the relay's own key
  moderationLog: true  # members can read who did what
  pendingJoins: true   # closed groups keep codeless joins for an admin
nip86:
  enabled: true
  admins: [<hex pubkey>]
```

NIP-86 bans pubkeys and events, allows or disallows kinds, blocks IPs and
renames the relay (name, description, icon); saved in
`<store>.moderation.json`.

More config blocks, each documented in
[`skills/ncli-relay-ops`](skills/ncli-relay-ops/SKILL.md) and
[`examples/relay/full.yaml`](examples/relay/full.yaml):

- `recipientOnlyKinds` — gift wraps (kind 1059) go only to the pubkey in their `p` tag; default `[1059]`, `[]` turns it off.
- `httpBridge.count` — `POST /count` (NIP-45), same rules as `/query`.
- `groups.membershipNotices` — a relay-signed notice (kinds 44100/44101) to a member who gains or loses a seat in a group.
- `membership.roles` — what each NIP-43 role may do, e.g. `greeter: {can: [createclaim]}`; `groups.create` says who may create a group.
- `presence` — `POST /query` for kind 40902 answers who is online.
- `blossom` — Blossom uploads and downloads on the relay's own origin, stored in a directory or S3/MinIO.
- `limits` — opt-in rate limits per pubkey and per IP (EVENT, REQ/COUNT, the HTTP bridge, Blossom) and connections per IP. Unset means no limit; a public relay without any gets a warning.

### Example configs

Every file under [`examples/relay/`](examples/relay/) is a working, fully
commented config. Start from the closest one:

| File | For |
|---|---|
| [`minimal.yaml`](examples/relay/minimal.yaml) | the smallest config that runs |
| [`full.yaml`](examples/relay/full.yaml) | every field, at its default, documented |
| [`personal-relay.yaml`](examples/relay/personal-relay.yaml) | your own outbox/backup relay |
| [`dev-test-relay.yaml`](examples/relay/dev-test-relay.yaml) | a throwaway relay for local dev or CI |
| [`public-search-relay.yaml`](examples/relay/public-search-relay.yaml) | a public relay with profile search and zap-ranked trending |
| [`anti-spam-relay.yaml`](examples/relay/anti-spam-relay.yaml) | a public relay that requires proof-of-work |
| [`accountability-relay.yaml`](examples/relay/accountability-relay.yaml) | anyone posts, but only signed in (NIP-42) |
| [`community-membership-relay.yaml`](examples/relay/community-membership-relay.yaml) | members only, joined by invite (NIP-43) |
| [`family-private-relay.yaml`](examples/relay/family-private-relay.yaml) | a closed circle the operator enrolls by hand |
| [`tracked-membership-relay.yaml`](examples/relay/tracked-membership-relay.yaml) | open to all, with a member roster and roles |
| [`enterprise-compliance-relay.yaml`](examples/relay/enterprise-compliance-relay.yaml) | members + proof-of-work + NIP-86 admin, stacked |
| [`agent-swarm-relay.yaml`](examples/relay/agent-swarm-relay.yaml) | agents posting under their owner's membership (NIP-AA) |
| [`app-backend-relay.yaml`](examples/relay/app-backend-relay.yaml) | an app's backend administering and querying over HTTP |
| [`community-voice-relay.yaml`](examples/relay/community-voice-relay.yaml) | voice calls and meeting spaces (huddles) |
| [`open-community-relay.yaml`](examples/relay/open-community-relay.yaml) | an open community: groups, seat notices, who's online, files |
| [`blossom-relay.yaml`](examples/relay/blossom-relay.yaml) | a media server: Blossom file hosting on the relay |

For a members-only community with search and MinIO, ready for Compose or
Helm, see [`deploy/stacks/community`](deploy/stacks/community/).

## `relay context`

Save, switch, and run relays by name instead of repeating `--config
path/to/relay.yaml` on every call:

```sh
ncli relay context add bee_community ~/relays/bee-community.yaml
ncli relay context add outbox ~/relays/outbox.yaml
ncli relay context use bee_community

# starts the relay server using bee_community's config
ncli relay

# targets bee_community too, no --config needed
ncli relay status
ncli relay members list

# list saved contexts, "*" marks the current one (same as `relay context list`)
ncli relay context
ncli relay context remove outbox
```

`--config` always wins; next the current context, then
`ncli.yaml`/`relay.yaml` in the current directory.

### `-c`/`--context`: run a context directly, creating it if new

`ncli relay -c <name>` runs a saved context without `context use`. If
`<name>` doesn't exist, it creates a minimal config under
`.ncli/relays/<name>/` and a fresh identity saved to the vault as `<name>`
(suffixed if the label is taken). The new context is saved but not made
current.

`-q`/`--json` skip the confirmation. `--identity <nsec-or-vault-label>`
uses an existing identity instead of generating one. `--context` and
`--config` are mutually exclusive.

## `relay members`/`invites`/`roles`

Manage NIP-43 membership on a running relay: add members, issue invite
codes, define roles. Needs `membership.enabled: true`; requests are
NIP-98-signed with `nip11.privkey`.

```sh
# enroll a pubkey directly -- no invite code needed
ncli relay members add <pubkey> --role vip --config examples/relay/community-membership-relay.yaml

# who's currently enrolled
ncli relay members list --config examples/relay/community-membership-relay.yaml
# one member's record, then un-enroll them
ncli relay members show <pubkey> --config examples/relay/community-membership-relay.yaml
ncli relay members remove <pubkey> --config examples/relay/community-membership-relay.yaml

# issue a code to hand out out-of-band (a signup email, a Discord invite)
ncli relay invites create --ttl 24h --max-uses 1 --config examples/relay/community-membership-relay.yaml
# every live code, then cancel one
ncli relay invites list --config examples/relay/community-membership-relay.yaml
ncli relay invites revoke <code> --config examples/relay/community-membership-relay.yaml

# define a role, and list them
ncli relay roles create vip --label "VIP" --color 280 --config examples/relay/community-membership-relay.yaml
ncli relay roles list --config examples/relay/community-membership-relay.yaml
```

There is no `roles delete`; re-run `roles create` with the same `id` to
replace a role.

**NIP-AA agent auth** (optional): an agent key counts as a member through
its owner's NIP-43 membership by presenting a NIP-OA `auth` tag during
AUTH, without being enrolled itself:

```yaml
# requires nip11.limitation.membership_required (which itself requires
# membership.enabled and limitation.auth_required)
agent_auth:
  enabled: true
  freshnessWindow: "120s"
  kindEnforcement: false
```

Examples: [`agent-swarm-relay.yaml`](examples/relay/agent-swarm-relay.yaml),
the commented block in
[`community-membership-relay.yaml`](examples/relay/community-membership-relay.yaml),
and [`full.yaml`](examples/relay/full.yaml).

<a id="relay-monitoring"></a>

## `relay status`/`index`/`jobs`/`verify`/`dashboard`

Monitor and maintain a running relay through its admin listener
(default `127.0.0.1:<port+1>`, from `listen`'s port when set), signed with `nip11.privkey`:

```sh
# health and live metrics
ncli relay status --watch

# each index: state, entries, how far behind, and what the relay is doing
ncli relay index list

# rebuild one: shows the plan, progress, then what changed
ncli relay index rebuild zaps

# the relay's own catch-ups and rebuilds, and yours
ncli relay jobs list

# a profile's NIP-05/lightning checks, score and stats; --refresh re-checks
ncli relay verify npub1... --refresh

# open the dashboard in your browser
ncli relay dashboard
```

Serve Prometheus metrics, or the dashboard from the relay itself:

```yaml
admin:
  # who may sign in, besides the relay's own key
  pubkeys: [npub1...]
  dashboard: {enabled: true}
  metrics: {enabled: true, tokensFile: ./data/metrics.tokens}
```

Sign in with a browser extension, an nsec (used in the page, never sent;
optionally saved encrypted with a password) or a remote signer
(`bunker://` or a `nostrconnect://` QR code).

```sh
# create a scrape token (printed once)
ncli relay metrics token add prometheus
```

The relay's URL in a browser shows a homepage built from its NIP-11
(`homepage: false` turns it off).

Examples: [`dashboard-relay.yaml`](examples/relay/dashboard-relay.yaml),
[`examples/monitoring/`](examples/monitoring/). Security and full reference:
`ncli skills show ncli-relay-ops`.

## `prefs`

Default relays that `find`, `ping`, `dump`, `miner check` and `publish` use
when you pass none.

```sh
ncli prefs relays add relay.primal.net
ncli prefs relays list

# no -s -- falls back to the prefs relay list above
ncli find jack@primal.net -k 1 -l 2

ncli prefs relays remove relay.primal.net
# remove all of them
ncli prefs relays clear

# print where prefs.yaml lives
ncli prefs path
```

![`ncli prefs relays add`, then `ncli find` with no -s at all](docs/vhs/prefs.gif)

## `find`

Look up events by ID and/or filter, stopping at the **first** target that
matches (`dump` merges every target). The identifier is an event (hex ID,
`note1...`/`nevent1...`) or an author (`npub1...`, `nprofile1...`, nip-05
`name@domain`); an author alone returns their profile (kind 0).

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
ncli find --kinds 1 --limit 5 -s relay.ohstr.com,relay.primal.net
```

![`ncli find` resolving a nip-05 address and its recent notes](docs/vhs/find.gif)

More filter flags (`-a/--authors`, `-i/--ids`, `--since`/`--until`/
`--search`/`--tag`): `ncli find --help`.

`--auth-identity` (alias `--signer`) logs in (NIP-42) where a relay
requires it: a socket signer (`bunker+unix:///path.sock`), a NIP-46
`bunker://...`, or a vault label/nsec. Only the login is signed, one kind
22242 per connection; omit it to query anonymously. A failed login exits 7
with `reason` `login_required`, `signer_refused` or `relay_refused` in
`--json`.

```sh
ncli find --kinds 1 -s wss://relay.example --auth-identity mykey
ncli find --kinds 1 -s wss://relay.example --auth-identity bunker+unix:///run/signer/agent.sock
```

A `--targets` file can carry `filters` as well as relays, replacing both
`-s` and the filter flags (a positional author is still ANDed with the
file's filters):

```sh
ncli find jack@primal.net -t examples/targets.yaml
```

```yaml
# examples/targets.yaml
kind: targets
spec:
  relays:
    - wss://relay.ohstr.com
    - relay: wss://relay.snort.social
      # skip re-verifying this source's signatures (default false)
      trusted: true
    - path: ./data/db/notes.db
      # create if missing (default), or "exists" to error if missing
      ensure: create

  # filters (optional, omit entirely to match everything): same NIP-01
  # fields as examples/filters.yaml. Multiple filters are OR'd together;
  # within one filter every field set must match (AND).
  filters:
    - kinds: [1]
      limit: 5
```

## `profile`

Look someone up as a readable card (one query, four records) instead of
`find -k 0` JSON:

```sh
ncli profile npub1...
ncli profile jack@primal.net
# a vault label works too
ncli profile mykey
# structured, for scripts
ncli profile npub1... --json
```

```
 ncli profile

  jack
  Bitcoin, Nostr, and open protocols.

  Identity
    nip-05       ✔ jack@example.com
    npub         npub1sg6plzptd64u62a878hep2kev88swjh3tw00gjsfl8f237lmu63q0uf63m
    pubkey       82129f882b6eab9a95d3f8f7c8556c873c1d2bc55cf7a250993e9553efdf3543
    lightning    ⚡ jack@example.com
    website      https://example.com

  Following      1,284

  Relays (3)
    ↕  wss://relay.example.com
    ↓  wss://read.example.com
    ↑  wss://write.example.com

  Blossom (2)
    •  https://blossom.example.com
    •  https://cdn.example.org

  queried 4 relay(s) · profile updated 2026-09-21
```

It reads kind:0 (metadata), kind:3 (following count), kind:10002 (NIP-65
relays: `↕` read+write, `↓` read, `↑` write) and kind:10063 (Blossom
servers) in one subscription, and **merges every relay** instead of
stopping at the first match like `find`.

The nip-05 is checked against its domain (`✔` verified, `✘` mismatch,
`⚠` unreachable); `--no-verify` skips it. An identity that published
nothing still renders (`not published`) and exits 0.

## `event`

Work with any event, no vault identity needed. Name it by nevent/note/naddr,
hex id or `kind:pubkey:d`, or pass JSON, `-f` files or stdin. Lookup order:
the reference's relay hints, then `-s` (or `ncli prefs relays`), then the
author's NIP-65 write relays.

```sh
# is this event authentic? (exit 1 if not)
ncli event verify nevent1...
ncli event verify -f signed.json --author npub1... --min-pow 20 --offline
ncli event verify naddr1... --cross-relays --deleted --json

# the event as served, or as a readable card
ncli event get note1... | jq .content
ncli event show naddr1...

# where does it live, and copy it somewhere else unchanged
ncli event seen nevent1...
ncli event rebroadcast nevent1... --to wss://my.relay.example

# build, sign and publish in one pipe
ncli event new --content "hello" --pubkey me \
  | ncli id sign -e - -o - --identity me \
  | ncli publish -e -
```

Also: `encode` (note/nevent/naddr with relay hints), `id` (compute ids of
unsigned events), `thread` (parents and direct replies), `diff` (two
versions), `unwrap` (decrypt a NIP-59 gift wrap with `--identity`) and
`validate` (the rule an nmilat relay applies to the event's kind).

## `ping`

Check that relays speak the protocol, not just accept a connection.
Fetches no events; exits non-zero if any relay is unreachable.

Output is plain log lines. `--tui` shows a live board instead (only in a
real terminal, without `--json`/`--quiet`).

```sh
# relays are plain arguments, no flag needed -- scheme optional too
ncli ping relay.primal.net relay.snort.social

# relays from a file (same shape as find/dump's --targets)
ncli ping -t examples/targets.yaml

# every `ncli prefs` relay
ncli ping

# structured report on stdout, for scripting
ncli ping relay.primal.net --json

# live interactive board instead of plain log lines
ncli ping relay.primal.net relay.snort.social --tui
```

![`ncli ping` checking two relays' reachability](docs/vhs/ping.gif)

## `dump`

Export events to JSON: same targets/filters as `find`, but merges
**every** target.

```sh
# export a relay's events to JSON
ncli dump -s relay.ohstr.com -o events.json

# merge a relay + a local store
ncli dump -s relay.primal.net,./data/db/notes.db -o out.json

# recent notes, every `ncli prefs` relay
ncli dump -k 1 --since 24h -o recent.json

# relays + filters from one YAML file
ncli dump -t examples/targets.yaml -o out.json
```

![`ncli dump` exporting events to JSON](docs/vhs/dump.gif)

`-o/--out` is required (`.json` or `.jsonp`). `--auth-identity` works as
in [`find`](#find).

## `publish`

Send signed events to relays and report each one's `OK`.

```sh
# mine + auto-sign, then publish
ncli miner mine --content "hello from ncli" --identity mykey -d 24 -o signed.json
ncli publish -e signed.json -s wss://relay.ohstr.com

# publish several events to several relays at once
ncli publish -e events.json -s relay.ohstr.com,relay.snort.social --json
```

Exits non-zero if any (event, relay) pair fails.

A relay that requires login (NIP-42) gets one kind 22242 per connection,
signed by `--signer`, or by `--auth-signer` when the login identity differs
(e.g. events someone else signed). The refused event is retried once.

```sh
ncli publish -e signed.json --auth-signer bunker+unix:///run/signer/agent.sock -s wss://members.example
```

## `apply`

`ncli apply -f <file>` runs one of three workflows, chosen by the file's
`kind`. Annotated versions live in [`examples/apply/`](examples/apply/);
the snippets below are trimmed.

- `--strict-pow` (default `false`) rejects an untrusted event whose `nonce`
  tag doesn't meet its declared difficulty. The spec's `strictPow: true`
  does the same; the flag wins when passed.
- `--auth-signer` logs in (NIP-42) to stream/sync relays that require it
  (`bunker+unix://`, `bunker://`, or a key); only login events are signed.

Full walkthrough, including `trusted: true` and relay-side PoW:
[`skills/ncli-apply/SKILL.md`](skills/ncli-apply/SKILL.md).

**`stream`** — forward everything matching `filters` from every `from` to
every `to` until interrupted (any mix of relays and stores):

```yaml
kind: stream
spec:
  from:
    - relay: "wss://relay.ohstr.com"
      # skip re-verifying this source's signatures (default false)
      trusted: true
    # bare-string shorthand for a remote relay also works
    - wss://relay.snort.social
  to:
    - path: "./data/mirror.db"
      ensure: create
  filters:
    - kinds: [1]
```

```sh
ncli apply -f examples/apply/stream.yaml
```

**`sync`** — NIP-77 negentropy sync between one local store and one relay
(`direction: both|up|down`):

```yaml
kind: sync
spec:
  from:
    type: local
    path: "./data/db/notes.db"
    ensure: create
  to:
    relay: "wss://relay.ohstr.com"
    # skip re-verifying this source's signatures (default false)
    trusted: true
  direction: both
```

```sh
ncli apply -f examples/apply/sync.yaml
```

**`inspect`** — read-only query across any mix of relays and stores; never
writes to a target:

```yaml
kind: inspect
spec:
  targets:
    - "wss://relay.primal.net"
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

For `stream`/`sync`, `ncli apply` opens a full-screen dashboard: a sortable
table of per-flow metrics (events, errors, throughput) and a log pane. Keys:

- `Tab` / `Shift+Tab` — move focus between panels
- a column's highlighted letter — sort by that column
- `d` — remove a flow from the running stream/sync (with confirmation)
- `Ctrl+S` — save the current spec (with live edits) to a new YAML file
- `r` — restart; saving the spec file on disk also prompts a reload

![`ncli apply` streaming live events into the TUI dashboard](docs/vhs/apply-stream.gif)

![`ncli apply` reconciling a local store against a relay (sync)](docs/vhs/apply-sync.gif)

`inspect` shows a Targets strip, the log, then a table of matched events.
Keys:

- `Enter` — open the selected event as read-only JSON
- `Ctrl+S` — save the selected event to a JSON file; saves in one session
  append to the same file (`<spec-file>-events-<timestamp>.json`)
- `w` / `s` — toggle Wrap/Autoscroll on the focused panel (the events table
  has Autoscroll only; open the detail view for full values)
- arrow/Page/Home/End pins the selection and turns off Autoscroll; `s`
  resumes it

`raw: true` in a `stream` spec skips the TUI and logs to stdout (for pipes
or a process supervisor).

![`ncli apply` inspecting matched events across relays and a local store](docs/vhs/apply-inspect.gif)

## `miner`

`ncli miner mine` finds a NIP-13 proof-of-work nonce, on every CPU core by
default. Write the note inline with `--content`/`--content-file` (fills in
`created_at`/`kind`) and `--identity` for the pubkey:

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

If `--identity` has a private key, the mined event is **signed** and ready
for `ncli publish`. A pubkey-only identity mines but can't sign.

For anything else, pass an event file:

```yaml
# examples/event.yaml
pubkey: 3c1db3dd55e2ff09ba5317dd8eec2339797e9e2ddf74591172735c47f3a2ad6e
# <-- replace with the current unix time, e.g. `date +%s`
created_at: 1719759720
kind: 1
tags:
  - ["t", "nostr"]
content: "hello from ncli"
```

```sh
ncli miner mine -e examples/event.yaml -o mined.json -d 24
```

`-e`'s file is only overwritten with `--in-place`. `--json` prints the
result as JSON.

`ncli miner check` verifies PoW on events from a file:

```sh
ncli miner check -e events.json
```

or fetched from relays (same filters), optionally only one identity's
events:

```sh
ncli miner check -s relay.ohstr.com --identity mykey --kinds 1 --since 7d --json
```

It exits non-zero if any event fails, so it fits CI/cron:

```sh
ncli miner check -e events.json || alert-oncall "PoW compliance regression"
```

## `bunker`

Run a NIP-46 remote signer: the key never leaves this process. Apps send
encrypted signing requests over a relay; you approve or reject each in a
TUI, or a remembered grant answers it.

```sh
ncli bunker --identity mykey --relay wss://relay.example

# reattach from another terminal
ncli bunker attach
# {"running", "identity_pub", "relays", "pending_count", "session_count"}
ncli bunker status --json
# every app with a remembered permission
ncli bunker sessions list
# one app's permissions; name it; drop one permission, or the whole app
ncli bunker sessions grants <pubkey>
ncli bunker sessions rename <pubkey> "my agent"
ncli bunker sessions revoke-grant <pubkey> --method sign_event --kind 1
ncli bunker sessions revoke <pubkey>
# recently resolved requests, most recent first
ncli bunker history
ncli bunker stop
```

![`ncli bunker` pairing an app via nostrconnect://, approving one request with a remembered grant, rejecting another outside its scope](docs/vhs/bunker.gif)

An approval can be "Always" instead of one-off. On Linux/macOS the TUI can
detach into a background daemon so apps stay connected.

Pair with `bunker://` or `nostrconnect://`, from the TUI (`c`) or a script:

```sh
# prints a bunker:// URI to paste into a client
ncli bunker connect
ncli bunker connect "nostrconnect://..."
# pre-authorize the app, no prompts
ncli bunker connect --grants examples/bunker/agent.yaml
```

Everything the TUI does also works from scripts. With no TTY (or with
`--json`), `ncli bunker` starts the signer in the background and prints its
status:

```sh
# start headless; prints the same JSON as `bunker status --json`
ncli bunker --identity mykey --relay wss://relay.example --json
# requests no grant covers, waiting for a decision
ncli bunker pending list --json
# approve once
ncli bunker pending approve <id>
# approve and remember it for 24h, like the TUI's "Always" button
ncli bunker pending approve <id> --always --for 24h
ncli bunker pending reject <id>
# add or replace an already-paired app's permissions
ncli bunker sessions set-grant <pubkey> --grants grants.yaml
```

See [`skills/ncli-bunker/SKILL.md`](skills/ncli-bunker/SKILL.md) for
Windows limits, the grants-spec format, and pairing an agent for unattended
signing.

## `signer`

A local signer for processes on the same machine or pod: `ncli` holds the
key in memory and signs over a unix socket, as a policy allows. No relay,
no network listener; a signature takes under a millisecond.

For agents: the key sits in a sidecar container and the agent's tools ask
it to sign. A prompt-injected agent can't read the key or sign what the
policy forbids.

```sh
# key from an ncryptsec file (or --identity <vault label>)
ncli signer serve --socket /run/signer/agent.sock \
  --identity file:/etc/signer/key.ncryptsec --password-file /etc/signer/password \
  --policy examples/signer/agent-policy.yaml --state-dir /var/lib/signer
# sign through it; the key never enters this process
ncli id sign --signer bunker+unix:///run/signer/agent.sock -e draft.json -o signed.json
ncli publish -e draft.json --signer bunker+unix:///run/signer/agent.sock
# health probe: exits 6 when nothing answers
ncli signer status --socket /run/signer/agent.sock
# dry-run a policy in CI: exits 7 on a denial
ncli signer check --policy policy.yaml -e event.json --pubkey npub1...
```

**Policy** (`kind: signer-policy`): rules are tried in order, the first
match decides, anything unmatched is denied.

```yaml
kind: signer-policy
rules:
  - name: chat
    allow: {kinds: [9, 7, 1111]}
    deny_matching: ['nsec1[02-9ac-hj-np-z]{58}']
    rate: 60/m
  - name: relay-auth
    allow: {kinds: [22242], tags_one: {relay: ["wss://relay.example", "wss://relay.example/*"]}}
  - name: repo-state
    allow: {kinds: [30617, 30618]}
    require:
      attestations:
        - name: maintainer-approval
          kinds: [9]
          authors_file: approvers.txt
          max_age: 15m
          binds: [{content: "approve {id}"}]
  - name: everything-else
    deny: {}
```

- **Selectors** match on `methods`, `kinds`, `counterparts` (for
  encrypt/decrypt) and `tags`. A tag value matches exactly, or as a prefix
  when it ends in `*`.
- **`require: attestations`** lists signed events the request must carry
  (a human approval, a CI result). Bind one to the target with
  `{tag: e, from: id}`, `{tag: d, from: "tag:d"}`, or
  `{content: "approve {id}"}` / `{content_contains: ...}`. Each is
  single-use.
- **Always on:** an event whose content or tags contain the signer's own
  key is never signed; an attestation signed by the signer's key, or made
  before the signer started, never counts.

Full reference:
[`skills/ncli-signer/references/policy.md`](skills/ncli-signer/references/policy.md).

**Threat model.** Any process that can open the socket is a client; the
socket's mode, owner and `--allow-uid`/`--allow-gid` are the
authentication.

| A compromised client can | It cannot |
|---|---|
| sign whatever the policy allows, up to the rate limits | read the key, which never crosses the socket |
| decrypt messages from the counterparts the policy lists | get the key into a signed event or an encrypted message |
| fill the decision log or keep the signer busy | satisfy an attestation, which needs another key |
| | replay an attestation, within a run or across restarts |

Residual risks:
- A process with the signer's uid or PID namespace can read the key from
  memory. Use a separate container, a separate uid and
  `shareProcessNamespace: false`.
- An approver's key is as powerful as the rule it unlocks.
- If `--state-dir` is lost, an attestation made within `max_clock_skew`
  before a restart may validate once more; keep the skew small.
- Unscoped auth kinds (22242, 27235) let a client log in as the signer
  anywhere; scope them with `tags`.

**Protocol.** The NIP-46 method set (`get_public_key`, `sign_event`,
`nip04_*`, `nip44_*`, `ping`) is sent as one JSON object per line:
`{"id","method","params"}` in, `{"id","result"|"error"}` out. There is
no kind-24133 wrapper and no encryption.
- `connect`, `get_relays`, `switch_relays` and `logout` get no-op acks.
- `sign_event` takes an optional `params[1]`, a JSON array of attestation
  events.
- Error strings start with `denied: ` (policy) or `invalid: ` (malformed
  request).
- `signer_status` is ncli's own addition.

**Failure modes:**

| Situation | Result |
|---|---|
| Signer down | `network` (exit 6) |
| Request denied | `auth` (exit 7) with the rule's reason |
| Bad policy at start | `serve` exits |
| Bad policy on SIGHUP or `--watch` | old policy kept |
| Used set can't be written | request denied |

Every decision goes to stdout as one JSON line, with no content or key
material. `--denials-file` collects denials alone.

**For client authors** (ngit, agent bridges, ...):
- Accept `bunker+unix:///abs/path.sock` (and `unix://`) wherever you
  accept `bunker://`.
- Dial the path and write plain NIP-46 request JSON, one per line (no
  encryption, no event wrapper); read one response line per request.
- Send attestations as `params[1]` of `sign_event`.
- Treat `denied: ...` as a refusal, not a transport error.
- In Go, [`nmilat/nipLS`](https://github.com/ohstr/nmilat/tree/main/nipLS)
  dials (`nipLS.Dial`) and serves (`nipLS.NewServer`) it. Spec:
  [NIP-LS](https://github.com/ohstr/nips/blob/main/NIP-LS.md).

`examples/signer/` has a full agent policy and a Kubernetes pod manifest
with the sidecar. See [`skills/ncli-signer/SKILL.md`](skills/ncli-signer/SKILL.md).

## `space`

A meeting space (NIP-53, kind:30312) is a published record of where a
meeting lives; it lasts when empty. Joining opens its voice call (a huddle)
and its chat.

```sh
ncli space create standup --summary "Daily sync" --relay wss://relay.example --identity mykey
ncli space list --relay wss://relay.example
ncli space show standup --relay wss://relay.example
# join the call: live roster, who is speaking, and the chat
ncli space join standup --relay wss://relay.example --identity mykey
```

Without a terminal (or with `--json`), `space join` streams JSON lines
(`joined`, `participant_joined`/`left`, `speaking_started`/`stopped`,
`chat`, `ended`) until `--duration` or an interrupt. Chat works without
joining:

```sh
ncli space join 30312:<pubkey>:standup --relay wss://relay.example --duration 60s --json
ncli space chat send 30312:<pubkey>:standup "hello" --relay wss://relay.example --identity mykey
ncli space chat list 30312:<pubkey>:standup --relay wss://relay.example --json
```

See [`skills/ncli-space/SKILL.md`](skills/ncli-space/SKILL.md).

## `huddle`

The voice rooms behind spaces, hosted by the relay. Enable the `huddle:`
block ([`examples/relay/community-voice-relay.yaml`](examples/relay/community-voice-relay.yaml)),
then list rooms that have someone in them:

```sh
ncli huddle list --relay wss://relay.example
```

Join with [`ncli space join`](#space): it shows who is in the room and who
is speaking. It never captures a microphone, so joining adds no audio.

Two endpoints reach the same rooms (same room id, same call):
`/huddle/{id}/audio` (Opus over a plain WebSocket) and `/huddle/{id}/rtc`
(a WebRTC SFU that also carries video and screen share). The relay forwards
Opus without decoding it.

`huddle.enabled` requires `nip11.url` (the joining client's NIP-42 event
names it). `huddle.allowedOrigins` is empty by default, allowing any origin;
admission is checked by a signed challenge, not `Origin`.

Hearing the call needs a build with audio output:

```sh
# macOS, Windows
go build -tags huddleaudio ./cmd/ncli
# Linux, needs libasound2-dev
CGO_ENABLED=1 go build -tags huddleaudio ./cmd/ncli
```

Release binaries are built without it (audio needs cgo, and ALSA on Linux).
Without the tag the roster still works and the status line says
`watching only`.

## `groups`

NIP-29 groups hosted by the relay: private and closed by default, joined by
invite code, with admins, roles, pins, and subgroups.

```sh
ncli groups create standup --relay wss://relay.example --identity mykey
ncli groups edit standup --name "Standup" --about "Daily sync" --relay wss://relay.example --identity mykey
# an invite code for someone to join with
ncli groups invite standup --relay wss://relay.example --identity mykey
ncli groups join standup --invite-code <code> --relay wss://relay.example --identity friend
# groups this identity belongs to
ncli groups list --mine --relay wss://relay.example --identity mykey
ncli groups show standup --relay wss://relay.example --identity mykey
# every visible group, as a parent/child tree
ncli groups tree --relay wss://relay.example

# admins: add or remove a member (optionally with roles)
ncli groups members add standup <pubkey> moderator --relay wss://relay.example --identity mykey
ncli groups members remove standup <pubkey> --relay wss://relay.example --identity mykey
# replace the pinned list (passing nothing clears it)
ncli groups pins set standup --event <event-id> --relay wss://relay.example --identity mykey
# delete one message from the group, or the whole group
ncli groups delete-event standup <event-id> --relay wss://relay.example --identity mykey
ncli groups delete standup --relay wss://relay.example --identity mykey
# a member leaving
ncli groups leave standup --relay wss://relay.example --identity friend
```

A private group is hidden without `--identity`: `list` leaves it out and
`show` is refused (`auth`). With `groups.pendingJoins`, a codeless join to a
closed group waits for an admin to `members add` you. See
[`skills/ncli-groups/SKILL.md`](skills/ncli-groups/SKILL.md).

## `dm`, and talking through a signer

Private messages (NIP-17), group chat, reactions, replies and your profile,
all through `--signer`, so an agent's key can stay in `ncli signer serve`.

```sh
ncli dm send --to npub1alice... "on my way" -s wss://inbox.example --signer bunker+unix:///run/signer/agent.sock
# only alice's messages are opened; -f streams new ones
ncli dm read --from npub1alice... -s wss://inbox.example -f --state dm.state
# react privately (NIP-25 in a gift wrap), and take it back (NIP-09)
ncli dm react --to npub1alice... <message-id> 👀 -s wss://inbox.example
ncli dm unreact --to npub1alice... --target <message-id> --emoji 👀 -s wss://inbox.example
# show one message to anyone, who checks it without a key
ncli dm prove <wrap-id> --from npub1alice... -s wss://inbox.example -o proof.json
ncli dm verify proof.json --from npub1alice...

ncli groups send standup "done" --relay wss://relay.example --signer bunker+unix:///run/signer/agent.sock
ncli event react nevent1... "👀" --signer bunker+unix:///run/signer/agent.sock
ncli profile follow npub1bob... --signer bunker+unix:///run/signer/agent.sock
# wait for new events
ncli find -f --kinds 1 --tag e=<event-id> -s wss://relay.example
```

In the signer policy, `unwrap_from` limits which senders' gift wraps open,
and `nip44_message_keys` (for `dm prove`) must be named. See
[`skills/ncli-messaging/SKILL.md`](skills/ncli-messaging/SKILL.md) and
[`examples/signer/messaging-policy.yaml`](examples/signer/messaging-policy.yaml).

## `form`, questions answered in one response (NIP-PF)

A form asks questions (single or multiple choice, text, number, date,
time) with the author's recommended option and why, optional required
notes, defaults, a quorum and an end. Each responder answers all questions
in one response (the latest counts); the author closes it with a result:
decided, default or cancelled.

A form is private (gift-wrapped to each responder and to you, sent to `-s`
only; `--inbox` adds each recipient's DM relays, kind 10050), in a NIP-29
group, or public. Everything is signed through `--signer`.

```sh
ncli form create --spec ask.json --to npub1bob... -s wss://inbox.example --signer bunker+unix:///run/signer/agent.sock
ncli form list --open -s wss://inbox.example
ncli form show 3f2a9c -s wss://inbox.example
ncli form respond 3f2a9c --choose revert --note revert="after lunch" -s wss://inbox.example
ncli form respond 3f2a9c -s wss://inbox.example     # question by question on the terminal
ncli form result 3f2a9c --status decided --choose revert --content "reverted" -s wss://inbox.example
```

A response the form would refuse is refused before sending. See
[`skills/ncli-forms/SKILL.md`](skills/ncli-forms/SKILL.md).

## `blossom`

A client for [Blossom](https://github.com/hzrd149/blossom) media servers:
content-addressed storage, authenticated with your Nostr identity.

```sh
ncli blossom servers add https://blossom.example --identity mykey --publish

ncli blossom upload photo.jpg --identity mykey
ncli blossom list --identity mykey
ncli blossom download <hash> -o photo.jpg
ncli blossom rm <hash> --identity mykey --yes
# copy a blob from another URL onto your servers
ncli blossom mirror https://other.example/<hash> --identity mykey
# report a blob to its server (BUD-09)
ncli blossom report <hash> --type spam --reason "bot spam" --identity mykey

# your default servers; drop one
ncli blossom servers list
ncli blossom servers remove https://blossom.example
```

`upload`/`rm`/`mirror` go to every configured server and report each
(item, server) result; `download` of a full URL fetches that URL first, then
asks the servers in order by hash until one answers.
Commands that take an identity also accept someone else's:

```sh
# someone else's media, not just your own
ncli blossom list alice@example.com
# find (and adopt) their published servers
ncli blossom servers discover alice@example.com --add
```

See [`skills/ncli-blossom/SKILL.md`](skills/ncli-blossom/SKILL.md),
including BUD-03 server-list publishing and discovery.

## `id`

No argument generates a keypair (hex, nsec, npub); an argument inspects an
existing identity.

```sh
# generate an identity, save it to the local vault (prints only the public key)
ncli id --save --label mykey
# same, and also print the new private key
ncli id --save --label mykey --reveal

# inspect an identity by npub
ncli id npub1...

# ...or a saved vault label, with privkey/nsec
ncli id mykey --reveal

# list every saved vault identity
ncli id list --reveal

# save an existing key (nsec, hex or ncryptsec), read from stdin or --file
ncli id import --label mykey < key.txt

# rename a saved identity
ncli id relabel mykey work

# remove a saved identity (asks first)
ncli id rm work

# non-interactive, vault password from NCLI_VAULT_PASSWORD
ncli id --json --save --label mykey

# sign an unsigned event with a saved identity, ready for `ncli publish`
ncli id sign --identity mykey -e draft.json -o signed.json

# mint a NIP-26 delegation token from two saved identities
ncli id delegate --issuer mykey --delegatee otherkey --json
```

The vault is password-encrypted. `ncli id attest <npub> --until <time>`
signs a NIP-OA owner attestation with an identity or through `--signer`.

![`ncli id` generating a keypair, then inspecting a nip-05 address](docs/vhs/id.gif)

## `decode`

Decode a NIP-19 entity.

```sh
# npub/nsec/note/nprofile/nevent/naddr -> hex fields
# (+ relay hints/kind if embedded)
ncli decode npub1...

# same, as structured JSON
ncli decode npub1... --json
```

![`ncli decode` resolving a npub to its hex pubkey](docs/vhs/decode.gif)


## Agent skills

[`AGENTS.md`](AGENTS.md) points agents to the matching skill under
[`skills/`](skills/): one per command group, needing only `ncli` on `PATH`.

```
# from the ncli binary itself, matching its version, into your agent's skills dir
ncli skills install --dir ~/.agents/skills

# any agentskills.io-compatible agent
npx skills add ohstr/ncli --all -y

# Claude Code only
/plugin marketplace add ohstr/ncli

# ...one command group at a time
/plugin install ncli-apply@ncli
```

## Configuration

Config comes from a YAML file or `NCLI_`-prefixed environment variables.
Without `--config`, ncli uses the current [`relay context`](#relay-context),
then `ncli.yaml`/`relay.yaml` in the current directory, then `$HOME`. Every
YAML input has a documented sample under [`examples/`](examples/).

## Docker and Kubernetes

[`deploy/`](deploy/) runs the `ghcr.io/ohstr/ncli` image as one of several
relay stacks (`deploy/stacks/<kind>/`), each with a Compose file and values
for the shared Helm chart:

| stack | what runs |
|---|---|
| `dev` | relay built from this checkout + Meilisearch (Compose only) |
| `personal` | relay alone |
| `public-search` | open relay + Meilisearch: profile search, stats, zap ranking |
| `community` | members-only relay with NIP-29 groups and the HTTP bridge + Meilisearch + MinIO |

```sh
cp deploy/stacks/public-search/.env.example deploy/stacks/public-search/.env
# edit .env: RELAY_PRIVKEY, RELAY_URL, MEILI_MASTER_KEY
docker compose -f deploy/stacks/public-search/compose.yaml up -d
```

Secrets stay in `.env` (gitignored) or a Kubernetes Secret and reach the
relay as `NCLI_*` variables. Helm: [`deploy/README.md`](deploy/README.md).

### Meilisearch versions

Profile search needs Meilisearch 1.14+; 1.55+ is recommended (faster
indexing on large relays since 1.41/1.44; fixes for an indexing memory leak
in 1.39 and a thread-exit crash in 1.52.4, 1.53.3, 1.54.3). Avoid 1.41.x
and 1.42.x (typo-tolerance bug, fixed in 1.43).

- Upgrade an existing database in place by starting once with
  `--upgrade-db` (`MEILI_UPGRADE_DB=true`), or start empty: the relay
  rebuilds search itself.
- On a small host, set `MEILI_MAX_INDEXING_MEMORY` (e.g. `1GiB`) to leave
  memory for the page cache a large index needs.

## Development

Tasks run with [`just`](https://github.com/casey/just):

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

> Building from source needs a one-time setup step: see
> [CONTRIBUTING.md](CONTRIBUTING.md#getting-set-up).

Releases are cut by hand with the `workflow_dispatch`-only
[`.github/workflows/release.yml`](.github/workflows/release.yml). The
release PR first pins every relay image to the version being released
([deploy/README.md](deploy/README.md#images)); the workflow refuses to
tag otherwise.

## License

[Unlicense](LICENSE) — public domain.
