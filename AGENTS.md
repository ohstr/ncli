# ncli

`ncli` is a Go CLI for the Nostr protocol: run a relay, stream/sync/query
events, manage keys, and mine proof-of-work — all driven by YAML spec/config
files. Assume the `ncli` binary is already on `PATH`. Global config comes from
`--config <file>`, `NCLI_*`-prefixed env vars, `ncli.yaml`/`relay.yaml` in the
current directory, a saved `ncli relay context` (see below), or `$HOME`, in
that priority order.

## Commands

| Command | Purpose |
|---|---|
| `ncli apply -f <spec.yaml>` | Run a `stream`/`sync`/`inspect` workflow from a YAML spec |
| `ncli dump -o <out.json>` | Export events to JSON from a relay, `.db` file, or prefs relays |
| `ncli find <id>` / `-t <targets.yaml>` | Look up events by ID (hex/note/nevent) or author (npub/nprofile/nip-05), and/or filter, across targets |
| `ncli profile <identifier>` | Readable profile card for one identity — metadata, following count, relay list, Blossom servers, lightning address — aggregated across every relay (not stopping at the first hit like `find`) |
| `ncli ping <relay...>` / `-t <targets.yaml>` | Probe whether targets are reachable (connect + subscribe), no events fetched |
| `ncli prefs relays add/list/remove/clear` | Manage the default relay list `find`/`dump`/`ping`/`miner check` fall back to |
| `ncli relay --config <relay.yaml>` | Run the relay server; `agent_auth` block enables NIP-AA (an agent key gains virtual membership from its owner's NIP-43 membership via a NIP-OA credential, no separate enrollment); `nip86` block serves the NIP-86 management API on the relay URL so an app can administer membership without a terminal; `query` block serves buzz's NIP-CW `POST /query` bridge, a one-shot HTTP alternative to a WS REQ/EOSE round trip -- NIP-98 there binds identity; results are further gated to the signer's own NIP-43 membership when `nip11.limitation.membership_required` is set, same as REQ/COUNT |
| `ncli relay stats/reindex/clear` | Administer a *running* relay over NIP-98 HTTP, incl. rebuilding search/zap indexes |
| `ncli relay members/invites/roles` | Administer a *running* relay's NIP-43 membership over NIP-98 HTTP: enroll/remove members, issue/revoke invite codes, define roles |
| `ncli relay context list/add/use/remove` | Save named relay `--config` shortcuts and switch the current one, so `relay` subcommands stop needing `--config` repeated on every call |
| `ncli relay -c/--context <name>` | Run directly against a saved context by name (bypassing "current"); if `<name>` doesn't exist yet, offers (or `-q`/`--json` auto-confirms) to create a minimal relay backed by a fresh vault identity under `<AppConfigDir>/relays/<name>/` |
| `ncli bunker` | Run as a NIP-46 remote signer: approve/reject other clients' signing requests from a live TUI, remembering per-app decisions so you aren't re-prompted every time |
| `ncli bunker attach/status/stop/sessions/connect` | Reattach to, query, stop, or pair a running bunker daemon without opening the TUI |
| `ncli blossom upload/download/list/rm/mirror/report` | Client for the Blossom protocol (BUD-01..12): content-addressed blob storage authenticated with a Nostr identity |
| `ncli blossom servers add/remove/list/discover` | Manage the default Blossom server list, optionally publishing/discovering it as a signed kind:10063 (BUD-03) event |
| `ncli huddle list` | List the ephemeral transport rooms that currently have someone in them, with peer count and the protocol version each is pinned to — a room id is otherwise out-of-band knowledge, and `space join` on an unused one opens it rather than failing |
| `ncli space create <id>` | Publish a new NIP-53 meeting space (kind:30312). `--service` defaults to `--relay`, which is what makes the space joinable with `space join` afterward — point it elsewhere to describe a space on a transport ncli itself can't dial. `id` doubles as the huddle room id, so it's rejected up front if it contains `/`, `?`, `#` or `%` (put a human-readable name in `--summary` instead) |
| `ncli space list` / `ncli space show <id>` | List every open space a relay knows about, or show one's full detail — a space with no live session is still shown, since the space is the durable thing, a live call is just a property of it |
| `ncli space join [id\|naddr\|room]` | Join a space's voice/audio call and watch the live roster and who is speaking; plays the call in a `-tags huddleaudio` build (never captures a microphone). Given a NIP-53 activity instead of a room id (naddr, or a `30312:`/`30313:` coordinate) it resolves the space to find the relay and room, and opens the kind:1311 conversation beside the roster — reply with `<r>`, quote with `<y>`, or `--no-chat` to omit it. Omitting the argument entirely joins `--relay`'s one open space, if there's exactly one (errors listing every candidate if there's more than one). Chat needs an activity: a 1311 message must name one, and a bare room id is not an addressable event. (Implemented in `cli/huddle`, mounted only under `space` — voice is one option a space can enable, not a separate top-level thing to join) |
| `ncli groups create/edit/delete/invite/join/leave` | Self-service NIP-29 relay-hosted-group writes — each a plain signed event (no admin HTTP surface), with a `previous` tag attached automatically; `edit` reads the group's current metadata first and merges in only the flags passed, since the underlying event replaces metadata wholesale |
| `ncli groups members add/remove` / `ncli groups pins set` / `ncli groups delete-event` | More NIP-29 writes: add/remove a member (optionally with roles), replace a group's pinned-events list (whole list, not append — passing nothing clears every pin), or delete one of the group's own events |
| `ncli groups list` / `ncli groups show <group-id>` | Read a relay's groups (metadata) or one group's full detail (metadata/admins/members); `--identity` authenticates (NIP-42) so a member can see their own private group, omitted it stays anonymous. `show` names one group, so the relay refuses it outright (exit `7`, `auth`) without membership; `list` names none, so it silently drops private groups you're not a member of instead |
| `ncli id [identifier]` | Generate or inspect a Nostr keypair (local vault) |
| `ncli id delegate` | Mint a NIP-26 delegation token |
| `ncli id sign -e <events.json> -o <signed.json>` | Sign one or more unsigned events with a vault/nsec identity |
| `ncli decode <entity>` | Decode any NIP-19 bech32 entity (npub/nsec/note/nprofile/nevent/naddr), a NIP-CASH cash-token-family string, or a NIP-CW circlehub1... connection |
| `ncli miner -e <event.yaml>` | Mine or check NIP-13 proof-of-work on an event |
| `ncli version` | Build info + on-disk paths |

Every relay input (`-s/--relays`, `prefs relays add`, `targets.yaml`, `apply`
flow entries) accepts a bare host with no `ws(s)://` scheme —
`relay.primal.net` tries `wss://` first, falling back to `ws://` only if
that fails and no scheme was given explicitly.

## Output conventions

Every command's actual result goes to **stdout only**; progress narration
and errors go to **stderr** — so piping stdout into `jq` or a script's
parser never picks up log noise. `--json` and `-q/--quiet` are global flags
(declared once on the root command, available on every subcommand) rather
than per-command. `id`, `id list`, `id sign`, `version`, `id delegate`, `relay
stats`/`reindex`/`clear`, `relay members`/`invites`/`roles`, `ping`,
`miner mine`/`check`, `publish`, `huddle list`, `space create`/
`list`/`show`, `groups create`/`edit`/`delete`/`invite`/`join`/`leave`/
`members`/`pins`/`delete-event`/`list`/`show`, and `prefs relays add`/
`remove`/`list`/`clear`/`prefs path` are human-readable text by default
and switch their *success* output to structured JSON on stdout with
`--json`. `find` has no
separate success-mode toggle because it's JSON-only always, and its stdout
is guaranteed to be exactly one JSON array on every successful run — `[]`
when nothing matched, never bare `null` and never empty output — so a
script never needs a no-result special case.
`-q/--quiet` drops the stderr narration on any command (warnings/errors
still show), for callers that can't rely on stdout/stderr being captured
separately.

`find`/`dump`/`miner check` query one or more targets and tolerate
individual failures — an unreachable relay is logged and skipped rather
than failing the whole query, so a mix of reachable and unreachable
targets still returns a normal (possibly partial) result with exit 0. But
if *every* target fails to connect or times out, that's a `network` error
(see the table below), not a false-successful empty result — `[]`/exit 0
means "queried successfully, found nothing," never "couldn't check."
`ping` is the opposite: reachability is exactly what it's testing for, so
*any* unreachable target is a failure (`internal`, exit 1), not tolerated
and logged like it is elsewhere.

**Failures**: exactly one top-level error report, always on stderr, never
stdout. In text mode it takes one of three shapes, and `--json` replaces
all three with a single `{"error", "code", "retryable", "input"}` line:

| shape | when | looks like |
|---|---|---|
| help alone, no error line | the command was invoked bare — no arguments and none of its own flags (`ncli decode`, `ncli miner`) | the command's `--help` text, on stderr |
| `Error: <msg>`, blank line, then help | something *was* supplied and it was wrong — bad arg count, unknown flag or subcommand, conflicting flags | `Error:` in red on a TTY, then the help |
| `Error: <msg>` alone | the invocation was fine and the operation failed (`ncli ping` with no relays, a bad identifier) | one line; help wouldn't help |

The first shape still exits non-zero with its usual code — help is shown
instead of scolding, **not** instead of failing, so a mistyped subcommand
is never mistaken for success. `Error:` is red only on a real terminal,
and never when `NO_COLOR` is set or stderr is piped.

| `code` | exit | retryable | meaning |
|---|---|---|---|
| `usage` | 2 | no | bad/missing/conflicting flags/args/config, a relay-side feature that isn't turned on (e.g. `relay members ...` against a relay with `membership.enabled: false`), or a group command (`relay members`/`invites`/`roles`/`reindex`/`clear`, `prefs`, `prefs relays`, `miner`, `blossom`, `blossom servers`, `bunker sessions`) invoked without one of its own subcommands |
| `invalid_input` | 3 | no | a supplied value failed validation/parsing (bad identifier, URL, key, duration, kind, ...) |
| `not_found` | 4 | no | the referenced thing doesn't exist (vault entry, configured relay, ...) |
| `conflict` | 5 | yes | collides with existing state (vault label taken, reindex already running) |
| `network` | 6 | yes | a remote call failed (relay connection, admin HTTP request, nip-05 fetch) |
| `auth` | 7 | no | wrong credentials or a rejected signature |
| `unsupported` | 8 | no | the target server doesn't support the requested capability at all (currently only `blossom list` against a server with BUD-02 disabled), distinct from `not_found`'s "this one specific resource is missing" |
| `internal` | 1 | no | anything else (fallback bucket) |

`input`, when present, is the single specific value that caused the
failure (an identifier, file path, relay URL, `--kinds` token, ...) —
omitted when there's no one clean value, or when the value is private-key
material (never echoed, even malformed). `retryable` lets an agent decide
whether to back off and retry (`network`/`conflict`) or fix the input and
try again (everything else) without string-matching the message. No
command double-reports the same failure in two shapes, and `--json` never
prints help at all — just the one structured line, whichever of the three
text shapes the failure would otherwise have taken.

**Waiting**: any command that blocks on the network (`find`, `dump`,
`ping`, `publish`, `profile`, `miner check`, every `blossom` and `relay`
admin subcommand) animates a spinner on stderr while it waits, with the
per-relay progress narration still printed above it. The spinner is off
under `--json`, under `-q/--quiet`, when `NO_COLOR` is set, and whenever
stderr isn't a terminal — so captured output is byte-identical to before.

`--json` also switches *every* other stderr log line (progress narration,
warnings, a partial/recoverable failure like one unreachable target in a
multi-target query) from a colored console line to a single-line JSON
object (`{"level","message","time",...}`, zerolog's native shape) — not
just the command's own final error — so a script parsing stderr never has
to handle two different log formats depending on where in the run the
message came from.

## Before attempting a task, read the matching skill

This repo ships deep, example-driven guidance in `skills/`, one file per
area, each with runnable commands and hard-won gotchas verified against the
Go source (not just the README). Read the relevant one *before* writing YAML
or invoking a command in that area:

- Writing or running an `apply` spec (`stream`/`sync`/`inspect`) →
  `skills/ncli-apply/SKILL.md`
- Querying/exporting events, or testing relay connectivity (`dump`,
  `find`, `ping`, `filters.yaml`, `targets.yaml`, `prefs`) →
  `skills/ncli-query/SKILL.md`
- Configuring or operating a relay (`relay.yaml`, `relay stats`/`reindex`/
  `clear`, incl. reindexing search/zaps, NIP-43 membership via `relay
  members`/`invites`/`roles`, or NIP-AA `agent_auth`) →
  `skills/ncli-relay-ops/SKILL.md`
- Generating/managing keys or delegation tokens (`id`, `id delegate`), or
  decoding a NIP-19 entity, cash token, or circlehub1... connection
  (`decode`) → `skills/ncli-identity/SKILL.md`
- Mining or verifying proof-of-work (`miner`) → `skills/ncli-miner/SKILL.md`
- Running `ncli` as a remote NIP-46 signer (`bunker`, `bunker attach/
  status/stop/sessions/history/connect`), including pairing an agent as
  its NIP-46 client so it can get events signed without holding a raw
  key → `skills/ncli-bunker/SKILL.md`
- Uploading/fetching/managing content on Blossom media servers
  (`blossom upload/download/list/rm/mirror/report/servers`) →
  `skills/ncli-blossom/SKILL.md`
- Listing a relay's live ephemeral transport rooms (`huddle list`) or the
  audio/video/chat details of an in-progress call → `skills/ncli-huddle/
  SKILL.md`
- Creating, listing, or showing a NIP-53 meeting space, or joining one's
  voice call (`space create`/`list`/`show`/`join`) →
  `skills/ncli-space/SKILL.md`
- Creating or administering a NIP-29 relay-hosted group (`groups create`/
  `edit`/`delete`/`invite`/`join`/`leave`/`members`/`pins`/`delete-event`/
  `list`/`show`) → `skills/ncli-groups/SKILL.md`

These skills assume only the `ncli` binary is available — no access to this
source tree. (Building/contributing to `ncli` itself is a different task —
see CONTRIBUTING.md and `.agents/skills/local-verify`.)
