# Changelog

## [0.8.0-rc.31]

### Added

- Adds `logs.level` and `logs.file` to the relay config. Per-connection lines now need `debug`, and `file: false` stops writing `nrelay.log` when stderr is already kept. ([#168](https://github.com/ohstr/ncli/pull/168))

### Changed

- Writes `nrelay.log` as JSON lines, which costs less CPU per line and reads with `jq`. ([#168](https://github.com/ohstr/ncli/pull/168))
- Updates nmilat to v0.5.0-rc.31, which also escapes tags like content when computing event ids and tightens NIP-04, NIP-44, private key and NWC response checks. ([#175](https://github.com/ohstr/ncli/pull/175))

### Fixed

- Fixes relay search (NIP-50) returning profiles newest first; results now come best match first, with `limit` applied after ranking. ([#175](https://github.com/ohstr/ncli/pull/175))
- Fixes the relay's index job history (`<store>.jobs.json`) being left stale or unreadable when two jobs finished at once, losing `relay jobs list` history on restart. ([#172](https://github.com/ohstr/ncli/pull/172))
- Fixes the `apply` footer always showing Wrap/AutoScroll whatever panel is focused. It now shows the focused panel's own keys (delete, column sort, view/save event), then Restart, Save and Quit. ([#170](https://github.com/ohstr/ncli/pull/170))
- Fixes `r` in the `ping` TUI opening a Restart dialog that did nothing. ([#170](https://github.com/ohstr/ncli/pull/170))
- Fixes a `logs:` block without `filename` writing its log to a temp directory instead of `<logdir>/nrelay.log`. ([#168](https://github.com/ohstr/ncli/pull/168))

## [0.8.0-rc.30]

### Added

- Adds `nip11.banner` to the relay config, served in the NIP-11 document. ([#166](https://github.com/ohstr/ncli/pull/166))
- Adds the operator's latest notes to the relay homepage. ([#166](https://github.com/ohstr/ncli/pull/166))

### Changed

- The relay homepage hero is laid out like a Nostr profile: the banner on
  top with the icon over its edge. It uses the NIP-11 banner, or an ncli
  banner when there is none or it fails to load. ([#166](https://github.com/ohstr/ncli/pull/166))
- The dashboard, relay homepage and docs site use the ohstr favicon in place
  of the `n_` tile, which is removed. ([#166](https://github.com/ohstr/ncli/pull/166))

## [0.8.0-rc.29]

### Fixed

- Fixes `nip11.icon` in the relay config being dropped at startup, so the
  NIP-11 document had no icon unless one was set through NIP-86. ([#165](https://github.com/ohstr/ncli/pull/165))
- Fixes the relay homepage showing a broken image when the icon URL doesn't
  load; it falls back to the operator's picture, then the placeholder. ([#165](https://github.com/ohstr/ncli/pull/165))

### Changed

- The relay homepage's tab always shows the ncli favicon, no longer the
  relay's icon. ([#165](https://github.com/ohstr/ncli/pull/165))

## [0.8.0-rc.28]

### Added

- Adds the ncli logo to the relay dashboard and homepage, with a favicon on
  both. The homepage's tab switches to the relay's own NIP-11 icon once it
  loads. ([#164](https://github.com/ohstr/ncli/pull/164))

## [0.8.0-rc.27]

### Added

- Adds "remember on this browser" to the dashboard's secret-key sign-in: the
  key is saved only as a password-encrypted NIP-49 `ncryptsec` and unlocked
  with the password on the next visit. Left off, sign-in stays temporary. ([#162](https://github.com/ohstr/ncli/pull/162))
- Adds session renewal: after a key sign-in the dashboard renews its session
  while the tab stays open. ([#162](https://github.com/ohstr/ncli/pull/162))

### Fixed

- Fixes the dashboard staying on "reconnecting…" with failing panels when its
  session expired; it now returns to the sign-in screen. ([#162](https://github.com/ohstr/ncli/pull/162))

## [0.8.0-rc.26]

### Added

- Adds dashboard sign-in with a secret key, signed in the browser and never
  sent or stored, or with a NIP-46 remote signer: a `bunker://` URL or a
  `nostrconnect://` QR code for apps such as Amber and nsec.app. ([#160](https://github.com/ohstr/ncli/pull/160))
- Adds `admin.pubkeys`: personal keys that may sign in to the dashboard and
  use the admin API, so admins no longer need the relay's own key. ([#160](https://github.com/ohstr/ncli/pull/160))
- Adds a homepage at the relay's URL for browsers, built from its NIP-11, the
  operator's profile and its public groups; `homepage: false` turns it off. ([#160](https://github.com/ohstr/ncli/pull/160))

### Fixed

- Fixes browsers on any site but localhost being refused (HTTP 403) when they
  opened a WebSocket to `ncli relay`, which locked out every web client. ([#160](https://github.com/ohstr/ncli/pull/160))

## [0.8.0-rc.25]

Relay monitoring: Prometheus metrics, a one-page dashboard, and index jobs
that replace `relay stats`/`reindex`/`clear`.

### Added

- Adds an admin listener to `ncli relay` (`admin:` block, `127.0.0.1:<port+1>`
  by default) serving Prometheus metrics at `/metrics` to bearer tokens from
  `ncli relay metrics token add`. The relay refuses to start when it would be
  reachable off loopback without TLS, and collecting metrics adds no measurable
  latency. ([#154](https://github.com/ohstr/ncli/pull/154))
- Adds `ncli relay status`, `relay index list|rebuild|drop` and `relay jobs
  list|show|cancel`, which run index maintenance as jobs with progress,
  `--follow` and cancel. Rebuilds swap the new index in atomically, so search
  and top-zapped answers never go empty mid-rebuild. ([#155](https://github.com/ohstr/ncli/pull/155))
- Adds a one-page relay dashboard embedded in `ncli`: live traffic, latency,
  rejections, queues, access and process charts plus index jobs. The relay
  serves it with `admin.dashboard.enabled` (sign in with a NIP-07 extension),
  or `ncli relay dashboard` serves it locally and signs for you.
  ([#156](https://github.com/ohstr/ncli/pull/156))
- Adds `admin.hosts`: the admin listener answers only to its own address and
  these names, so a NIP-98 signature an admin made for another site can't be
  used against it. ([#158](https://github.com/ohstr/ncli/pull/158))

### Removed

- Removes `ncli relay stats`, `relay reindex` and `relay clear`, and their
  routes on the public relay port; each now names its replacement (`relay
  status`, `relay index rebuild`, `relay index drop`). ([#155](https://github.com/ohstr/ncli/pull/155))

### Fixed

- Fixes zap reindexing dropping every NIP-57 (kind 9735) receipt from the
  top-zapped index. ([#154](https://github.com/ohstr/ncli/pull/154))
- Fixes search reindexing covering only the newest `max_limit` profiles; a
  rebuild now indexes every stored profile. ([#155](https://github.com/ohstr/ncli/pull/155))

### Changed

- Updates nmilat to v0.5.0-rc.25, which reports relay activity for these
  metrics and keeps NIP-57 receipts when the zap index is rebuilt.
  ([#158](https://github.com/ohstr/ncli/pull/158))

## [0.8.0-rc.24]

### Changed

- Updates nmilat to v0.5.0-rc.24, whose NIP-CASH private transport listens for a
  reply on the relay it sent the request to; ncli's behaviour is unchanged.
  ([#152](https://github.com/ohstr/ncli/pull/152))

## [0.8.0-rc.23]

### Fixed

- Fixes huddle video freezing for good after a lost packet on `ncli relay`: a
  subscriber's keyframe requests (PLI/FIR) now reach the publisher, throttled,
  and its NACKs are answered. The SFU also asks for a keyframe once a new
  subscriber is actually receiving, and when a publisher reconnects, so the
  first picture no longer waits for the encoder's next natural keyframe. Before
  this, a 1:1 video call could lose one side's picture while audio carried on.

## [0.8.0-rc.22]

AltZap events move to kinds 9520/9521/9523.

### Changed

- Updates nmilat to v0.5.0-rc.20: AltZap events use kinds 9520/9521/9523, so
  `ncli event validate` and `ncli relay` check and index AltZap receipts under
  kind 9521. ([#150](https://github.com/ohstr/ncli/pull/150))

## [0.8.0-rc.21]

Relays hosting NIP-29 groups no longer keep a deleted subgroup in its parent.

### Fixed

- Fixes deleting a NIP-29 subgroup on `ncli relay`: it now leaves its parent's
  `child` list, so the parent's other subgroups stay readable, and an edit to
  the parent clears an id left behind by an earlier delete.
  ([#149](https://github.com/ohstr/ncli/pull/149))

## [0.8.0-rc.20]

`ncli skills install` needs `--dir`, so skills go wherever your agent reads them.

### Changed

- Makes `--dir` required for `ncli skills install`, which no longer defaults to
  `~/.claude/skills`; without it the command exits 2 and writes nothing.
  ([#141](https://github.com/ohstr/ncli/pull/141))

## [0.8.0-rc.19]

Relays configure the rest of NIP-29 group hosting from relay.yaml.

### Added

- Adds `groups.roleDescriptions`, `groups.admins`, `groups.moderationLog` and
  `groups.pendingJoins` to relay.yaml: describe roles in kind 39003, let more
  keys moderate every group, keep a moderation log, and hold codeless joins to
  closed groups for an admin.
  ([#148](https://github.com/ohstr/ncli/pull/148))
- Adds the relay's OK message to `publish --json` results (`message`), and
  `groups join` now says when a request is pending an admin.
  ([#148](https://github.com/ohstr/ncli/pull/148))

## [0.8.0-rc.18]

`ncli event` verifies, fetches and inspects any event, whoever signed it, and
relays gain NIP-86 moderation and configurable NIP-29 group roles.

### Added

- Adds `ncli event verify`, which checks any event's id and signature plus
  what it claims (author, kind, PoW, expiration, delegation, zap) and, with
  `--deleted`/`--cross-relays`, its deletion and every relay's copy. Events
  are looked up on the reference's relay hints, `--relays` or prefs, then the
  author's NIP-65 outbox, and no identity is needed.
  ([#144](https://github.com/ohstr/ncli/pull/144))
- Adds `ncli event get`, `show`, `encode`, `seen` and `rebroadcast` to print
  an event as served or as a readable card, spell it as nevent/naddr with
  relay hints, list which relays hold it, and copy it unchanged to other
  relays.
  ([#144](https://github.com/ohstr/ncli/pull/144))
- Adds `ncli event new`, `id`, `thread`, `diff`, `unwrap` and `validate` to
  build unsigned events and compute ids, walk a NIP-10/NIP-22 thread, compare
  two events, decrypt NIP-59 gift wraps, and run nmilat's per-kind rules
  before publishing.
  ([#144](https://github.com/ohstr/ncli/pull/144))
- Adds `groups.roles` to relay.yaml, which sets the moderation kinds each NIP-29
  group role may send, so a role like `moderator` has real power. Without it,
  only `admin` moderates, as before.
  ([#145](https://github.com/ohstr/ncli/pull/145))
- Adds NIP-86 moderation to `ncli relay`: ban pubkeys and events, allow or
  disallow kinds, block IPs, and change the relay's name, description and
  icon, saved across restarts. `nip86` no longer requires `membership`; the
  membership methods are added when it is on.
  ([#146](https://github.com/ohstr/ncli/pull/146))
- Adds `-e -` to `ncli id sign` and `ncli publish`, and `-o -` to `id sign`,
  so an event can be built, signed and published in one pipe.
  ([#144](https://github.com/ohstr/ncli/pull/144))

### Changed

- Updates nmilat to v0.5.0-rc.17: hosted NIP-29 groups enforce `restricted`
  and `hidden`, the relay's own key can moderate every group, and each group
  advertises its roles (kind 39003).
  ([#147](https://github.com/ohstr/ncli/pull/147))

## [0.8.0-rc.17]

`publish`, `find`, `dump` and `apply` log in to relays that require NIP-42
through a signer, so a process with no private key can use them.

### Added

- Adds NIP-42 login through a signer (`bunker+unix://`, `bunker://` or a key)
  to `publish` (`--signer`, `--auth-signer`), `find`/`dump` (`--auth-identity`,
  alias `--signer`) and `apply` (`--auth-signer`). Each connection signs one
  kind 22242 for the relay it dialed, and a failed login exits 7 with
  `reason` `login_required`, `signer_refused` or `relay_refused`.
  ([#139](https://github.com/ohstr/ncli/pull/139))

### Changed

- Names the matched rule in `ncli signer serve` denials, e.g.
  `rule "rest": denied by rule`. ([#139](https://github.com/ohstr/ncli/pull/139))

## [0.8.0-rc.16]

`ncli bunker` works on relays that require NIP-42 AUTH.

### Fixed

- Fixes `ncli bunker` hearing nothing on relays that require NIP-42 AUTH; it now
  subscribes once AUTH settles and re-subscribes when a relay closes its
  subscription. ([#137](https://github.com/ohstr/ncli/pull/137))

## [0.8.0-rc.15]

A Go 1.26.9 build with nine standard-library security fixes, and clearer
bunker refusals.

### Security

- Builds with Go 1.26.9, which fixes nine standard-library vulnerabilities in
  `net/http`, `crypto/tls` and `net/textproto`.
  ([#134](https://github.com/ohstr/ncli/pull/134))

### Changed

- Starts refusals from `ncli bunker` with `denied: ` or `invalid: `, so an
  app can tell a refusal from a malformed request.
  ([#135](https://github.com/ohstr/ncli/pull/135))

### Fixed

- Fixes `ncli bunker` history showing a quickly approved request as expired
  after a restart. ([#135](https://github.com/ohstr/ncli/pull/135))

## [0.8.0-rc.14]

A relay performance fix.

### Fixed

- Fixes `ncli relay` starving reads when many subscriptions are open; a REQ
  that can't be served within 10s now gets `CLOSED` `error: relay busy, try
  again later`. ([#133](https://github.com/ohstr/ncli/pull/133))

## [0.8.0-rc.13]

A local policy signer, and `id --save` stops printing the private key.

### Added

- Adds `ncli signer serve`, a local signer on a unix socket that signs only
  what its policy allows, with `signer status` and `signer check`; `id sign`
  and `publish` use it via `--signer bunker+unix:///path.sock`.
  ([#128](https://github.com/ohstr/ncli/pull/128))
- Adds the relay setting `pprofAddr`, which serves Go's profiler so a stuck
  relay can be inspected without stopping it.
  ([#131](https://github.com/ohstr/ncli/pull/131))

### Changed

- Stops `ncli id --save` printing the new private key; `--reveal` prints
  it. This breaks scripts that read `nsec` or `priv_hex` from its `--json`.
  ([#130](https://github.com/ohstr/ncli/pull/130))

### Fixed

- Fixes `ncli relay` leaving a valid event without an OK under heavy load;
  one queued over 10s gets `error: relay busy`, and a failing event no
  longer rejects others written with it.
  ([#132](https://github.com/ohstr/ncli/pull/132))

## [0.8.0-rc.12]

Agent skills ship inside the binary.

### Added

- Adds `ncli skills list|show|install`, so an agent with only `ncli` gets
  guidance for its version; `install` won't overwrite a changed file
  without `--force`. ([#126](https://github.com/ohstr/ncli/pull/126))

## [0.8.0-rc.11]

Import, relabel and remove vault identities.

### Added

- Adds `ncli id import`, which saves an nsec, hex key or ncryptsec to the
  vault from stdin, `--file` or a prompt, never an argument.
  ([#124](https://github.com/ohstr/ncli/pull/124))
- Adds `ncli id relabel <identifier> <new-label>`.
  ([#124](https://github.com/ohstr/ncli/pull/124))
- Adds `ncli id rm <identifier>`, which asks first and needs `--yes` with
  `--json` or no terminal. ([#124](https://github.com/ohstr/ncli/pull/124))

## [0.8.0-rc.10]

Relay examples organized by deployment scenario.

### Changed

- Moves the huddle packages under `huddle/` (`audio`, `client`, `rtp`,
  `sfu`). This breaks code that imports them.
  ([#113](https://github.com/ohstr/ncli/pull/113))
- Reorganizes `examples/relay/` by deployment scenario, from a personal relay
  to an agent swarm, replacing the feature-named files.
  ([#114](https://github.com/ohstr/ncli/pull/114))

### Fixed

- Fixes `examples/relay/full.yaml` showing `query:` at the top level, where
  it was silently ignored; it belongs under `httpBridge`.
  ([#114](https://github.com/ohstr/ncli/pull/114))

## [0.8.0-rc.9]

Security fixes from a black-box command matrix, headless bunker and space
commands, and consistent exit codes.

### Security

- Fixes `blossom upload` tokens that allowed uploading any blob; each is now
  scoped to its file's hash. ([#117](https://github.com/ohstr/ncli/pull/117))
- Fixes the relay's admin API accepting a replayed request or one without a
  body hash, which could enroll a member or mint an invite.
  ([#117](https://github.com/ohstr/ncli/pull/117))
- Accepts each NIP-98 event only once on the NIP-86 management API.
  ([#117](https://github.com/ohstr/ncli/pull/117))
- Applies REQ's private-group rules to `POST /query`.
  ([#117](https://github.com/ohstr/ncli/pull/117))
- Fixes a deleted private group's metadata and member lists staying
  readable by anyone. ([#119](https://github.com/ohstr/ncli/pull/119))

### Added

- Starts `ncli bunker` in the background without a terminal, and adds
  `bunker pending list|approve|reject` and `bunker sessions set-grant`.
  ([#119](https://github.com/ohstr/ncli/pull/119))
- Publishes the relay's NIP-43 kind:13534 member list on every join and
  leave. ([#119](https://github.com/ohstr/ncli/pull/119))
- Streams `space join` as JSON lines without a terminal, and adds
  `space chat send|list`. ([#119](https://github.com/ohstr/ncli/pull/119))

### Changed

- Moves the `id delegate` wizard to Bubble Tea v2, so commands no longer
  wait up to 5s on a terminal that doesn't answer a color query.
  ([#117](https://github.com/ohstr/ncli/pull/117))
- Changes `groups show --json` admin keys to `pubkey` and `roles`.
  ([#117](https://github.com/ohstr/ncli/pull/117))

### Fixed

- Fixes `prefs relays clear` wiping all of `prefs.yaml`, including the vault
  key. ([#117](https://github.com/ohstr/ncli/pull/117))
- Fixes `id --save` starting a new vault when the vault key is missing; it
  now refuses with `not_found`. ([#117](https://github.com/ohstr/ncli/pull/117))
- Applies a NIP-43 join to the member's open connections, and refuses an
  unauthenticated private read with `auth-required:`.
  ([#119](https://github.com/ohstr/ncli/pull/119))
- Fixes an authenticated `groups list` or `find` sometimes missing private
  groups. ([#117](https://github.com/ohstr/ncli/pull/117))
- Exits `auth` (7) when the relay refuses an anonymous `find`, `dump` or
  `groups show`, instead of printing an empty result.
  ([#117](https://github.com/ohstr/ncli/pull/117))
- Classifies a refused `groups` write by the relay's reason (`conflict`,
  `auth`, `invalid_input`) instead of `internal`, and adds the NIP-29
  `previous` tags private-group writes were missing.
  ([#117](https://github.com/ohstr/ncli/pull/117))
- Fixes exit codes across `apply`, `space`, `huddle`, `relay invites`,
  `relay reindex`, `relay context`, `miner`, `blossom`, vault labels and
  bare `ncli`, which reported success or `internal` for usage, missing-item
  and unsupported cases. ([#117](https://github.com/ohstr/ncli/pull/117), [#119](https://github.com/ohstr/ncli/pull/119))
- Makes every `--json` empty result `[]` and every `ncli relay --json` log
  line JSON. ([#117](https://github.com/ohstr/ncli/pull/117), [#119](https://github.com/ohstr/ncli/pull/119))
- Fixes `id delegate` tokens failing NIP-26 verification; re-issue older
  tokens. ([#117](https://github.com/ohstr/ncli/pull/117))
- Fixes the `ncli-huddle` skill being skipped by installers over invalid
  frontmatter. ([#117](https://github.com/ohstr/ncli/pull/117))

## [0.8.0-rc.8]

Fixes for editing private groups.

### Fixed

- Fixes `groups edit` making a private group public and rejecting edits on
  a group with subgroups; it now reads the group as the editor.
  ([#110](https://github.com/ohstr/ncli/pull/110))
- Advertises `nip29.subgroups` in `ncli relay`'s NIP-11 document.
  ([#110](https://github.com/ohstr/ncli/pull/110))

## [0.8.0-rc.7]

NIP-29 subgroups.

### Added

- Adds `groups create --parent` and `groups edit --parent` for NIP-29
  subgroups; `edit` keeps a group's existing children.
  ([#108](https://github.com/ohstr/ncli/pull/108))
- Adds `ncli groups tree`, and shows parent and children in `groups list`
  and `groups show --json`. ([#108](https://github.com/ohstr/ncli/pull/108))
- Exits 7 for `groups show` of a group that doesn't exist, the same as a
  private one you can't read. ([#108](https://github.com/ohstr/ncli/pull/108))

## [0.8.0-rc.6]

Authenticated reads, and refusals reported as refusals.

### Added

- Adds `--auth-identity` to `ncli find` and `ncli dump`, which authenticate
  (NIP-42) against a relay that requires it.
  ([#100](https://github.com/ohstr/ncli/pull/100))
- Accepts `--identity` in `groups show` and `groups list`, so a member can
  read their own private group. ([#103](https://github.com/ohstr/ncli/pull/103))
- Exits `auth` (7) from `find`, `dump`, `groups show` and `groups list` when
  the relay refuses the read, instead of reporting nothing found.
  ([#103](https://github.com/ohstr/ncli/pull/103))
- Adds `--mine` and `--member <pubkey>` to `groups list`, which list only the
  groups that pubkey belongs to. ([#104](https://github.com/ohstr/ncli/pull/104))

### Changed

- Publishes a floating `ghcr.io/ohstr/ncli:rc` image tag that tracks the
  newest release candidate. ([#98](https://github.com/ohstr/ncli/pull/98))
- Updates nmilat past v0.5.0-rc.6 for authenticated reads and the private
  group fixes. ([#100](https://github.com/ohstr/ncli/pull/100), [#104](https://github.com/ohstr/ncli/pull/104))

## [0.8.0-rc.5]

Meeting spaces and NIP-29 groups from the command line.

### Added

- Adds `ncli space create|list|show|join` for NIP-53 meeting spaces;
  `join` moves here from `ncli huddle`.
  ([#93](https://github.com/ohstr/ncli/pull/93))
- Adds `ncli groups` for NIP-29 groups, from `create` and `edit` to
  `members`, `pins` and `delete-event`; `edit` keeps every field you don't
  change. ([#94](https://github.com/ohstr/ncli/pull/94))

### Changed

- Updates nmilat to v0.5.0-rc.5, which hosts NIP-29 groups.
  ([#96](https://github.com/ohstr/ncli/pull/96))

## [0.8.0-rc.4]

Publish over HTTP.

### Added

- Adds `POST /events` to `ncli relay` behind `httpBridge.events.enabled`,
  which takes one signed event over HTTP.
  ([#91](https://github.com/ohstr/ncli/pull/91))

### Changed

- Moves the `query:` block under `httpBridge:`. This breaks configs that set
  `query:` at the top level. ([#91](https://github.com/ohstr/ncli/pull/91))
- Updates nmilat for the NIP-98 nonce fix and `POST /query` paging.
  ([#92](https://github.com/ohstr/ncli/pull/92))

## [0.8.0-rc.3]

Query over HTTP.

### Added

- Adds `POST /query` to `ncli relay` behind `query: enabled: true`, a
  NIP-98-signed HTTP alternative to a WebSocket REQ.
  ([#90](https://github.com/ohstr/ncli/pull/90))

## [0.8.0-rc.2]

Find live huddles and spaces, and chat in a call.

### Added

- Adds `ncli huddle list`, which shows a relay's live rooms with their peer
  count. ([#87](https://github.com/ohstr/ncli/pull/87))
- Adds `ncli huddle spaces`, which lists open NIP-53 spaces with a live
  session. ([#87](https://github.com/ohstr/ncli/pull/87))
- Accepts a space (naddr or coordinate) in `huddle join`, which finds its
  relay and room. ([#87](https://github.com/ohstr/ncli/pull/87))
- Adds in-call chat beside the roster when joining a space, with replies,
  quotes and backlog; `--no-chat` hides it.
  ([#87](https://github.com/ohstr/ncli/pull/87))

### Fixed

- Fixes `r` opening a "Restart?" dialog during a call.
  ([#87](https://github.com/ohstr/ncli/pull/87))

## [0.8.0-rc.1]

Relay management over HTTP, and `nostrconnect://` pairing that works with
real apps.

### Added

- Adds NIP-86, the Relay Management API, to `ncli relay` behind a `nip86:`
  block, including roles. ([#85](https://github.com/ohstr/ncli/pull/85))
- Adds `nip86.admins`, so a relay can be administered from keys other than
  its own. ([#85](https://github.com/ohstr/ncli/pull/85))
- Adds `huddle.udpPortRange`, so a relay in a container can publish the
  ports WebRTC media uses. ([#82](https://github.com/ohstr/ncli/pull/82))

### Changed

- Ends a removed member's live huddle calls.
  ([#85](https://github.com/ohstr/ncli/pull/85))
- Binds an admin request's body to its NIP-98 signature.
  ([#85](https://github.com/ohstr/ncli/pull/85))

### Fixed

- Fixes WebRTC huddles failing to connect when ICE candidates arrive before
  the SDP. ([#82](https://github.com/ohstr/ncli/pull/82))
- Fixes `ncli bunker` pairing with real apps over `nostrconnect://`, using
  every relay in the URI. ([#84](https://github.com/ohstr/ncli/pull/84))
- Applies the permissions an app requests as grants, and shows app names for
  `bunker://` pairings. ([#84](https://github.com/ohstr/ncli/pull/84))
- Answers `switch_relays` and `logout`.
  ([#84](https://github.com/ohstr/ncli/pull/84))
- Fits the "Paste nostrconnect:// URI" and "Set App Name" dialogs to their
  content. ([#84](https://github.com/ohstr/ncli/pull/84))

## [0.7.0-rc.1]

Group voice calls with video and screen share.

### Added

- Adds huddles to `ncli relay` behind a `huddle:` block: voice for NIP-53
  rooms over WebSocket, and voice, video and screen share for browsers over
  WebRTC, in the same rooms. ([#66](https://github.com/ohstr/ncli/pull/66))
- Adds `ncli huddle join <room>`, a live roster of who's present and
  speaking; it never captures a microphone.
  ([#66](https://github.com/ohstr/ncli/pull/66))
- Plays the call in `huddle join` when built with `-tags huddleaudio`; the
  default build shows "watching only". ([#76](https://github.com/ohstr/ncli/pull/76))
- Adds `huddle.requireMembership`, `huddle.iceServers` and
  `huddle.allowedOrigins`, and an example config.
  ([#66](https://github.com/ohstr/ncli/pull/66))

### Fixed

- Ends live huddles before `ncli relay` shuts down, instead of severing
  them. ([#66](https://github.com/ohstr/ncli/pull/66))

## [0.6.0]

Profiles, a network spinner, and clearer errors.

### Added

- Adds `ncli profile <identifier>`, which shows an identity's metadata,
  follows, relays, Blossom servers and lightning address from every relay.
  ([#55](https://github.com/ohstr/ncli/pull/55))
- Shows a spinner on stderr while a command waits on the network, except
  under `--json`, `-q`, `NO_COLOR` or a non-terminal.
  ([#55](https://github.com/ohstr/ncli/pull/55))
- Adds the importable packages `client/vault` and `client/prefs`, which pull
  10 modules instead of 38. ([#60](https://github.com/ohstr/ncli/pull/60))

### Changed

- Prints errors as `Error:` (red on a terminal) and help only on stderr;
  exit codes and `--json` are unchanged.
  ([#55](https://github.com/ohstr/ncli/pull/55))
- Rewrites every command's help to say what it does.
  ([#61](https://github.com/ohstr/ncli/pull/61), [#62](https://github.com/ohstr/ncli/pull/62))
- Updates nmilat to v0.4.0. ([#58](https://github.com/ohstr/ncli/pull/58))

### Fixed

- Fixes an unknown flag being reported twice and exiting 1 instead of 2.
  ([#55](https://github.com/ohstr/ncli/pull/55))
- Fixes `bunker sessions revoke-grant` without `--method` exiting as
  `internal` instead of `usage`. ([#55](https://github.com/ohstr/ncli/pull/55))
- Prints a short error and the command's help when `ncli relay` finds no
  config. ([#61](https://github.com/ohstr/ncli/pull/61))
- Fixes `ncli relay` freezing until restarted when a write had to grow its
  database file. ([#58](https://github.com/ohstr/ncli/pull/58))

## [0.5.0]

Decode cash tokens, examples in every help page, and day-scale ages.

### Added

- Reads NIP-CASH cash tokens and NIP-CW `circlehub1…` connections in
  `ncli decode`, never showing their secret.
  ([#49](https://github.com/ohstr/ncli/pull/49))
- Adds an `Example:` to every command's help.
  ([#51](https://github.com/ohstr/ncli/pull/51))

### Changed

- Defaults a local flow's `ensure` to `create`, so a missing store is
  created. ([#50](https://github.com/ohstr/ncli/pull/50))
- Shows the Age column in days and weeks (`2d4h`, `1w3d`).
  ([#53](https://github.com/ohstr/ncli/pull/53))
- Shortens help text. ([#51](https://github.com/ohstr/ncli/pull/51))
- Updates nmilat to v0.3.1. ([#53](https://github.com/ohstr/ncli/pull/53))

### Fixed

- Fixes two `Example:` commands that failed as written.
  ([#51](https://github.com/ohstr/ncli/pull/51))
- Fixes wallet transfers reusing a stale client, and NWC responses dropping
  circle fee fields, via nmilat v0.3.0.
  ([#51](https://github.com/ohstr/ncli/pull/51))

## [0.5.0-rc.1]

Decode cash tokens and examples in every help page.

### Added

- Reads NIP-CASH cash tokens and NIP-CW `circlehub1…` connections in
  `ncli decode`, never showing their secret.
  ([#49](https://github.com/ohstr/ncli/pull/49))
- Adds an `Example:` to every command's help.
  ([#51](https://github.com/ohstr/ncli/pull/51))

### Changed

- Defaults a local flow's `ensure` to `create`, so a missing store is
  created. ([#50](https://github.com/ohstr/ncli/pull/50))
- Shortens help text and uses `satoshi` as the example label.
  ([#51](https://github.com/ohstr/ncli/pull/51))
- Updates nmilat to v0.3.0. ([#51](https://github.com/ohstr/ncli/pull/51))

### Fixed

- Fixes two `Example:` commands that failed as written.
  ([#51](https://github.com/ohstr/ncli/pull/51))

## [0.4.9]

Reliable `apply stream` and `sync`.

### Fixed

- Fixes `apply stream` silently losing events while its destination
  reconnects. ([#48](https://github.com/ohstr/ncli/pull/48))
- Fixes an `apply stream` destination that could stop sending for good.
  ([#48](https://github.com/ohstr/ncli/pull/48))
- Fixes `apply sync` ignoring its `timeouts:` block.
  ([#48](https://github.com/ohstr/ncli/pull/48))

### Changed

- Renames the destination "Synced" column to "Duplicates", which is what it
  counts. ([#48](https://github.com/ohstr/ncli/pull/48))

## [0.4.8]

Paced publishing for `apply stream`.

### Fixed

- Caps unacknowledged events per remote destination with `writeConcurrency`,
  so an initial sync no longer trips a relay's rate limit.
  ([#45](https://github.com/ohstr/ncli/pull/45))
- Updates nmilat to v0.2.9, fixing stalled subscriptions and corrupted event
  JSON under load. ([#45](https://github.com/ohstr/ncli/pull/45))

## [0.4.7]

A relay fairness fix.

### Fixed

- Fixes a burst on one kind starving the other kinds in a relay's live
  subscription, via nmilat v0.2.8. ([#41](https://github.com/ohstr/ncli/pull/41))

## [0.4.6]

More NIPs on `ncli relay`, and no more dropped PoW events.

### Added

- Advertises and validates NIP-47, 48, 88, 90, AZ, B0 and B7 in
  `ncli relay`. ([#39](https://github.com/ohstr/ncli/pull/39))

### Fixed

- Fixes a destination relay dropping an event whose PoW tag overclaims its
  difficulty, via nmilat v0.2.7. ([#33](https://github.com/ohstr/ncli/pull/33))
- Advertises and validates NIP-57 and NIP-65, which `ncli relay` listed but
  never registered. ([#38](https://github.com/ohstr/ncli/pull/38))

## [0.4.5]

List relay contexts.

### Added

- Adds `ncli relay context list`. ([#31](https://github.com/ohstr/ncli/pull/31))

## [0.4.4]

Run a relay by context name.

### Added

- Adds `ncli relay -c/--context <name>`, which runs a saved context or
  creates one with a fresh identity. ([#30](https://github.com/ohstr/ncli/pull/30))

### Changed

- Lets a selected relay context win over a local `relay.yaml`.
  ([#30](https://github.com/ohstr/ncli/pull/30))

### Fixed

- Says plainly when `ncli relay` finds no config at all.
  ([#30](https://github.com/ohstr/ncli/pull/30))

## [0.4.3]

A vault error-code fix.

### Fixed

- Exits `conflict` when saving a vault identity under a taken label.
  ([#28](https://github.com/ohstr/ncli/pull/28))

## [0.4.2]

Error-code fixes from agent testing.

### Added

- Adds the `unsupported` error code (exit 8) for a server that lacks a
  capability. ([#27](https://github.com/ohstr/ncli/pull/27))

### Fixed

- Exits `usage` for wrong argument counts, without a help dump under
  `--json`. ([#27](https://github.com/ohstr/ncli/pull/27))
- Classifies a missing or wrong vault password in `id --save` as `usage` or
  `auth`. ([#27](https://github.com/ohstr/ncli/pull/27))
- Fixes `blossom mirror` failing on servers that require a hash-scoped
  token. ([#27](https://github.com/ohstr/ncli/pull/27))
- Records auto-approved requests in `bunker history`.
  ([#27](https://github.com/ohstr/ncli/pull/27))
- Fixes `ncli relay` leaking goroutines and hanging on shutdown.
  ([#27](https://github.com/ohstr/ncli/pull/27))

## [0.4.1]

TUI polish.

### Changed

- Dims unfocused panel borders, and lets Left/Right move between bunker's
  dialog buttons. ([#23](https://github.com/ohstr/ncli/pull/23))
- Shows the bunker splash screen while the board loads.
  ([#24](https://github.com/ohstr/ncli/pull/24))
- Shortens help text. ([#22](https://github.com/ohstr/ncli/pull/22))

## [0.4.0]

A Blossom client.

### Added

- Adds `ncli blossom` to upload, download, list, remove, mirror and report
  blobs, and manage servers. ([#18](https://github.com/ohstr/ncli/pull/18))

### Changed

- Draws every TUI in fixed 24-bit colors, fixing black-on-black text.
  ([#19](https://github.com/ohstr/ncli/pull/19))

## [0.3.0]

A NIP-46 remote signer.

### Added

- Adds `ncli bunker`, which signs for other apps after approval in a TUI or
  by a remembered grant, pairing over `bunker://` or `nostrconnect://`.
  ([#15](https://github.com/ohstr/ncli/pull/15))

## [0.2.0]

Sign events, and identity-based delegation.

### Added

- Adds `ncli id sign`, which signs unsigned events with a vault or nsec
  identity. ([1d9ebac](https://github.com/ohstr/ncli/commit/1d9ebac))

### Changed

- Makes `ncli ping`'s live board opt-in with `--tui`.
  ([ae902a1](https://github.com/ohstr/ncli/commit/ae902a1))
- Changes `id delegate` to take `--issuer` and `--delegatee` as any identity
  form and print the token. This breaks scripts that use `--issuer-key` or
  `--relay-key`. ([#1](https://github.com/ohstr/ncli/pull/1))

## [0.1.0]

The first public release.

### Added

- Adds `ncli relay`, a Nostr relay with NIP-11, auth, retention, search, a
  zap cache, NIP-43 membership and NIP-AA agent auth.
  ([5f6724c](https://github.com/ohstr/ncli/commit/5f6724c))
- Adds remote relay administration (`relay stats|reindex|clear|members|invites|roles`)
  and relay contexts. ([5f6724c](https://github.com/ohstr/ncli/commit/5f6724c))
- Adds `ncli apply` for stream, sync and inspect workflows, with a live TUI.
  ([5f6724c](https://github.com/ohstr/ncli/commit/5f6724c))
- Adds `ping`, `publish`, `find`, `dump`, `miner`, `id`, `prefs` and
  `version`. ([5f6724c](https://github.com/ohstr/ncli/commit/5f6724c))
- Adds structured error codes and `--json` on every command, and releases
  for Linux, macOS and Windows, Homebrew and Docker.
  ([5f6724c](https://github.com/ohstr/ncli/commit/5f6724c))
