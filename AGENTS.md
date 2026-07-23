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
| `ncli ping <relay...>` / `-t <targets.yaml>` | Probe whether targets are reachable (connect + subscribe), no events fetched |
| `ncli prefs relays add/list/remove/clear` | Manage the default relay list `find`/`dump`/`ping`/`miner check` fall back to |
| `ncli relay --config <relay.yaml>` | Run the relay server; `agent_auth` block enables NIP-AA (an agent key gains virtual membership from its owner's NIP-43 membership via a NIP-OA credential, no separate enrollment) |
| `ncli relay stats/reindex/clear` | Administer a *running* relay over NIP-98 HTTP, incl. rebuilding search/zap indexes |
| `ncli relay members/invites/roles` | Administer a *running* relay's NIP-43 membership over NIP-98 HTTP: enroll/remove members, issue/revoke invite codes, define roles |
| `ncli relay context add/use/remove` | Save named relay `--config` shortcuts and switch the current one, so `relay` subcommands stop needing `--config` repeated on every call |
| `ncli id [identifier]` | Generate or inspect a Nostr keypair (local vault) |
| `ncli id delegate` | Mint a NIP-26 delegation token |
| `ncli id sign -e <events.json> -o <signed.json>` | Sign one or more unsigned events with a vault/nsec identity |
| `ncli decode <entity>` | Decode any NIP-19 bech32 entity (npub/nsec/note/nprofile/nevent/naddr) |
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
`miner mine`/`check`, `publish`, and `prefs relays add`/`remove`/`list`/
`clear`/`prefs path` are human-readable text by default and switch their
*success* output to structured JSON on stdout with `--json`. `find` has no
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
stdout — a plain timestamped line by default, or `{"error", "code",
"retryable", "input"}` with `--json`:

| `code` | exit | retryable | meaning |
|---|---|---|---|
| `usage` | 2 | no | bad/missing/conflicting flags/args/config, a relay-side feature that isn't turned on (e.g. `relay members ...` against a relay with `membership.enabled: false`), or a group command (`relay members`/`invites`/`roles`/`reindex`/`clear`, `prefs`, `prefs relays`, `miner`) invoked without one of its own subcommands |
| `invalid_input` | 3 | no | a supplied value failed validation/parsing (bad identifier, URL, key, duration, kind, ...) |
| `not_found` | 4 | no | the referenced thing doesn't exist (vault entry, configured relay, ...) |
| `conflict` | 5 | yes | collides with existing state (vault label taken, reindex already running) |
| `network` | 6 | yes | a remote call failed (relay connection, admin HTTP request, nip-05 fetch) |
| `auth` | 7 | no | wrong credentials or a rejected signature |
| `internal` | 1 | no | anything else (fallback bucket) |

`input`, when present, is the single specific value that caused the
failure (an identifier, file path, relay URL, `--kinds` token, ...) —
omitted when there's no one clean value, or when the value is private-key
material (never echoed, even malformed). `retryable` lets an agent decide
whether to back off and retry (`network`/`conflict`) or fix the input and
try again (everything else) without string-matching the message. No
command double-reports the same failure in two shapes, and a usage mistake
in `--json` mode skips the human-readable help dump (which would otherwise
land on stdout) in favor of the structured error alone.

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
  decoding a NIP-19 entity (`decode`) → `skills/ncli-identity/SKILL.md`
- Mining or verifying proof-of-work (`miner`) → `skills/ncli-miner/SKILL.md`

These skills assume only the `ncli` binary is available — no access to this
source tree. (For building/testing `ncli` itself from source, see
`.claude/skills/verify` instead, which is scoped to contributors.)

Contributing a change to this repo (not just using the `ncli` binary)?
Work in a branch, keep commits atomic, update CHANGELOG.md alongside the
behavior it describes, and open a PR against `main` (protected — no direct
pushes).
