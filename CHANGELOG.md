# Changelog

## [0.4.0]

### Added

- `ncli blossom` — a client for the Blossom protocol (BUD-01..12):
  content-addressed blob storage authenticated with a Nostr identity
  instead of a login. `upload`/`download`/`list`/`rm`/`mirror`/`report`
  cover the direct blob operations; `servers add`/`remove`/`list`
  manage the default server list these fall back to when not given an
  explicit `--server`, with `--publish` broadcasting it as a signed
  kind:10063 (BUD-03) event to your relays and `servers discover
  <identifier>` fetching another identity's published list the same
  way. `upload`/`rm`/`mirror` fan out to every configured server and
  report a result per (item, server) pair, matching `publish`'s
  (event, relay) report shape; `download` instead falls back through
  the configured servers in order, stopping at the first that answers.
  Every subcommand that takes a target identity (`list`, `download`
  with `--identity`, `servers discover`) accepts the same shapes as
  `--identity` everywhere else in `ncli` — vault label, nsec, npub,
  hex, nprofile, or nip-05 — resolved before it's sent anywhere.
  Signing is local-key only for now (no `bunker`/NIP-46 backend). (#18)

## [0.3.0]

### Added

- `ncli bunker` — run `ncli` as a NIP-46 remote signer: listen on one or
  more relays for `connect`/`sign_event`/`get_public_key`/`get_relays`/
  `nip04_*`/`nip44_*` requests from other Nostr clients, approve or reject
  each one from a live TUI (matching `apply`'s own art direction and
  chrome), and remember per-app decisions so you aren't re-prompted for
  every action. Every app that completes pairing shows up in the TUI's
  Trusted Apps panel right away, whether or not you've remembered any
  specific permission for it yet. Pending Requests can also auto-advance
  from one request straight to the next as each is decided — on by
  default, toggle with "p" — so a client walks through connect and its
  first few requests without you hunting down and selecting each row by
  hand; "Decide Later" on the approval dialog backs out of a request
  without deciding it and switches auto-advance off, so it never turns
  into an unclosable loop on the same undecided request. A remembered
  grant is the product of three independent
  axes — scope (method, and for `sign_event`, optionally one specific
  kind), duration (1h/24h/7d/until revoked), and budget (next N uses) —
  with kinds 0/3/5 (metadata/contacts/deletion) always excluded from a
  broad any-kind grant, only ever covered by one that names them
  explicitly. On Linux/macOS, closing the TUI ("b", from any panel) offers
  detaching (the daemon keeps running in the background; reattach with
  `ncli bunker attach`) or stopping it entirely — a background daemon is
  otherwise unaffected by the terminal that started it closing. The
  footer's own hints track whichever panel is currently focused, showing
  only that panel's own keys. `ncli
  bunker status`/`stop`/`sessions list`/`sessions revoke <pubkey>`/
  `sessions rename <pubkey> <name>`/`connect [nostrconnect-uri]` manage a
  running daemon without opening the
  TUI (`--json` supported); `status`/`stop` also report the identity's
  vault label (`vault_label`), when the signing identity is a saved vault
  entry, alongside its resolved name/nip05. Windows runs the same TUI
  directly in the foreground with no background/attach support (a
  documented platform gap). Supports both connection flows: `bunker://`
  (this signer generates the pairing secret; a client pastes it in) and
  `nostrconnect://` (a
  client generates it; this signer initiates once given the URI via
  `ncli bunker connect`). Both are also reachable without leaving the
  TUI via the "c" key — board-wide, reachable from any panel, the same
  as "b" — which offers a "Copy" button (OSC 52 first — reaches the
  local clipboard even over SSH/tmux with no clipboard tool installed
  remotely — then a native clipboard tool as fallback) alongside the URI
  shown on its own selectable line. A Request History panel/`ncli bunker
  history` command lists recently resolved requests (Approved/Rejected,
  optionally "(always)" for one that also created a remembered grant, or
  Expired), most recent first, alongside Pending Requests rather than
  replacing it — a request moves there the moment it's decided rather
  than just disappearing; the two sit side by side in one row in the TUI
  rather than stacked, since they're the two panels worth watching
  together. Selecting any `sign_event` row in Request History (approved,
  rejected, or expired) opens its full event JSON, with a Copy button
  suggested alongside Close — the real signed event if it was approved,
  or the original unsigned request if nothing was ever signed; the
  Pending Requests approval dialog shows that same request's unsigned
  JSON above its own Approve/Reject buttons, so you can see exactly what
  you're about to sign before deciding either way.
  Request History is now durably persisted (`events.wal`, fsync'd per
  write, bounded to the most recent 200 entries) instead of resetting on
  every restart; a request still undecided when the daemon last stopped
  running shows up afterward as Expired rather than vanishing without a
  trace or silently resuming, since a NIP-46 client already resends an
  unanswered request on its own. Trusted Apps also shows each app's own
  self-reported name (nostrconnect:// pairings only — bunker:// has no
  equivalent at the protocol level), how long ago it first paired, how
  recently it last actually made a request (from Request History, "-" if
  it hasn't asked for anything yet), and which `sign_event` kinds it can
  currently sign without asking, alongside the existing pubkey and full
  grant detail. Rows sort by that last-request time, most recently used
  first, with apps that haven't made a request at all sinking to the
  bottom, rather than sitting in whatever order `sessions list` happens
  to return. Trusted Apps also has an "n" shortcut ("Set Name") that
  opens a modal for giving an app your own name (or `ncli bunker sessions
  rename <pubkey> <name>` outside the TUI, `""` to clear it) — useful for
  telling apart two `bunker://` pairings, which otherwise both show up
  nameless. In Trusted Apps itself this shows up in the Name column,
  preferred there over the app's own self-reported name — App keeps
  showing the raw pubkey either way. Everywhere else an app is identified
  (Pending Requests, the approval dialog, Request History, `sessions
  list`/`history`'s own text output, the daemon's activity log), there's
  no separate name/pubkey split, so the name is preferred over the raw
  pubkey directly. Ctrl+C in the
  TUI stops the daemon outright (the same as choosing "Stop bunker
  entirely" from "b"'s own dialog, but without asking first) instead of
  silently detaching and leaving it running unattended. The identity
  line always shows the signing identity's npub alongside any resolved
  name/nip05, not only as a fallback when neither has resolved yet. The
  "no relay connected" alert and each relay's own status bullet withhold
  judgment for the first instant after startup (a distinct "still
  trying its first connection" state) rather than flashing a false
  alarm before any relay has even had a chance to dial yet.
  Trusted Apps' grants no longer have to be revoked all at once: "Enter"
  on an app's row opens Manage Grants, listing every grant it holds
  individually with its own revoke ("x") and extend/re-scope ("e", the
  same duration/budget choices the approval dialog's own second step
  offers) — "r" on the row itself still revokes everything at once.
  `ncli bunker sessions grants <pubkey>`/`sessions revoke-grant <pubkey>
  --method ... [--kind N]` are the scriptable equivalents. `ncli bunker
  connect --grants <file>` pre-authorizes a whole pairing attempt in one
  step from a `kind: bunker` YAML spec (the same `kind:`/`spec:` envelope
  `ncli apply` uses) declaring method/kind scope, verdict (allow/deny),
  and an optional expires duration or use budget per grant, plus an
  optional nickname — resolved against the moment the app actually pairs,
  not when the file was loaded, so a bunker:// URI that sits unused
  doesn't burn its own grant's clock early. A "connect" request completes
  without ever going through the usual approval queue when a spec was
  staged for it this way — unlike an unscripted pairing, which still
  queues for a human even once the secret checks out — making unattended
  agent pairing genuinely possible for the first time; see
  `examples/bunker/` for a ready-to-copy everyday-client and
  unattended-agent spec. (#15)

## [0.2.0]

### Added

- `ncli id sign` — sign an unsigned event (or an array of them) with a
  vault/nsec identity, writing the result back in the same
  single-object-or-array shape it was read in so it chains straight into
  `ncli publish --events`/`ncli miner check --events` with no reshaping.
  Rejects a pubkey-only identity (no private key to sign with) and a
  pubkey conflict between the event and the resolved identity.

### Changed

- `ncli ping`'s live board is now opt-in via `--tui` instead of firing
  automatically whenever stdout is a real terminal. Results narrate as
  plain log lines by default; pass `--tui` to get the interactive board
  back (still falls back to plain under `--json`/`--quiet`, or without a
  real terminal).
- `ncli id delegate` no longer has any relation to `nip11`/`relay.yaml`,
  and `--issuer-key`/`--relay-key` are renamed to `--issuer`/`--delegatee`
  (env var `NCLI_DELEGATE_ISSUERKEY` renamed to `NCLI_DELEGATE_ISSUER`).
  `--delegatee` (not `--relay`) is deliberate: this command has no
  relation to relay config, so a flag named "relay" would misleadingly
  suggest a `wss://...` URL like every other relay-adjacent flag in this
  CLI. Both now accept the same identifier shapes `ncli id sign --identity`
  does — a vault label, nsec, npub, hex, nprofile, or nip-05 — resolved via
  the vault (`NCLI_VAULT_PASSWORD`) the same way. Both are always required
  (no more falling back to `nip11.privkey` from config) and must resolve to
  a private key; a bare hex string is now always read as a public key
  (never inferred as a raw private key, matching `id sign`/`miner mine`
  elsewhere), and a pubkey-only identity is rejected with `code: "auth"`.
  Output is `issuer_pubkey`/`delegatee_pubkey`/`conditions`/`token` plus
  the literal `["delegation", issuer, conditions, token]` tag to attach to
  an event, instead of a paste-into-`relay.yaml` `nip11:` block. The relay
  itself no longer verifies or signs under a NIP-26 delegation
  (`nip11.delegation` is no longer a config field — a `relay.yaml` that
  still sets it is now silently ignored rather than validated at startup).
  (#1)


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
