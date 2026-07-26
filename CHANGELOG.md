# Changelog

## [0.4.1]

### Changed

- Every bordered TUI panel now dims its border while it doesn't hold
  keyboard focus, and bunker's form dialogs support Left/Right button
  navigation. (#23)
- `ncli bunker`'s TUI now shows its splashscreen for the full duration of a
  slow startup instead of skipping straight to an incomplete board. (#24)
- CLI help text and CHANGELOG.md entries are trimmed for conciseness, with
  no change in behavior. (#22)

## [0.4.0]

### Added

- **`ncli blossom`** — a client for the Blossom protocol (BUD-01–12):
  content-addressed blob storage authenticated with a Nostr identity instead
  of a login.
  - `upload` / `download` / `list` / `rm` / `mirror` / `report` cover direct
    blob operations; `upload`/`rm`/`mirror` fan out across every configured
    server, while `download` falls back through them in order.
  - `servers add` / `remove` / `list` manage the default server list, with
    `--publish` broadcasting it (BUD-03) and `servers discover <identifier>`
    fetching another identity's published list.
  - Accepts the same identity shapes as the rest of `ncli` (vault label,
    nsec, npub, hex, nprofile, nip-05).
  - Signing is local-key only for now — no `bunker`/NIP-46 backend. (#18)

### Changed

- Every TUI board (`apply stream`/`sync`/`inspect`, `ping --tui`, `bunker`)
  now renders with a fixed truecolor palette instead of the terminal's own
  ANSI-16 theme, fixing a couple of black-on-black legibility bugs along the
  way. (#19)

## [0.3.0]

### Added

- **`ncli bunker`** — run `ncli` as a NIP-46 remote signer, approving or
  rejecting `connect`/`sign_event`/other requests from a live TUI.
  - Remembers per-app decisions (scope, duration, and use-budget) so you
    aren't re-prompted every time; sensitive kinds (metadata/contacts/
    deletion) can never be covered by a broad any-kind grant.
  - Auto-advances through a client's pairing and first few requests, with a
    "Decide Later" escape hatch that turns auto-advance off.
  - Supports both `bunker://` and `nostrconnect://` pairing, with in-TUI URI
    copy (OSC 52, falling back to a native clipboard tool).
  - Trusted Apps shows each app's name, pairing/activity history, and
    current grants, with per-grant revoke/extend (Manage Grants) and a
    rename shortcut.
  - Request History persists across restarts; a request still undecided
    when the daemon last stopped shows up as Expired.
  - Detach/reattach a running daemon on Linux/macOS (`ncli bunker attach`);
    Windows runs the TUI in the foreground only.
  - `ncli bunker status` / `stop` / `sessions list` / `sessions revoke` /
    `sessions rename` / `connect` manage a daemon without opening the TUI
    (`--json` supported).
  - `ncli bunker connect --grants <file>` pre-authorizes an entire pairing
    from a YAML spec, so a request completes with no human in the loop —
    see `examples/bunker/` for ready-to-copy specs. (#15)

## [0.2.0]

### Added

- **`ncli id sign`** — sign an unsigned event (or array of events) with a
  vault/nsec identity, chaining directly into `ncli publish --events` /
  `ncli miner check --events`.

### Changed

- `ncli ping`'s live TUI board is now opt-in via `--tui` instead of firing
  automatically on a real terminal; plain log lines are the default.
- `ncli id delegate` is no longer tied to `nip11`/`relay.yaml`:
  `--issuer-key`/`--relay-key` are renamed `--issuer`/`--delegatee`, both now
  accept any identity shape (vault label, nsec, npub, hex, nprofile,
  nip-05), and output is `issuer_pubkey`/`delegatee_pubkey`/`conditions`/
  `token` instead of a paste-into-`relay.yaml` snippet. The relay itself no
  longer verifies or signs under a NIP-26 delegation. (#1)

## [0.1.0]

Initial public release.

### Added

- `ncli relay` — run a Nostr relay server: NIP-11 metadata, auth, retention,
  optional Meilisearch-backed search, and a signed "top zapped" cache.
- NIP-43 group membership (`membership:` config block) and NIP-AA agent
  auth (`agent_auth:` config block).
- `ncli relay stats` / `reindex` / `clear` and `ncli relay members` /
  `invites` / `roles` — remotely administer a running relay over NIP-98
  auth.
- `ncli relay context add` / `remove` / `use` — named `--config` shortcuts.
- `ncli apply` — run a `stream` (live forwarding), `sync` (negentropy
  reconciliation), or `inspect` (read-only query) workflow from a YAML
  config file, with a live TUI for monitoring a stream.
- `ncli ping` — probe whether targets are reachable.
- `ncli publish` — publish events to one or more relays, reporting a
  per-(event, relay) accept/reject outcome.
- `ncli find` / `ncli dump` — look up or export events by ID/filter, from a
  relay or local store.
- `ncli miner mine` / `ncli miner check` — mine or verify NIP-13
  proof-of-work for an event.
- `ncli id` / `ncli id list` — generate or inspect a Nostr identity, with an
  optional password-encrypted local vault.
- `ncli id delegate` — generate a NIP-26 delegation token.
- `ncli prefs relays` / `ncli prefs path` — a persistent default relay list.
- `ncli version` — print build/version info.
- Structured error codes and `--json` output on every command; human-
  readable text on stdout by default, log narration on stderr.
- Prebuilt release archives (Linux/macOS/Windows), a Homebrew tap, and a
  multi-arch Docker image.
