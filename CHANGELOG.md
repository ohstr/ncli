# Changelog

## [0.2.0]

### Changed

- `ncli ping`'s live board is now opt-in via `--tui` instead of firing
  automatically whenever stdout is a real terminal. Results narrate as
  plain log lines by default; pass `--tui` to get the interactive board
  back (still falls back to plain under `--json`/`--quiet`, or without a
  real terminal).


## [0.1.0]

Initial public release.

### Added

- `ncli relay` — run a Nostr relay server: NIP-11 metadata, auth, retention,
  optional Meilisearch-backed search, and an optional signed "top zapped"
  cache response (`cache.topZapped.enabled`, requires `nip11.privkey`). The
  default time window for "top-zapped" queries that omit their own is
  configurable via `cache.topZapped.window` (duration string, e.g.
  `24h`/`7d`/`2w`/`1mo`; falls back to 24h). Session behavior
  (`outgoingBufferSize`, `maxConcurrentStoreTasks`, `verificationWorkers`)
  and NIP-11 limits (`nip11.limitation.*`, including
  `pow.min`/`pow.strict`) are configurable in relay.yaml.
- `membership:` relay.yaml config block (`enabled`, `inviteTTL`,
  `inviteMaxUses`, `publishAddRemoveEvents`) enabling NIP-43 group
  membership, and `nip11.limitation.membership_required` (requires
  `membership.enabled` and `auth_required`), plus a dedicated
  `examples/relay/membership.yaml` preset.
- `agent_auth:` relay.yaml config block (`enabled`, `freshnessWindow`,
  `kindEnforcement`) enabling NIP-AA: an agent key gains virtual
  membership from its owner's NIP-43 membership via a NIP-OA credential,
  no separate enrollment. Requires `membership_required`.
- `ncli relay stats`/`reindex`/`clear` — remotely administer a running
  relay over NIP-98 auth: stats, trigger a search/zap reindex, or clear
  the search index/zap counters (`--json` for scripts).
- `ncli relay members/invites/roles` — administer a running relay's NIP-43
  group membership over NIP-98 HTTP: list/show/add/remove members,
  issue/list/revoke invite codes, and define roles.
- `ncli relay context add/remove/use` — save named `--config` shortcuts
  and switch the current one, so `relay` subcommands stop needing
  `--config` repeated on every invocation.
- `ncli apply` — run a `stream` (live event forwarding), `sync` (negentropy
  reconciliation between one local store and one remote relay), or
  `inspect` (read-only query) workflow from a YAML config file. `stream`
  detects headless environments (no TTY, or `--quiet`) and skips the
  interactive TUI automatically (or force it with `raw: true` even under a
  real terminal); `sync`/`inspect` currently require an interactive
  terminal and error immediately if run headlessly. A stream's delivery to
  multiple destinations is concurrent (a slow destination doesn't block
  the others), local writes go through a tunable worker pool
  (`writeConcurrency`, default 32), and an at-least-once delivery recovery
  log is always on, with a final bounded flush on shutdown that
  Ctrl-C/SIGTERM waits for. The interactive TUI includes an event detail
  dialog and event table for inspecting individual events as they arrive.
- `ncli ping` — probe whether targets are reachable (connect + subscribe),
  no events fetched.
- `ncli publish` — publish one or more events to one or more relays,
  reporting per-(event, relay) accept/reject outcomes (`--json` for
  scripts). Exits non-zero if any target rejects.
- `ncli find` — look up events by ID and/or filter (a `--targets` YAML
  file, or inline flags) across one or more relays/local stores; falls
  back to the configured default relays (`ncli prefs relays`) if no
  explicit targets are given.
- `ncli dump` — export events to JSON from a local store or live relay(s),
  optionally filtered; falls back to the configured default relays if no
  explicit source is given.
- `ncli miner mine` / `ncli miner check` — mine or verify NIP-13
  proof-of-work for an event. Mining uses multiple CPU cores by default
  (`--workers`) with periodic progress reporting (`--progress-interval`);
  verification can check a saved `ncli dump` file or fetch events live
  from relays. Both support `--identity` to scope to one vault identity's
  events and `--json` for scripts.
- `ncli id` / `ncli id list` — generate or inspect a Nostr identity (hex,
  nsec, npub; resolves a saved vault label, npub, hex pubkey, nsec,
  nprofile, or a nip-05 `name@domain` address), with an optional local
  vault: the vault's own key is NIP-49-encrypted under a password, and
  each saved identity is NIP-44-encrypted under a key derived from the
  vault key. `--reveal` decrypts a saved identity's private key; `--json`
  disables interactive prompts for scripting/agent use.
- `ncli id delegate` — generate a NIP-26 delegation token via an
  interactive wizard, or non-interactively with
  `--issuer-key`/`--relay-key`/`--kinds`/`--duration`/`--json` (required
  when stdin/stdout isn't a real terminal).
- `ncli prefs relays add/remove/list/clear` and `ncli prefs path` — a
  persistent default relay list, stored outside any single spec file, that
  `dump`/`find` fall back to. All support `--json`.
- `ncli version` — print the ncli build version, the resolved
  `github.com/ohstr/nmilat` dependency version, and (`--json`) the app
  data directory and prefs/vault/log file paths.
- Config loading from a YAML file (`--config`, an `ncli.yaml`/`relay.yaml`
  in the working directory, a saved `ncli relay context`, or
  `NCLI_`-prefixed environment variables, in that priority order), with
  ready-made relay presets (`open`, `auth`, `ephemeral`, `minimal`,
  `membership`, `full`, `cache-search`) under `examples/relay/`.
- Every command reports failure with a non-zero exit code and a structured
  `usage`/`invalid_input`/`not_found`/`conflict`/... error code (see
  AGENTS.md), including group commands invoked without a subcommand. Text
  output is human-readable on stdout by default and switches to JSON with
  `--json`; log narration always goes to stderr, so scripts and agents can
  parse stdout as clean data.
- Prebuilt release archives for Linux/macOS/Windows (amd64 + arm64), a
  Homebrew tap (`ohstr/ncli`), and a multi-arch Docker image
  (`ghcr.io/ohstr/ncli`) with a `:latest` (tagged release) and `:edge`
  (`main`) track.
