# Changelog

## [0.9.0-rc.1]

### Added

- `ncli huddle list` shows which huddles are live on a relay right now, with
  each room's peer count and the protocol version it is pinned to. A room id
  was otherwise out-of-band knowledge, and `huddle join` on an unused id opens
  that room rather than failing -- so a typo put you alone in a new call with
  nothing to warn you. Backed by a new `GET /huddle/rooms`, gated exactly like
  a join: open on an open relay, members-only where `huddle.requireMembership`
  is set. Rooms exist only while occupied, so nothing ended or empty is
  listed. (#87)

## [0.8.0-rc.1]

### Added

- `ncli relay` serves NIP-86, the Relay Management API, when a `nip86:` block
  turns it on: membership administration over HTTP on the relay's own URL, so a
  host's app can add or remove a guest and mint an invite code without a
  terminal. It answers the CORS preflight a browser requires, and NIP-11
  advertises 86 so a client can detect it. `nip86.admins` is what makes this
  usable from an app -- see below. (#85)
- `nip86.admins` lists pubkeys allowed to administer the relay, alongside
  `nip11.pubkey`. Until now the relay's own key was the only admin identity, so
  administering meant holding the relay's secret key; a host can now administer
  from their own key instead. (#85)
- The NIP-86 surface covers roles too (`createrole`, `editrole`, `deleterole`,
  `assignrole`, `unassignrole`), mapping onto the kind:33534 definitions
  `ncli relay roles` already manages. (#85)
- `huddle.udpPortRange` pins the UDP ports WebRTC media is carried on, as
  `"min-max"`. Without it the OS picks from the ephemeral range, which a
  container cannot publish and a firewall will not have open -- so signalling
  succeeds and no audio ever arrives. (#82)

### Changed

- Removing a member now ends that member's live huddle calls instead of only
  blocking their next join. The door checks admission once, at join, so a
  removed guest previously kept hearing a room until they chose to reconnect.
  The call's other participants are unaffected. (#85)
- Admin HTTP requests bind their body to the NIP-98 signature with a `payload`
  tag. A captured `Authorization` header was previously good for any body at
  the same URL and method until it expired, and these routes sit on the public
  relay port. The relay verifies the tag when a client sends one, so an older
  `ncli` keeps working. (#85)

### Fixed

- WebRTC huddles no longer fail to connect when trickled ICE candidates arrive
  before the SDP they belong to. A candidate that outran its description was
  logged and discarded rather than held, which cost the fastest paths and, off
  the local network, often every reachable one -- the call then sat in
  `connecting` until ICE gave up. Candidates are now queued in both directions
  and released once the matching description is in place. (#82)
- `ncli bunker` could not pair with any real app over `nostrconnect://`.
  The URI was rejected for lacking a `metadata` param NIP-46 does not
  define, and past that the handshake ran backwards: ncli sent a `connect`
  request and waited for the client to echo the secret, where the spec has
  the signer publish a `connect` response and the client answer nothing.
  Fixing only the first would have turned the error into a silent timeout.
  (#84)
- A `nostrconnect://` URI's relays are all used, not just the first. They
  are dialed in parallel and published to as each connects, so one dead
  relay at the head of the list neither delays nor fails a pairing the
  others can carry. Every relay failing is now a `network` error (exit 6,
  retryable) rather than `invalid_input`. (#84)
- The permissions an app requests in its URI are applied as grants, so a
  freshly paired app stops prompting on every request. `bunker://` pairings
  get the same from `connect`'s own params, along with the app's name and
  URL -- previously dropped, which is why every app paired that way showed
  as a bare hex key. (#84)
- `switch_relays` and `logout` are answered instead of being reported
  unsupported. A compliant client sends `switch_relays` right after every
  pairing, so a signer's own relay list never took effect. (#84)
- A relay named only by a pairing URI no longer keeps a reconnect loop
  running for the daemon's lifetime when it never comes up. (#84)
- The "Paste nostrconnect:// URI" and "Set App Name" dialogs no longer
  stretch to the full width of the terminal with most of their height
  empty. (#84)

## [0.7.0-rc.1]

### Added

- `ncli huddle join` can now play the call, decoding Opus with pure Go and mixing
  every speaker into one stream. Playback is compiled in only under
  `-tags huddleaudio`: the release binaries are built without cgo and the only
  maintained pure-Go output library needs cgo and ALSA on Linux. Without the tag
  the roster still works and the status line says "watching only" rather than
  leaving a silent call looking like a working one. (#76)
- Each speaker gets their own Opus decoder, because a decoder carries stream state
  across frames -- sharing one would corrupt every speaker in the room. A
  departing peer's decoder is dropped rather than kept for the life of the call.
  (#76)
- The mix sums in 32-bit and clamps, so several loud speakers at once clip instead
  of wrapping. A wrap would turn a loud moment into a loud click, which is far
  more noticeable. (#76)
- `ncli huddle join <room>` joins a relay's voice room and shows a live roster:
  who is present, who is speaking, and at what level. Speaking is read from the
  telemetry every audio frame already carries, held briefly so a gap between
  words does not blink the indicator off, and rows never reorder as people talk.
  (#66)
- Joining never captures a microphone, so it puts no audio into the room. There
  is no pure-Go capture library, and `ncli` ships every target with
  `CGO_ENABLED=0`, so capture waits for an opt-in build tag of its own. Mute and
  raise-hand are absent for the same reason rather than present and inert. (#66)
- A refused join is explained rather than reported as a bare error -- a relay
  without huddles enabled, a full room, an ended call, or a protocol-version
  mismatch each say so, and an unrecognized code shows the relay's own message
  instead of a guess. (#66)
- WebRTC peers in a huddle now exchange **video and screen share**, which the
  WebSocket transport cannot carry. A publisher's track ids come through
  unchanged, so a receiver tells a camera from a screen share exactly as the
  publisher labelled them, and a peer may publish both at once. (#66)
- Video takes a separate path from audio on purpose. Audio goes through the
  huddle room, which is what lets a WebSocket peer hear the call; video is
  forwarded only among the WebRTC peers. So a buzz client in a screen-share
  call hears the call and misses only the picture, rather than being sent
  bytes it has no way to use. (#66)
- A subscriber joining mid-stream is sent a keyframe request, so video appears
  at once instead of waiting out the publisher's next natural keyframe. (#66)
- `huddle.rtc` mounts a WebRTC endpoint at `/huddle/{id}/rtc`, so a browser
  can join the same voice rooms as a client on the WebSocket endpoint. Both
  share one set of rooms: the same room id is the same call, not two. A
  speaker's Opus is repacketized between RTP and huddle frames and never
  decoded, so the relay still links no codec. (#66)
- `huddle.iceServers` configures STUN/TURN for those clients. With none, only
  peers on the same network connect; TURN is what carries peers behind
  symmetric NAT. (#66)
- `ncli relay` can host real-time voice. A new `huddle:` config block mounts
  an audio endpoint at `/huddle/{id}/audio`, where peers authenticate with
  NIP-42 and relay Opus frames to each other. Audio gets its own WebSocket
  rather than sharing the Nostr socket, which decodes every frame as JSON and
  would drop the session on the first binary frame -- so a client connects to
  both. (#66)
- `huddle.requireMembership` restricts joining to relay members (NIP-43). It is
  independent of `nip11.limitation.membership_required`, which gates the Nostr
  socket: a relay may want open reading and closed calls, or the reverse. (#66)
- `huddle.enabled` requires `nip11.url`, since a joining client's NIP-42 event
  names this relay and the endpoint has to know what to validate against.
  Omitting the block, or disabling it, mounts nothing -- a client then sees the
  same 404 an older relay gives it, rather than a huddle-specific error. (#66)
- `huddle.allowedOrigins` restricts browser origins, and is empty by default.
  Admission is gated by a signed NIP-42 challenge, so Origin is not the security
  boundary here, and restricting it by default would lock out web clients while
  every CLI client kept working. (#66)
- `examples/relay/huddle.yaml` documents every field of the block, as a preset to
  copy from. (#66)

### Fixed

- `ncli relay` now ends live huddles before shutting down.
  `http.Server.Shutdown` never waits on hijacked WebSocket connections, so
  without this a restart would sever calls with no notice and leave
  participants waiting for audio that had simply stopped arriving. (#66)

## [0.6.0]

### Added

- `ncli profile <identifier>` prints a readable profile card for one
  identity -- metadata, following count, relay list, Blossom servers and
  lightning address -- from a single query, aggregated across every relay
  rather than stopping at the first hit. `--json` for the structured
  shape; `--no-verify` skips the nip-05 check. (#55)
- A spinner on stderr while any command waits on the network. Off under
  `--json`, `-q/--quiet`, `NO_COLOR`, and whenever stderr isn't a
  terminal. (#55)
- `client/vault` and `client/prefs` are importable on their own, for code
  that wants the vault or the relay list without the rest of `client`.
  Importing them pulls 10 modules instead of 38 -- no TUI, no viper, no
  bbolt. `client` re-exports every previous name unchanged, so existing
  code needs no edits. (#60)

### Changed

- Failures now read like `cashctl`: a bare invocation prints help with no
  error line, a wrong one prints `Error: <msg>` (red on a TTY) above the
  help, and a runtime failure prints the error alone. All three go to
  **stderr** -- help used to land on stdout -- and exit codes are
  unchanged, so a bare group command still exits 2, never 0. `--json` is
  untouched: one structured line, never help. (#55)
- Updated `nmilat` to v0.4.0. (#58)
- Rewrote every command's `--help` description in a flatter style: each
  one now states what the command does, with the rules a caller can't
  guess stated plainly, instead of explaining how it works internally.
  (#61, #62)

### Fixed

- An unknown flag was reported twice (cobra's own `Error:` plus ncli's
  own line) and exited 1 instead of 2. (#55)
- `ncli bunker sessions revoke-grant` with no `--method` exited 1 as
  `internal` instead of 2 as `usage`. (#55)
- `ncli relay` with no config reported three alternatives crammed into
  one line and no help. It now prints a short error followed by the
  command's help, which lists the flags and where the config is read
  from. Same for the `relay` admin subcommands missing `nip11.privkey`.
  (#61)
- `ncli relay` could freeze until restarted: a `REQ` held its database
  read open while sending events, so a write that grew the database file
  hung every other `REQ` and `EVENT`, health checks included. Fixed
  upstream in `nmilat` v0.3.2. (#58)

## [0.5.0]

### Added

- `ncli decode` reads cash tokens (any HRP, e.g. `lokicash1...`) and
  `circlehub1...` connections. Pairing secrets are never shown.
  `cashhub1...` is recognized and rejected.
- Every command's `--help` has an `Example:`.

### Changed

- A local flow's `ensure` now defaults to `create` (was `exists`), so a
  missing store path is created instead of failing.
- The Age column shows days and weeks (`2d4h`, `1w3d`) instead of
  stopping at hours.
- Shorter, less repetitive `--help` text.
- Updated `nmilat` to v0.3.1.

### Fixed

- Wallet transfers reused a stale client on their second call.
- NWC responses dropped the `circle_hub`/`circle_wallet` fee fields.
- Two `Example:` commands failed when run as written.

## [0.5.0-rc.1]

### Added

- `ncli decode` extended to NIP-CASH tokens (`lokicash1...`, any HRP) and
  NIP-CW `circlehub1...` connections. A `cashhub1...` Hub connection is
  recognized and rejected -- no local decoder for that format. The
  pairing secret embedded in either new format is never surfaced, in any
  mode. ([#49](https://github.com/ohstr/ncli/pull/49))
- Every command's `--help` output now includes an onboarding `Example:`
  field, including pure group commands.
  ([#51](https://github.com/ohstr/ncli/pull/51))

### Changed

- A local flow's `ensure` policy now defaults to `create` instead of
  `exists` when omitted, so a missing local store path is created rather
  than failing to load.
  ([#50](https://github.com/ohstr/ncli/pull/50))
- Trimmed redundant/noisy `Long` text and placeholder examples across
  commands -- text already covered by a flag's own description no
  longer repeats in the command's `Long`, and the generic `mylabel`
  placeholder now reads `satoshi`.
  ([#51](https://github.com/ohstr/ncli/pull/51))
- Bumped `github.com/ohstr/nmilat` to v0.3.0 -- adds NIP-34/NIP-22
  support (consumed by the `decode` extension above), fixes
  `TransferFromSources` reusing a stale wallet-bound client on its
  second call, and fixes silently-dropped `circle_hub`/`circle_wallet`
  fee fields on NWC unmarshal.
  ([#51](https://github.com/ohstr/ncli/pull/51))

### Fixed

- Two `Example:` commands that didn't actually run as written against a
  real `ncli` binary.
  ([#51](https://github.com/ohstr/ncli/pull/51))

## [0.4.9]

### Fixed

- `apply stream`'s destination could silently drop an in-flight event
  during its own reconnect window instead of recovering it, and its Age
  column never reset on retry.
  ([#48](https://github.com/ohstr/ncli/pull/48))
- `apply sync`'s `timeouts:` spec block was silently a no-op.
  ([#48](https://github.com/ohstr/ncli/pull/48))
- `apply stream`'s destination could permanently leak a
  `writeConcurrency` publish slot and stall for good, either after many
  reconnects or from a large multi-source fan-in against one throttled
  destination alone -- the source kept receiving fine while the
  destination simply stopped.
  ([#48](https://github.com/ohstr/ncli/pull/48))

### Changed

- `apply stream`'s destination "Synced" TUI column renamed to
  "Duplicates" -- it counts duplicate acks, not overall sync progress.
  ([#48](https://github.com/ohstr/ncli/pull/48))

### Added

- Hermetic Docker Compose e2e coverage for `inspect`/`sync` (previously
  stream-only), plus dedicated stress suites (20 source relays for
  stream, 15 targets for inspect).
  ([#48](https://github.com/ohstr/ncli/pull/48))

## [0.4.8]

### Fixed

- `apply stream` could overwhelm a destination relay on initial sync --
  an unpaced burst from a large `from` pool exceeded its concurrency
  guard and got rejected. `FlowSpec.WriteConcurrency` now also bounds
  remote destinations (previously local-only).
  ([#45](https://github.com/ohstr/ncli/pull/45))
- Bumps `github.com/ohstr/nmilat` to v0.2.9: bounds a previously-unbounded
  write that could stall subscription delivery, and fixes a
  use-after-transaction-scope bug causing corrupted event JSON under load.
  ([nmilat#19](https://github.com/ohstr/nmilat/pull/19))

## [0.4.7]

### Fixed

- `ncli relay`'s live scans on a multi-kind filter could starve other
  kinds during a burst on one busy kind. Bumps `github.com/ohstr/nmilat`
  to v0.2.8, which fixes this relay-side; bounded queries are unaffected.
  ([nmilat#15](https://github.com/ohstr/nmilat/pull/15),
  [#41](https://github.com/ohstr/ncli/pull/41))
- Also picks up a `nmilat` NWC client fix for untagged NIP-44 v2
  responses misparsed as NIP-04.
  ([nmilat#14](https://github.com/ohstr/nmilat/pull/14),
  [#41](https://github.com/ohstr/ncli/pull/41))

## [0.4.6]

### Added

- `ncli relay` now also declares and validates NIP-47 (Nostr Wallet
  Connect), NIP-48 (Proxy tags), NIP-88 (Polls), NIP-90 (Data Vending
  Machines), NIP-AZ (AltZap), NIP-B0 (Web bookmarks), and NIP-B7 (Blossom
  server lists) -- nmilat ships a `relayreg` subpackage for each that
  `ncli relay` simply never blank-imported before.

### Fixed

- `apply stream`/`sync` no longer silently drop an event at the destination
  relay just because its `nonce` tag overclaims its NIP-13 difficulty --
  regardless of `strictPow`/`--strict-pow`, since that setting only ever
  governed ncli's own read-side check, never the relay's. Bumps
  `github.com/ohstr/nmilat` to v0.2.7, which fixes this on the relay side.
  ([#33](https://github.com/ohstr/ncli/pull/33))
- `ncli relay` never actually advertised NIP-57 (Zaps) or NIP-65 (Relay
  List Metadata) support, or validated their event kinds -- the blank
  imports pointed at the base `nip57`/`nip65` packages instead of their
  `relayreg` subpackages, so the registration that declares them in
  `supported_nips` never ran.
  ([#38](https://github.com/ohstr/ncli/pull/38))

## [0.4.5]

### Added

- `ncli relay context list` explicitly lists saved relay contexts, same
  as the pre-existing bare `ncli relay context` invocation.
  ([#31](https://github.com/ohstr/ncli/pull/31))

## [0.4.4]

### Added

- `ncli relay -c/--context <name>` runs a saved relay context directly,
  skipping `context use` -- and creates one on the spot (a minimal config
  backed by a freshly generated, vault-saved identity, or an existing
  `--identity`) if that name isn't saved yet.
  ([#30](https://github.com/ohstr/ncli/pull/30))

### Changed

- A saved `ncli relay context` now wins over a local `ncli.yaml`/
  `relay.yaml` in the working directory, instead of silently losing to
  it. ([#30](https://github.com/ohstr/ncli/pull/30))

### Fixed

- `ncli relay` and its admin subcommands (`stats`/`reindex`/`clear`/...)
  with no config source at all -- no `--config`, no current context, no
  local `ncli.yaml`/`relay.yaml` -- reported a confusing missing-field
  error instead of saying plainly that no config resolved.
  ([#30](https://github.com/ohstr/ncli/pull/30))

## [0.4.3]

### Fixed

- Saving a vault identity under an already-taken `--label` reported
  `code: "internal"` instead of the documented `conflict`.
  ([#28](https://github.com/ohstr/ncli/pull/28))

## [0.4.2]

### Added

- A new `unsupported` error code (exit 8) distinguishes "this server
  doesn't support the requested capability at all" (e.g. `blossom list`
  against a server with BUD-02 disabled) from `not_found`'s "this one
  resource is missing". ([#27](https://github.com/ohstr/ncli/pull/27))

### Fixed

- Several commands' argument-count checks (missing/extra positional args)
  reported `code: "internal"` instead of the documented `usage`, and
  printed cobra's help dump even under `--json`.
  ([#27](https://github.com/ohstr/ncli/pull/27))
- `ncli id --save` with a missing or wrong `NCLI_VAULT_PASSWORD` reported
  `code: "internal"` instead of `usage`/`auth`.
  ([#27](https://github.com/ohstr/ncli/pull/27))
- `ncli blossom mirror` always failed against servers that require a
  BUD-11 auth token scoped to the source blob's hash.
  ([#27](https://github.com/ohstr/ncli/pull/27))
- `ncli bunker history` came back empty for requests a standing grant
  auto-approved, instead of recording them.
  ([#27](https://github.com/ohstr/ncli/pull/27))
- `ncli relay`'s shutdown no longer leaks its verification-worker
  goroutines, and no longer hangs indefinitely if shutdown gets stuck.
  ([#27](https://github.com/ohstr/ncli/pull/27))

## [0.4.1]

### Changed

- Every bordered TUI panel now dims its border while it doesn't hold
  keyboard focus, and bunker's form dialogs support Left/Right button
  navigation. ([#23](https://github.com/ohstr/ncli/pull/23))
- `ncli bunker`'s TUI now shows its splashscreen for the full duration of a
  slow startup instead of skipping straight to an incomplete board.
  ([#24](https://github.com/ohstr/ncli/pull/24))
- CLI help text and CHANGELOG.md entries are trimmed for conciseness, with
  no change in behavior. ([#22](https://github.com/ohstr/ncli/pull/22))

## [0.4.0]

### Added

- **`ncli blossom`** — a client for the Blossom protocol: upload, download,
  list, remove, mirror, and report blobs across your configured servers
  (`servers add`/`remove`/`list`/`discover`), using the same identity
  shapes as the rest of `ncli`. ([#18](https://github.com/ohstr/ncli/pull/18))

### Changed

- Every TUI board (`apply stream`/`sync`/`inspect`, `ping --tui`, `bunker`)
  now renders with a fixed truecolor palette instead of the terminal's own
  ANSI-16 theme, fixing a couple of black-on-black legibility bugs along the
  way. (#19)

## [0.3.0]

### Added

- **`ncli bunker`** — run `ncli` as a NIP-46 remote signer: approve or
  reject signing requests from a live TUI, with remembered per-app grants
  and both `bunker://`/`nostrconnect://` pairing. `status`/`sessions`/
  `connect` manage a running daemon without opening the TUI, for
  scripting. ([#15](https://github.com/ohstr/ncli/pull/15))

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
