# Changelog

## [0.8.0-rc.14]

### Fixed

- `ncli relay` no longer starves reads when many subscriptions are open.
  Every open REQ re-walked its whole index on each live tick, so feeds
  stopped getting EOSE on a large store. Live subscriptions now read only
  new events and wake on writes, and a REQ that can't be served within
  10s gets `CLOSED` `error: relay busy, try again later`.
  (nmilat v0.5.0-rc.13)
  ([#133](https://github.com/ohstr/ncli/pull/133))

## [0.8.0-rc.13]

### Added

- `ncli signer serve` runs a local signer on a unix socket. It holds one
  key in memory and answers the NIP-46 method set, so an agent can sign
  without holding the key. A `kind: signer-policy` file decides what gets
  signed (default deny), including required attestations such as a
  maintainer's signed "approve <id>". `signer status` probes it;
  `signer check` dry-runs a policy. `id sign` and `publish` sign through
  it with `--signer bunker+unix:///path.sock`.
  ([#128](https://github.com/ohstr/ncli/pull/128))
- Relay config `pprofAddr` serves `net/http/pprof` on its own listener,
  so a stuck relay can be profiled without killing it.
  ([#131](https://github.com/ohstr/ncli/pull/131))

### Changed

- `ncli id --save` no longer prints the new private key (`nsec`,
  `priv_hex`), in text or `--json`. It's in the vault, so printing it only
  put it in transcripts and logs. Add `--reveal` to print it anyway, or
  run `ncli id <label> --reveal` later.
  ([#130](https://github.com/ohstr/ncli/pull/130))

### Fixed

- `ncli relay` answers every EVENT with exactly one OK. Under heavy load
  a valid event could get no reply at all; one still queued after 10s now
  gets `error: relay busy`, and a failing event no longer rejects the
  others written with it. (nmilat v0.5.0-rc.12)
  ([#132](https://github.com/ohstr/ncli/pull/132))

## [0.8.0-rc.12]

### Added

- `ncli skills list/show/install` serves the agent skills built into the
  binary, so an agent with only `ncli` on `PATH` gets guidance matching its
  version. `install` copies them to `~/.claude/skills` (or `--dir`) and
  won't overwrite a differing file without `--force`.
  ([#126](https://github.com/ohstr/ncli/pull/126))

## [0.8.0-rc.11]

### Added

- `ncli id import` saves an existing private key (nsec, hex or ncryptsec)
  to the vault. The key is read from stdin, `--file` or a hidden prompt,
  never an argument; the first valid line is used. Re-running is a no-op;
  `--force` relabels a key already saved under another label. A key is
  never saved twice. ([#124](https://github.com/ohstr/ncli/pull/124))
- `ncli id relabel <identifier> <new-label>` renames a saved identity
  without its key. ([#124](https://github.com/ohstr/ncli/pull/124))
- `ncli id rm <identifier>` removes a saved identity; it asks first, and
  needs `--yes` with `--json` or no terminal.
  ([#124](https://github.com/ohstr/ncli/pull/124))

## [0.8.0-rc.10]

### Changed

- The `huddleaudio`/`huddleclient`/`huddlertp`/`huddlesfu` packages moved
  under `huddle/` as `audio`/`client`/`rtp`/`sfu`. Breaking only for code
  importing them; the `-tags huddleaudio` build tag is unchanged.
  ([#113](https://github.com/ohstr/ncli/pull/113))
- `examples/relay/` is organized by deployment scenario: personal, dev/test,
  public search, anti-spam, accountability, community membership, family,
  tracked membership, enterprise compliance, agent swarm (NIP-AA), app
  backend (NIP-86 + `POST /query`) and community voice, plus `minimal.yaml`
  and `full.yaml`. `open`/`auth`/`cache-search`/`ephemeral.yaml` are retired;
  `membership`/`pow`/`huddle.yaml` became `community-membership-relay`/
  `anti-spam-relay`/`community-voice-relay.yaml`. The README lists them all.
  ([#114](https://github.com/ohstr/ncli/pull/114))

### Fixed

- `examples/relay/full.yaml` showed `query:` as a top-level key; the real
  shape is `httpBridge.query`/`httpBridge.events`, so a config copied from
  it silently never mounted the endpoint.
  ([#114](https://github.com/ohstr/ncli/pull/114))

## [0.8.0-rc.9]

### Security

- `blossom upload` signed an authorization token with no `x` (hash) tag,
  valid for uploading any blob until it expired; servers that check the
  tag (the reference blossom-server) refused every upload. Each token is
  now scoped to its file's SHA-256. ([#117](https://github.com/ohstr/ncli/pull/117))
- The relay's admin HTTP API (`/admin/...`) accepted a NIP-98 header with
  no `payload` tag for any request body, and accepted the same header
  again until it expired: a captured header could enroll an attacker as a
  member or mint them an invite code. A `payload` tag is now required and
  each NIP-98 event is accepted once. `ncli` adds a `nonce` tag so repeated
  identical requests stay distinct; admin clients that send no `payload`
  tag are now refused. ([#117](https://github.com/ohstr/ncli/pull/117))
- The NIP-86 management API accepts each NIP-98 event once, like the admin
  API. ([#117](https://github.com/ohstr/ncli/pull/117))
- `POST /query` returns what REQ would for its signer, private NIP-29
  groups included. ([#117](https://github.com/ohstr/ncli/pull/117))
- Deleting a private NIP-29 group left its metadata, admin and member
  lists stored and readable by anyone, and it kept showing in `groups
  list`/`tree`. The relay now purges a deleted group's events.
  ([#119](https://github.com/ohstr/ncli/pull/119))

### Added

- `ncli bunker` without a terminal (or with `--json`) starts the signer in
  the background and prints its status. `bunker pending list/approve/reject`
  and `bunker sessions set-grant` do from scripts what the TUI does.
  ([#119](https://github.com/ohstr/ncli/pull/119))
- The relay publishes its NIP-43 kind:13534 member list, with roles, on
  every join and leave. ([#119](https://github.com/ohstr/ncli/pull/119))
- `space join` without a terminal (or with `--json`) streams the call as
  JSON lines -- arrivals, departures, speaking, chat -- until `--duration`
  or an interrupt. `space chat send/list` post and read a space's
  conversation without joining. ([#119](https://github.com/ohstr/ncli/pull/119))

### Changed

- Moved the `id delegate` wizard to Bubble Tea v2. v1 queried the terminal
  for its background colour when the program started, so every `ncli`
  command waited up to 5s on a terminal that doesn't answer (some
  multiplexers, serial consoles, `script`).
  ([#117](https://github.com/ohstr/ncli/pull/117))
- `groups show --json` admin entries use `pubkey`/`roles` keys, not
  `Pubkey`/`Roles`. ([#117](https://github.com/ohstr/ncli/pull/117))

### Fixed

- `prefs relays clear` wiped all of `prefs.yaml`, not just the relay list --
  including the vault key, so every identity saved in the vault became
  unrecoverable and the next save silently started a new vault ("invalid
  MAC" on the old entries). Relay contexts and Blossom servers were lost
  too. It now clears only the relays.
  ([#117](https://github.com/ohstr/ncli/pull/117))
- `ncli relay --json` logged in console format once the server started;
  every stderr line is now JSON. ([#119](https://github.com/ohstr/ncli/pull/119))
- `space show <id>` for a space that doesn't exist exits `not_found`
  instead of succeeding with nothing; so does `space join` for a missing
  space, instead of `internal`. ([#119](https://github.com/ohstr/ncli/pull/119))
- `groups show` exits `not_found` when the relay returns nothing for the
  group, instead of printing `{}`; `groups tree --json` prints `"roots": []`,
  not `null`. ([#119](https://github.com/ohstr/ncli/pull/119))
- `bunker sessions grants --json` prints `[]` for an app with no grants,
  not `null`. ([#119](https://github.com/ohstr/ncli/pull/119))
- A Blossom server's outright refusal (a 4xx other than 401/403/404/409/429,
  e.g. a mirror of a private address) exits `invalid_input`, not a
  retryable `network`, and no longer suggests the blob may exist anyway.
  ([#119](https://github.com/ohstr/ncli/pull/119))
- A NIP-43 join (invite claim or `relay members add`) now applies to the
  member's already-open connections; they were refused until they
  reconnected. A private group read refused before AUTH says
  `auth-required:`, not `restricted:`. The `membership.yaml` preset no
  longer claims non-members can request their own invite.
  ([#119](https://github.com/ohstr/ncli/pull/119))
- An authenticated `groups list`/`find` could intermittently omit private
  groups: a REQ the relay answered before AUTH landed was taken as final.
  It is now retried once authenticated.
  ([#117](https://github.com/ohstr/ncli/pull/117))
- With the vault key missing but identities still saved, `id --save`
  silently started a new vault (orphaning them) and unlocking said "no
  vault identity yet". Both now refuse with `not_found`, naming the
  missing key. ([#117](https://github.com/ohstr/ncli/pull/117))
- A missing vault label exits `not_found` (4), not `invalid_input`.
  ([#117](https://github.com/ohstr/ncli/pull/117))
- The `ncli-huddle` skill's frontmatter wasn't valid YAML, so skill
  installers skipped it. ([#117](https://github.com/ohstr/ncli/pull/117))
- `id delegate` tokens now verify under NIP-26 (the `nostr:delegation:`
  string, fixed in nmilat). Re-issue tokens minted before.
  ([#117](https://github.com/ohstr/ncli/pull/117))
- Exit codes that misreported what went wrong:
  `apply` sync/inspect without a terminal and `apply -f` on a missing file
  are `usage` (were `internal`); `space join` without a terminal is `usage`
  (was `unsupported`); `huddle list`/`space join` against a relay with
  huddles off is `unsupported` (was `internal`); `relay invites revoke` of
  an unknown code is `not_found` (reported `revoked`); `relay reindex
  search`/`clear search` with search off are `usage` (reported success).
  ([#117](https://github.com/ohstr/ncli/pull/117))
- `blossom upload`/`rm`/`mirror` failing on every server exit with the
  server's own code (`auth`, `not_found`, `network`) instead of `internal`.
  ([#117](https://github.com/ohstr/ncli/pull/117))
- `--json` left a config-file warning as a plain console line on stderr.
  ([#117](https://github.com/ohstr/ncli/pull/117))
- An anonymous `find`/`dump`/`groups show` that the relay refuses exits
  `auth` (7) instead of printing `[]`/`{}` with exit 0, which read as
  "nothing matched". ([#117](https://github.com/ohstr/ncli/pull/117))
- `groups` writes to a private group carried no NIP-29 `previous` tags:
  the timeline read behind them was anonymous, so it never saw the group.
  It now reads as the writer. ([#117](https://github.com/ohstr/ncli/pull/117))
- A `groups` write the relay rejects is classified by the relay's reason:
  `duplicate:` exits `conflict` (5), `restricted:`/`auth-required:` exit
  `auth` (7), `invalid:` exits `invalid_input` (3), instead of always
  `internal` (1). ([#117](https://github.com/ohstr/ncli/pull/117))
- Bare `ncli` exits 2 (`usage`) like every other group command; under
  `--json` it printed help on stdout and exited 0.
  ([#117](https://github.com/ohstr/ncli/pull/117))
- `miner check -e` on a malformed file exits `invalid_input` (3), not
  `internal` (1). ([#117](https://github.com/ohstr/ncli/pull/117))
- `miner mine` rejects a `--difficulty` outside 0-256 instead of mining
  with it. ([#117](https://github.com/ohstr/ncli/pull/117))
- `relay context use <unknown>` and `relay context add` with a missing
  config file exit `not_found` (4), not `invalid_input`.
  ([#117](https://github.com/ohstr/ncli/pull/117))

## [0.8.0-rc.8]

### Fixed

- `groups edit` silently flipped a private group public, and rejected any
  edit on a group with subgroups, because its current-metadata read
  (`currentGroupMetadata`) queried the relay unauthenticated -- always
  denied for a private group, the default on creation -- so the merge
  carried forward blank `Private`/`Closed`/`Children` instead of the
  group's real values. The read now authenticates as the editor's own
  `--identity`, and a restricted read fails the edit outright rather
  than silently proceeding from blank.
  ([#110](https://github.com/ohstr/ncli/pull/110))
- `ncli relay`'s NIP-11 document never advertised `nip29.subgroups`, even
  on a build that hosts NIP-29 groups and lists `29` in `supported_nips`
  -- this service built its own NIP-11 handler straight from config
  rather than going through the SDK's dynamic capability check.
  ([#110](https://github.com/ohstr/ncli/pull/110))

## [0.8.0-rc.7]

### Added

- `ncli groups create --parent <id>` and `ncli groups edit --parent <id>`
  (`""` detaches to root) for NIP-29 "Subgroups" -- `create --parent` is
  sugar for two events (9007 then 9002) combined into a single
  `{"create":...,"set_parent":...}` value under `--json`, never two
  concatenated JSON blobs. `edit`'s existing `mergeEditParams` now also
  carries the group's current `Children` list forward unconditionally
  (there's no `--child` flag): nmilat's relay rejects any edit on a group
  with existing subgroups that doesn't re-list every one of them, since
  kind:9002 is a full replace. Relies on nmilat's own new validation
  (self-reference/cycle/parent-must-exist/cross-group-admin/privacy-
  boundary checks, nmilat#70) for everything the relay itself enforces.
- New `ncli groups tree`: reads every visible kind:39000 and assembles
  the parent/child hierarchy locally from each group's own `Parent` tag
  (NIP-29's own recommended assembly, not the relay's `Children` tag --
  a node whose parent is private and unreadable still surfaces as its
  own root rather than being dropped). Indented text by default, a
  nested `{"roots":[...],"nodes":{...}}` object under `--json`.
  `Parent`/`Children` also surfaced in `groups list`/`show --json`.
- `ncli groups show <nonexistent-id>` now exits `7` (`auth`), same as a
  private group you're not a member of, rather than printing
  `(nothing found)` -- nmilat's relay deliberately makes the two
  indistinguishable now (closing an id-enumeration oracle an
  unauthenticated prober could otherwise use), so this follows suit
  rather than masking it. (#108)

## [0.8.0-rc.6]

### Added

- `ncli find`/`ncli dump` take `--auth-identity`, authenticating (NIP-42)
  against a target that requires it. Until now both connected
  anonymously -- `registerQueryFlags` exposed no auth flag, and nothing
  in the query path resolved or passed a signing key down to a relay
  connection -- so a restricted relay (e.g. a private+closed NIP-29
  group) read as "no events found," indistinguishable from a genuinely
  empty result. Threads the key through `Find`/`DumpFromTargets` into
  nmilat's `relayclient.ReadEventsFromRelayWithAuth` (nmilat#59/#61),
  which answers the challenge and retries a REQ closed as restricted.
  Not named `--identity`: `miner check`'s live mode shares
  `registerQueryFlags` with find/dump and already has its own
  `--identity` (an author to restrict the check to, a public key only)
  -- same shared flag set, unrelated meaning, so this gets its own flag
  rather than colliding. No identity given is unchanged: anonymous, as
  before. `apply`'s per-target identity is left for a follow-up -- its
  YAML stream/sync spec needs its own schema decision. Closes #99. (#100)
- `ncli groups show`/`ncli groups list` accept `--identity` now (the same
  persistent flag every write in `groups` already requires, just optional
  here) to authenticate a read the same way `--auth-identity` does for
  `find`/`dump`: given, a member can see their own private group; omitted,
  the read stays anonymous as before. Threaded via a new
  `client.QueryTargetsWithAuth`, `QueryTargets`' --identity-aware
  counterpart (`QueryTargets` itself is untouched -- none of its other 10
  callers need this).
- `find`/`dump`/`groups show`/`groups list` all now tell "the relay
  refused this" apart from "nothing matched": a new `client.ErrRestricted`
  surfaces whenever a query comes back empty because at least one target
  closed it as `"restricted: ..."` rather than a plain EOSE, and each
  command maps it to exit `7` (`auth`) instead of the usual empty-result
  success. Required threading `restricted` all the way down to
  `relayclient.ReadEventsFromRelayWithAuth`'s own per-attempt result
  (previously discarded on the retry path) through
  `readEventsWithTimeout`/`readEventsWithFallback`/
  `mergeEventsFromTargets`. Never observable without an identity -- an
  anonymous caller's connections have no such signal to give, so
  `QueryTargets`' own anonymous-only callers are unaffected. Closes #102.
- `ncli groups list` takes `--mine`/`--member <pubkey>` to scope the
  listing to groups a specific pubkey belongs to -- the "what groups am I
  in" shape `buzz-acp`'s `discover_channels()` needs, as opposed to the
  default "what groups exist" one. Implemented as a second filter in the
  same subscription, a `kind:39002` roster query `#p`-tagged to the
  target pubkey, merged client-side against the usual `kind:39000`
  listing. `--mine` resolves its pubkey from `--identity` (so it requires
  one); `--member` takes any pubkey directly with no identity required,
  though an authenticated read is still needed to see that pubkey's own
  private groups, same as the unscoped listing -- scoping by `#p` is
  orthogonal to NIP-42 auth, not a way around it. Depended on nmilat's
  own P0 fix for its privacy guarantee to hold (nmilat#66): before that
  fix, a `#p`-tagged `kind:39002` query bypassed the relay's visibility
  gate exactly like the untagged `kind:39000` case it fixed, since
  neither carries a "d"/"h" tag the old request-level gate looked at.

### Changed

- The release pipeline now also publishes `ghcr.io/ohstr/ncli:rc` (and the
  per-arch `rc-amd64`/`rc-arm64` tags under it), always pointing at the
  newest *prerelease* build the way `latest` already points at the newest
  *stable* one. Same per-arch/manifest split as `latest`, but with the
  inverse condition: `skip_push` on the `rc` entries is a template that
  pushes only when `.Prerelease` is set, since goreleaser's `auto` keyword
  only knows how to skip *on* a prerelease, not skip *unless* one. A stable
  release still never touches `rc`, exactly as an RC never touches `latest`.
  (#98)
- Bumped to nmilat past v0.5.0-rc.6 (a post-rc.6 commit, no tag cut yet),
  which carries the `AuthState`/`AuthMessage`/`AuthSettled`/
  `ReadEventsFromRelayWithAuth` additions `--auth-identity` above
  depends on (nmilat#61), plus nmilat#64 landing right behind it: the
  NIP-42 challenge is now sent on every connection unconditionally
  rather than only when the relay's `auth_required` is on (which also
  gated every write), and a `processClose` fix for a real relay killing
  whole sessions over a harmless redundant CLOSE -- very likely the
  actual cause behind the "connection closed" failure nmilat#61's own
  redial fix was built to route around. `ReadEventsFromRelayWithAuth`
  also gained a `restricted` return value nmilat#64 exposes; this repo
  doesn't surface it yet (ncli#102's own follow-up).
- Bumped to nmilat past v0.5.0-rc.6 again, past nmilat#66: the
  untagged-group-query privacy bypass fix `groups list --mine`/`--member`
  above depends on for its own privacy guarantee to hold.

## [0.8.0-rc.5]

### Added

- `ncli space create/list/show/join` -- a generic front door for NIP-53
  meeting spaces (kind:30312), which had no write path at all before.
  `create` always fills every field `nip53.ParseMeetingSpace` strictly
  requires, and rejects a room id that couldn't later be dialed as a
  huddle room before publishing, not after. Consolidates NIP-53
  listing/joining under `space`: `ncli huddle` now keeps only `list`
  (ephemeral room listing, genuinely distinct from a space) -- `join`
  moved off `huddle` and is reachable only as `space join` now
  (implementation stays in `cli/huddle`, which needs its own dial/TUI
  code). `space join` with no argument resolves to the relay's one open
  space when exactly one exists, erroring on zero or multiple rather than
  guessing. (#93)
- `ncli groups create/edit/delete/invite/join/leave/members/pins/
  delete-event/list/show` -- the full self-service NIP-29 relay-hosted-
  groups write surface, each a plain signed event (no admin HTTP surface)
  with the `previous` tag attached automatically. `edit` reads the
  group's current kind:39000 metadata first and merges in only the flags
  actually passed, since the underlying kind:9002 event replaces metadata
  wholesale. `list`/`show` are anonymous reads only for now -- a private
  group (the default on creation) needs NIP-42 auth to read, which this
  repo's connections can't do yet (tracked separately). (#94)

### Changed

- Bumped to nmilat v0.5.0-rc.5, which carries the NIP-29 relay-based groups
  implementation above (nmilat#56: real create/delete/membership/
  moderation state behind kinds 9000-9022, not just structural
  validation) -- also replaces the untagged pseudo-version
  (`v0.5.0-rc.3.0.20261004153625-3e302880071b`) go.mod had been pinned to
  with a clean tagged release. (#96)

## [0.8.0-rc.4]

### Added

- `ncli relay` mounts POST /events when `httpBridge.events.enabled` is set:
  the write-side counterpart to the existing POST /query bridge (nmilat's
  `relay.NewEventsHandler`), a NIP-98-authenticated HTTP alternative to
  opening a WebSocket purely to publish one already-signed event. The buzz
  CLI (and reactions, NIP-AM turn metrics) needs this to publish at all --
  without it every write attempt 404s. Wired exactly like /query: unwrapped
  by adminAuth (the handler does its own per-request NIP-98 check, not an
  admin-pubkey allowlist), sharing `wsHandler.Membership()` rather than a
  second independent cache. (#91)

### Changed

- **Breaking:** `query:`/`events:` moved under a new `httpBridge:` parent --
  `httpBridge.query.enabled` / `httpBridge.events.enabled`. A bare root
  `query` or `events` key names a wire path, not a concept, and gave an
  operator skimming the config no reason to read the two together even
  though they're the same feature's two halves. Anyone who already set the
  bare `query:` block (shipped in #90/v0.8.0-rc.3) needs to move it under
  `httpBridge:`; no functional change to /query's own behavior. (#91)
- Bumped to the nmilat release carrying the NIP-98 anti-replay-nonce fix
  (nmilat#51, the same commit that added `relay.NewEventsHandler` above)
  and the POST /query `Limit`/same-second-pagination fix (nmilat#52). (#91)

## [0.8.0-rc.3]

### Added

- `ncli relay` can serve buzz's NIP-CW `POST /query` bridge, a one-shot HTTP
  alternative to a WebSocket REQ/EOSE round trip, behind a new `query:`
  config block (`enabled: true`). NIP-98 authenticates the caller; when
  `nip11.limitation.membership_required` is set, a result is further gated
  to the signer's own NIP-43 membership, same as REQ/COUNT -- otherwise any
  validly-signed request is served, the same as an anonymous REQ would be.
  (#90)

## [0.8.0-rc.2]

### Added

- `ncli huddle list` shows which huddles are live on a relay right now, with
  each room's peer count and the protocol version it is pinned to. A room id
  was otherwise out-of-band knowledge, and `huddle join` on an unused id opens
  that room rather than failing -- so a typo put you alone in a new call with
  nothing to warn you. Backed by a new `GET /huddle/rooms`, gated exactly like
  a join: open on an open relay, members-only where `huddle.requireMembership`
  is set. Rooms exist only while occupied, so nothing ended or empty is
  listed. (#87)
- `ncli huddle spaces` lists the NIP-53 meeting spaces (kind:30312) that are
  `open` and have a live kind:30313 session. A space is the published,
  addressable `30312:<pubkey>:<d>` record of where a meeting lives; the room
  `huddle list` reports is the ephemeral transport underneath it, and the two
  were previously indistinguishable from the CLI. A session claiming `live`
  whose event has not been refreshed within `--stale-after` (default 1h) reads
  as ended, so a host whose process died leaves no meeting that looks forever
  in progress. (#87)
- `ncli huddle join` accepts a NIP-53 activity as well as a room id -- an
  naddr, or a `30312:<pubkey>:<d>` space or `30313:<pubkey>:<d>` session
  coordinate. The space is resolved to find which relay and room to dial, so a
  room id no longer has to be known out of band. (#87)
- In-call chat. Joining by activity opens the kind:1311 conversation beside the
  roster, with threaded replies (`e` tags, `<r>`), quotes (`q` tags, `<y>`) and
  the last 200 messages as backlog. A reply that arrives before its parent
  re-nests once the parent does; indentation is capped and reply cycles are
  survivable. `--no-chat` omits the panel. Chat needs an activity: a 1311
  message must name one, and a transport room id is not an addressable event,
  so joining by room id is still roster-only. (#87)

### Fixed

- `ncli huddle join` no longer registers a reload callback, which made
  `tui.App` capture `r` application-wide: pressing it over a live call popped a
  "Restart?" dialog wired to nothing, and it would have made `r` untypable in
  the chat composer. (#87)

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
