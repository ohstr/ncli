---
name: ncli-relay-ops
description: Configure, run, and operate a live ncli Nostr relay — author relay.yaml (NIP-11, auth, search, cache, NIP-43 membership), start the server, and monitor/administer it over its admin listener (`ncli relay status`, `relay index rebuild/drop`, `relay jobs`, Prometheus `/metrics` with `relay metrics token`, the embedded dashboard via `relay dashboard` or `admin.dashboard`), and `ncli relay members`/`invites`/`roles` for NIP-43 membership admin, plus a `nip86:` block serving the standard NIP-86 management API an app can call without a terminal), including rebuilding search/zap indexes on a running relay as cancellable jobs and switching between named relay configs without repeating `--config` (`ncli relay context`). Use when writing a relay config file, deploying/operating an ncli relay, monitoring a relay with Prometheus/Grafana or the dashboard, managing NIP-43 relay membership/invite-codes/roles, troubleshooting search/zap index state, or juggling multiple relay targets.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's examples/relay/*.yaml, cli/relay/command.go,
cli/relay/admin.go, cli/relay/admin_api.go, cli/relay/adminplane/,
cli/relay/jobs/ and cli/relay/service_membership.go as of writing. This skill is self-contained by
design and won't see repo changes automatically — update by hand if
flags/schemas change. -->

# ncli relay / relay status, index, jobs

One config file (`--config relay.yaml`), read by both `ncli relay` and the
commands that administer it (`status`, `index`, `jobs`, `members`, ...) —
those need `nip11.privkey` from it to sign NIP-98 requests, and
`admin.listen`/`listen`/`port` to find the relay.

## Minimal config to start

```yaml
port: 5500

nip11:
  name: ncli-dev-relay
  pubkey: 9b14dff44fb8d74e2b90d5ae1501b935e073a5245749458a3e261021646f4e11
  contact: admin@example.com

store: ./data/db/notes.db
logdir: ./data/logs
```

`pubkey` alone is enough to start; `privkey` is only required if you also
want `cache.zaps.enabled`, `cache.profiles.stats.enabled`,
`cache.relay.stats.enabled` or the admin commands (`status`, `index`,
`jobs`, `verify`, `members`, ...).

`port` binds every interface. To bind one address, set `listen:
127.0.0.1:5500` (host:port): it wins over `port`, and the admin listener's
default becomes `127.0.0.1:<its port + 1>`. `relay members`/`invites`/
`roles` dial `listen`'s host (`localhost` when it is `0.0.0.0`, `::` or
empty); `status`/`index`/`jobs`/`verify`/`dashboard` dial the admin
listener. Prefer an IP literal: a hostname with several addresses binds
only one. `--check` rejects (`invalid_input`) a `listen` that isn't
host:port with a port 1-65535, has stray whitespace, or names a host that
doesn't resolve. On port 65535 set `admin.listen` (port + 1 doesn't
exist). `NCLI_LISTEN`/`NCLI_PORT` apply even when the file has no
`listen`/`port` key, to the relay and its admin commands alike.

## Scenario examples

Each file under `examples/relay/` (besides `minimal.yaml` and `full.yaml`
below) is a real deployment shape, not just a feature demo -- copy the one
closest to what you're building and adapt its name/port/store.

| File | Scenario | Key fields beyond minimal |
|---|---|---|
| `personal-relay.yaml` | One person's own outbox/backup -- wide open, low stakes | Nothing beyond minimal, by design |
| `community-membership-relay.yaml` | Invite-gated community (paid newsletter, friend group, contributor channel) | `membership.enabled` + `nip11.privkey` + `auth_required`+`url` (+ optional `membership_required`) -- NIP-43 relay membership: enrolled pubkeys can REQ/EVENT, everyone else must invite-then-join first |
| `public-search-relay.yaml` | Public, high-traffic relay where search, "what's trending" and per-profile stats actually work | `cache.profiles.search.*` + `cache.profiles.stats.enabled` + `cache.zaps.enabled` + `cache.relay.stats.enabled` + `nip11.privkey`/`url` -- also the shape the admin commands need, since their auth requires `nip11.privkey` too |
| `anti-spam-relay.yaml` | Public relay fighting spam with mining cost instead of a ban list | `pow.strict: true` + `pow.min` (nonzero) -- actually rejects under-difficulty events instead of just advertising the requirement |
| `dev-test-relay.yaml` | Throwaway relay for local development or CI | Nothing beyond minimal -- its own port/store so it can run alongside `just dev relay` or another instance of itself |
| `agent-swarm-relay.yaml` | A team's AI agents post under their own key via a human's membership, no separate bot enrollment | `agent_auth.enabled` on top of membership + `auth_required` + `membership_required` -- NIP-AA |
| `community-voice-relay.yaml` | A community that meets, not just posts -- live voice rooms alongside its notes | `huddle.enabled` (+ `rtc`, `iceServers` for browsers) -- real-time voice over its own WebSocket, independent of the Nostr socket |
| `app-backend-relay.yaml` | A product's own backend administers/calls the relay over HTTP, never a terminal | `nip86.enabled` (+ `admins`) -- the standard NIP-86 management API -- and `httpBridge.query.enabled` -- a one-shot HTTP alternative to a WS REQ/EOSE round trip |
| `accountability-relay.yaml` | Public Q&A/support relay deterring drive-by abuse without an allowlist | `nip11.limitation.auth_required` **alone**, no membership block -- requires *some* signed-in identity, not a specific one (that distinction is the point: `membership_required` is what actually restricts *who*) |
| `enterprise-compliance-relay.yaml` | An org's internal relay wanting defense in depth, not one mechanism alone | `membership` + `auth_required`/`membership_required` + `pow.strict` + `nip86` **together** -- the checks stack (an enrolled member's event still needs real PoW; a high-PoW event from a non-member is still refused) |
| `family-private-relay.yaml` | A small closed circle enrolled directly, never by invite | Same mechanism as `community-membership-relay.yaml` (`membership` + `membership_required`) -- the difference is operational: `ncli relay members add` only, no invite flow ever used |
| `tracked-membership-relay.yaml` | A community wanting a visible roster/roles without gating anything | `membership.enabled` with `membership_required` **left off** -- membership is a label (`members list`, `roles`) for display/moderation tooling, not an admission check |

`examples/relay/full.yaml` documents every field at its default, commented:
`port`/`listen`, `description`, `limitation.*`, `pow.*`, `cache.profiles.*` (search,
stats, checks, scoring), `cache.zaps.*`, `cache.relay.stats.*`,
`membership.*`, `nip86.*`, `agent_auth.*`, `httpBridge.*`, `presence.*`,
`blossom.*`, `recipientOnlyKinds`, `limits.*` (commented out),
`handshakeTimeout`/`pingInterval`/`pongTimeout`/`writeTimeout`,
`outgoingBufferSize`/`maxConcurrentStoreTasks`, `logs.*` rotation and
`huddle.*`.
`examples/relay/minimal.yaml` is the bare quickstart `just dev relay` runs.

Full field table: `references/config-schema.md`.

## Running the relay

```sh
ncli relay --config relay.yaml
# validate only: exit 0, or the error the relay would fail to start with
ncli relay --config relay.yaml --check
```

A key the relay doesn't know (`htpBridge:`, `httpBridge.qurey`) only logs
a warning at startup and is ignored, so its setting stays off. `--check`
fails on it (`invalid_input`, exit 3), naming every unknown key by its
dotted path; run it after every config edit. A key with no value
(`bogus:`) counts too. `NCLI_*` env vars and the exact keys other commands
read (`vault.password`, `signer`, `groups.identity`, ...) are not
reported; anything else beside them (`query: {enabled: true}`) is.

Foreground process; stops cleanly on SIGINT/SIGTERM. Logs go to stderr and
to `<logdir>/nrelay.log` as JSON lines (or a custom `logs:` rotation block).
`logs.level: debug` adds per-connection lines; `logs.file: false` keeps
stderr only.

## `limits:` — rate limits, opt-in

**A limit that isn't set is no limit.** There are no defaults: without a
`limits:` block the relay limits nothing, exactly as before. The one
exception is Blossom's own caps (`blossom.maxBlobBytes` 100 MiB,
`maxUploadsPerSigner` 2, `maxConcurrentUploads` 8), which are separate and
keep their defaults.

A public relay (`nip11.limitation.auth_required: false`) with no limits is
warned about, never refused: `--check` exits 0 but logs `no rate limits
configured (limits:)` on stderr (and lists it in `--json`'s `warnings`),
and startup logs the same line. An `auth_required` relay isn't warned.
A per-pubkey limit that never applies is warned about the same way:
`blossom.downloads.perPubkey` while downloads are public (a GET or HEAD has
no signer, so only DELETEs count), and `blossom.uploads.perPubkey` or
`events.perPubkey` with `exempt: members` while only members may upload or
write (every limited key is exempt).

Recommended block for a public relay:

```yaml
limits:
  events:                    # EVENT, WebSocket and POST /events
    perPubkey: {events: 30, per: 1m, burst: 60}
    perIP: {events: 120, per: 1m}
  reads:                     # REQ, COUNT, NEG-OPEN and AUTH
    perIP: {events: 60, per: 1m, burst: 120}
  http:                      # POST /query, /count, /events
    perIP: {events: 60, per: 1m}
  blossom:
    uploads: {perPubkey: {events: 20, per: 1h}, perIP: {events: 40, per: 1h}}
    downloads: {perIP: {events: 600, per: 1m}}
  connectionsPerIP: 20
  exempt: members            # members and admins skip the per-pubkey limits
  trustedProxies: [127.0.0.1]   # only your reverse proxy's own address
```

- Each rate is `events` per `per`, up to `burst` at once (default
  `events`). Set only the ones you want; each counts on its own.
- A refused EVENT or AUTH is `OK false "rate-limited: ..."`, a refused
  REQ/COUNT is `CLOSED "rate-limited: ..."` (NEG-OPEN: `NEG-ERR`), HTTP and
  Blossom get 429 with `Retry-After`, and a connection over
  `connectionsPerIP` is refused 429 before the WebSocket upgrade.
- `events.perPubkey` counts an EVENT against its author only on a
  connection logged in (NIP-42) as that author, and only for an event the
  relay doesn't already have; anything else is counted per IP alone. Over
  `POST /events` the NIP-98 signer is the author, so it always counts.
- `reads` counts REQ, COUNT and NEG-OPEN, and each AUTH takes one from the
  IP's `reads.perIP`: a `reads.perIP` of 1 is spent by the login itself.
- One limiter is shared: an author's EVENTs over the WebSocket and over
  `POST /events` draw from the same budget, and `connectionsPerIP` counts
  the relay's WebSocket and huddle ones (`/huddle/{id}/audio`, `/rtc`)
  together.
- `exempt: members` lifts only per-pubkey limits (never for a banned key);
  per-IP limits still apply to members.
- Behind a reverse proxy, list it in `trustedProxies`, or every client is
  counted as the proxy's IP. The proxy must set or append
  `X-Forwarded-For` (the client is the rightmost hop that isn't a trusted
  proxy; one that passes a client's header through untouched lets the
  client pick its own key). `X-Real-IP` is read only when there is no
  `X-Forwarded-For`, and a client hop that isn't an address (`unknown`)
  falls back to the proxy's own address. List the proxy's own address, not
  its network: trusting a pod or cluster network (`10.0.0.0/8`) trusts
  every in-cluster client to name itself. IPv6 clients are counted by
  their /64.
- `--check` rejects (`invalid_input`, `input` = the key or value) negative
  numbers, `events` without `per` or the reverse, a bad duration, a rate
  under 1ns an event (`input` = its `events`), a `burst` whose window
  overflows (`input` = its `burst`), a bad CIDR and an `exempt` other than
  `none`/`members`.
- Refusals are counted in `ncli_relay_rate_limited_total{action,by}`
  (`action`: event, read, http, upload, download, connection; `by`: ip or
  pubkey) on `/metrics`.

## `ncli relay context` — stop repeating `--config`

Operating more than one relay means repeating `--config path/to/relay.yaml`
on every admin call. Save each relay's config under a name instead:

```sh
ncli relay context add bee_community ~/relays/bee-community.yaml
# every relay command now targets bee_community
ncli relay context use bee_community

# list saved contexts, "*" = current (bare `ncli relay context` does the same)
ncli relay context list
ncli relay context remove bee_community
```

Precedence when `--config` is omitted: a `ncli.yaml`/`relay.yaml` in the
current directory still wins (unchanged from before contexts existed); the
current context is consulted only when neither is present, ahead of the
`$HOME` fallback. Every example below that omits `--config` relies on one
of these three -- cwd discovery, a context, or `$HOME/ncli.yaml` -- already
resolving to the right relay.

## `ncli relay members`/`invites`/`roles` — NIP-43 membership admin, relay must be running

NIP-98-signed HTTP to the relay's public port, gated on
`membership.enabled: true` in the relay's own config (see the `membership`
preset). Manage enrolled members, issue/revoke invite codes out-of-band, and
define roles — all against the relay's live `MembershipService`, so changes
take effect for already-connected clients immediately.

```sh
# every enrolled member
ncli relay members list
# one member's record (404 if not a member)
ncli relay members show <pubkey>
# admin bypass: enroll directly, no invite code
ncli relay members add <pubkey> --role vip
# idempotent — no error if already not a member
ncli relay members remove <pubkey>

# issue a code to hand out out-of-band
ncli relay invites create --ttl 24h --max-uses 1
# absolute expiry instead of a TTL (unix seconds; must be future and within
# membership.inviteMaxTTL) -- mutually exclusive with --ttl and --no-expiry
ncli relay invites create --expires-at 1799999999 --max-uses 1
# a code that never expires (requires membership.inviteAllowNoExpiry: true)
ncli relay invites create --no-expiry --max-uses 1
# every currently-stored code
ncli relay invites list
# one code's state (claim, expires_at, max_uses, uses), incl. expired/used-up
ncli relay invites get <code>
# idempotent
ncli relay invites revoke <code>

ncli relay roles list
# NIP-43 has no "delete role" — see reference doc
ncli relay roles create <id> --label "VIP" --color 280
```

Same `--json`/error-classification convention as `status`/`index` below, plus
`invalid_input` (`400` — a malformed pubkey, an unparseable `--ttl`, a
missing role `id`, `--color` outside 0-360), `not_found` (`404` — `members
show` on a non-member), and `usage` (`501` — membership isn't enabled on the
relay at all; not `network`, since retrying won't fix a config problem).

Full endpoint list and subcommand reference: `references/membership-admin-reference.md`.

## `nip86:` — let an app moderate and administer the relay, no terminal

`ncli relay members/invites/roles` is a bespoke API that only this CLI
speaks, and it authenticates as `nip11.privkey` — so administering means
holding the relay's own secret key. The `nip86:` block adds NIP-86, the
standard Relay Management API, on the relay's own URL, authenticated by
NIP-98 against a list of admin pubkeys:

```yaml
membership:
  # only for the membership methods (allowpubkey, claims, roles); the
  # moderation methods work without it
  enabled: true
nip86:
  enabled: true
  # in addition to nip11.pubkey, which is always allowed
  admins:
    - <host hex pubkey>
  # omit to answer any origin
  allowedOrigins:
    - https://app.example
```

Requests are told apart from the WebSocket upgrade and the NIP-11 document by
their content type alone — all three share the relay URL:

```sh
curl -X POST https://relay.example/ \
  -H 'Content-Type: application/nostr+json+rpc' \
  -H "Authorization: Nostr <base64 kind-27235 event>" \
  -d '{"method":"createclaim","params":["party-2026"]}'
```

Gotchas worth knowing before you debug a 401:

- The NIP-98 event **must** carry a `payload` tag hashing the body. NIP-98
  itself only recommends it; NIP-86 requires it.
- The `u` tag is compared against the request URL. A bare
  `https://relay.example` is accepted for a request to `/`, but anything else
  must match exactly — and clients know the relay as `wss://`, so the scheme
  has to be swapped.
- With `enabled: true` and no admin pubkey resolvable at all, the relay
  refuses to start rather than serve an endpoint nobody can use.
- A browser cannot reach the endpoint without the CORS preflight, which the
  relay answers. A permissive origin is fine here: authorization is a signed
  header, not a cookie.

Methods, always: `supportedmethods`, and moderation --
`banpubkey`/`unbanpubkey`/`listbannedpubkeys` (a banned pubkey's new events
are refused `blocked: ...`), `banevent` (also deletes the stored copy)/
`allowevent`/`listbannedevents`, `listeventsneedingmoderation` (always `[]`:
there is no review queue), `allowkind`/`disallowkind`/`listallowedkinds`/
`listdisallowedkinds` (once any kind is allowed, every other kind is
refused), `blockip`/`unblockip`/`listblockedips` (by the connection's own
address; a forwarded-for header is ignored; NIP-86 calls themselves are
never blocked, so an admin can't lock themselves out), and
`changerelayname`/`changerelaydescription`/`changerelayicon` (the NIP-11
document changes at once). The relay's own key is never refused. This state
is saved beside the store as `<store>.moderation.json` and survives a
restart; it overrides `nip11.name`/`description`/`icon` from the config.

With `membership.enabled`, also: `allowpubkey`, `unallowpubkey`,
`listallowedpubkeys`, `createclaim`, `createclaimwithlimits`, `getclaim`,
`listclaims`, `deleteclaim`, and the role methods `createrole`, `editrole`,
`deleterole`, `assignrole`, `unassignrole`.

Invite-code method shapes:

- **`createclaim`** has two forms. `["<code>"]` stores a caller-chosen code
  with the relay's configured defaults and answers `true`; a client-chosen
  code must be **at least 16 characters** and use only `[A-Za-z0-9_-]`, else
  it is refused. `[]` or `[""]` makes the relay generate a strong code and
  return the full claim record (so an operator can read the code back). A code
  that already exists is an error -- it is never silently overwritten.
- **`createclaimwithlimits`** `["<code>", {…}]` -- a **PROPOSED NIP-86
  addition** -- mints a code with an explicit `expires_at` (unix seconds; `0`
  = never, only when `inviteAllowNoExpiry` is set) and/or `max_uses` (`>= 1`).
  An absent field uses the relay default. `expires_at` must be in the future
  and within `inviteMaxTTL`; a value outside policy, an unknown field, a
  wrong-typed value, an empty/weak code, or a duplicate is an error and **no
  code is created** -- the relay never silently clamps a requested limit.
  Answers `true`.
- **`getclaim`** `["<code>"]` -- a **PROPOSED NIP-86 addition** -- returns
  `{"claim","expires_at","max_uses","uses"}` for any stored code, **including
  expired or used-up ones**, until it is deleted. The output field is `claim`
  (not the record's `code`). An unknown code is an error.
- **`listclaims`** `[]` returns a plain list of **live** code strings only
  (not expired, not used up). Use `getclaim` or the admin REST route to see a
  dead code's record.

Both proposed methods are detectable via `supportedmethods`, which a client
MUST call with the **same key** it will use before offering per-invite limits.
The set is scoped to the calling key: an admin's `supportedmethods` lists every
method, whereas a plain member's lists only `createclaim` and
`supportedmethods` (see member claims below), plus whatever its NIP-43 roles
give (see "Roles and tasks").

**Member claims (`membership.memberClaims`, bool).** Unset, it follows
`nip11.limitation.membership_required`: **false on a members-only relay, true
otherwise** (the same trigger as `blossom.membersOnly.upload`, with the
opposite effect); an explicit
`true`/`false` always wins. When membership is on and this is true, a plain member -- not only an admin -- may
mint an invite via `createclaim`, per NIP-43. The member's NIP-98 key is
accepted even though it is not an admin, and the Relay Management API then
scopes it to exactly `createclaim` + `supportedmethods`; calling any other
method (`listclaims`, `getclaim`, `createclaimwithlimits`, `deleteclaim`, the
pubkey/role methods, or any moderation method) is refused as an unknown method.
A member's code uses the relay's configured defaults and carries no roles.

- A verified key that is neither an admin nor a member is still rejected with a
  **401**, exactly as before.
- **Risk / escape hatch:** there is **no per-member cap** on how many codes a
  member may mint -- the invite record has no creator field to count against.
  That is why a members-only relay leaves it off unless set. With it false, a
  member gets a **401** from the management API (admin-only), while invite
  redemption on join (kind 28934) is unaffected. With membership off,
  `memberClaims` has no effect.
- A NIP-43 invite request (REQ kind 28935) needs `createclaim` too, so
  `memberClaims` decides it the same way: with it false, a member's request
  is closed `restricted: ...` (an unauthenticated one `auth-required: ...`).
  Older relays answered any connection allowed to read.

**`unallowpubkey` ends the member's live huddle calls**, not just their next
join — the other participants stay connected. `ncli relay members remove`
does the same.

## Roles and tasks — what a NIP-43 role may do

NIP-29, NIP-43 and NIP-86 define roles but leave what a role may do to the
relay. `membership.roles` is where this relay says it: each NIP-43 role id
(the `d` tag of its kind 33534, as `relay roles create`/`createrole` make
it) maps to a list of **tasks**, named after the protocol verb that does
them. A member's tasks are the union of its roles' lists plus
`membership.members` (every enrolled member). A **banned** pubkey holds
none — but a ban never takes what the config file gives: `nip11.pubkey` and
`nip86.admins` hold every task (`*`), `groups.admins` holds `group:*`.

```yaml
membership:
  enabled: true
  roles:
    host:     { can: ["@claims", "@members", banpubkey, banevent, "group:9005"] }
    greeter:  { can: [createclaim] }        # greeters may invite
    uploader: { can: [blossom.upload] }
  members:  { can: [createclaim] }          # every member
groups:
  create: [members]                         # who may create a group (kind 9007)
```

| task | what it allows |
|---|---|
| a NIP-86 method name | that method: `createclaim`, `banpubkey`, `changerelayname`, ... (incl. `createclaimwithlimits`, `getclaim`) |
| `@claims` | `createclaim`, `createclaimwithlimits`, `getclaim`, `listclaims`, `deleteclaim` |
| `@members` | `allowpubkey`, `unallowpubkey`, `listallowedpubkeys` |
| `@bans` | `banpubkey`, `unbanpubkey`, `listbannedpubkeys`, `banevent`, `allowevent`, `listbannedevents`, `listeventsneedingmoderation` |
| `@roles` | `createrole`, `editrole`, `deleterole`, `assignrole`, `unassignrole` |
| `@relayinfo` | `changerelayname`, `changerelaydescription`, `changerelayicon` |
| `@policy` | `allowkind`, `disallowkind`, `listallowedkinds`, `listdisallowedkinds`, `blockip`, `unblockip`, `listblockedips` |
| `group:<kind>` | that NIP-29 moderation kind (9000-9020) in **every** hosted group, e.g. `group:9005` (delete an event); `group:*` all of them |
| `blossom.upload` / `blossom.download` | upload / download blobs where `blossom.membersOnly` restricts them |
| `blossom.delete` | delete **any** blob, not only your own |
| `huddle.join` | join calls where `huddle.requireMembership` restricts them |
| `group.create` | create a group (kind 9007) where `groups.create` restricts it |
| `*` | everything |

- **How a client learns its tasks:** NIP-86 `supportedmethods`, signed with
  the key it will use, lists exactly the methods that key holds (the spec
  allows a per-caller answer). A key holding no method is a 401; a method it
  doesn't hold is refused as an unknown method.
- **Existing settings map on, nothing changes by default:**
  `memberClaims` = members get `createclaim`; `blossom.membersOnly.upload/
  download` = members get `blossom.upload/download` (off: everyone may);
  `huddle.requireMembership` = members get `huddle.join` (off: anyone may);
  `groups.create` omitted = everyone may create a group. Admins count as
  members everywhere (they already did for Blossom; a huddle with
  `requireMembership` now admits them too).
- **`membership.members.can` wins.** Set, it is the whole members grant: the
  old switches then only say whether a task is restricted. So with
  `blossom.membersOnly.upload: true` and `members: {can: [createclaim]}`, only
  admins and roles listing `blossom.upload` (the `uploader` above) may upload;
  `--check` logs a warning for each restricted task `members.can` leaves out.
  An explicit `memberClaims` must agree with whether `members.can` gives
  `createclaim`, or `--check` fails.
- **`groups.create`** entries are `everyone`, `members` or a role id; `[]`
  leaves group creation to the admins and roles whose tasks list
  `group.create`. Refused creation is `OK false` `restricted: ...`.
- **Validated at `--check`** (`invalid_input`, exit 3, `input` = the bad
  value): an unknown task, a `group:` kind outside 9000-9020, `roles`/`members`
  without `membership.enabled`, a `groups.create` entry other than
  `everyone` without membership. A role id with no kind 33534 yet is only a
  warning at startup: roles can be created at runtime.
- **`@roles` and `@members` are as good as `*`** in practice: whoever can
  assign roles can give themselves any task, and `allowpubkey` makes members.
- **`groups.grantCeiling: true`** stops a group member from handing out a
  role that can send a moderation kind they can't send themselves (put-user
  with roles). Off by default, since existing groups may rely on such grants.
- A NIP-43 invite request (REQ kind 28935) is the `createclaim` task, like
  NIP-86 `createclaim`: a greeter role can hand out codes either way.
- The `ncli relay members/invites/roles` admin HTTP routes stay admin-only.

## Monitoring and index jobs — relay must be running

Every running relay has an **admin listener** separate from its public
port: `admin.listen`, default `127.0.0.1:<port+1>` (`listen`'s port when set; `off` disables it).
`status`/`index`/`jobs` sign each request (NIP-98) with `nip11.privkey`,
which must be the relay's key or in `nip86.admins`. `--admin-url` overrides
where they connect.

```sh
# health and live metrics (--watch: every 2s; NDJSON with --json)
ncli relay status
# each index: state, exact entries, events behind, last job, and a → line
# for anything that isn't simply ready (why, and what happens next)
ncli relay index list
# rebuild: prints the plan, live progress, then what changed
ncli relay index rebuild zaps
# for a script: the job at once, or the final job once it ends
ncli relay index rebuild zaps --json
ncli relay index rebuild zaps --json --wait --timeout 10m
# what a rebuild would do; nothing runs
ncli relay index rebuild all --dry-run
# remove what an index whose feature is off still holds
ncli relay index drop stats --yes
# the relay's own jobs and yours, newest first (20 kept per index)
ncli relay jobs list --index zaps
ncli relay jobs show <id> --follow
ncli relay jobs cancel <id>
```

The relay keeps each index current **on its own**, as jobs (`trigger:
reconcile`): it checks on start, when a job ends, and every few seconds.
Readers keep the previous data until new data is ready, and an index job
holds the store writer at most 25 ms at a time, so writes never wait on it.

| What happened | What the relay does |
|---|---|
| Feature off while events were stored, then on again | `catch_up` from where the index stopped: deletes first, then the new events. No rebuild |
| Too many deletes while off (100k), layout change, store written by an older ncli, or never built | `rebuild` (state `needs_rebuild`, `reason` says which) |
| Relay stopped or crashed mid-rebuild | The job reads `interrupted`; the rebuild resumes where it stopped (search starts its build index over) |
| Relay died during the same index's job 3 times in a row within an hour | That job is `failed` with `parked: true` and not started again until `relay index rebuild` (or `drop`) |
| Scoring settings changed | stats `rescore`; search `rebuild` |
| Search index settings changed (an ncli upgrade, or `searchAbout`) | search `rebuild` into a new index, swapped in; the live index's settings are never changed in place |
| Meilisearch down | search `unreachable`; profiles wait in the store and go in once it answers |
| Meilisearch index deleted or recreated | search `rebuild` |
| A job failed, or panicked | Retried after 1, 5, then 15 minutes (`retry_at`); a panic fails the job, not the relay |
| zaps or stats being rebuilt or caught up | search's rebuild or rescore waits for it, since search scores read them |
| Feature turned off | Data kept, unused; `hint` names the `drop` that removes it |

- `zaps` is on with `cache.zaps.enabled`; `stats` with
  `cache.profiles.stats.enabled` or writable search (search scores use it);
  `search` with `cache.profiles.search.enabled`, not `readonly`.
- States: `ready`, `behind`, `needs_rebuild`, `unreachable`, `empty`,
  `disabled`, and while a job runs `building`, `catching_up`, `rescoring`,
  `dropping`. `action` is the job the relay starts next; `reason` why.
- `entries` is exact: zap receipts, followed profiles, Meilisearch
  documents. `behind` is events stored since the index was last current
  (for search, events of any kind); search also has `queued` (profiles to
  look at again), `last_task` (the last Meilisearch batch: `duration_ms`,
  `batch_documents`, `slowest_step`) and `warning` when its queue has
  grown faster than profiles were applied for 30 minutes.
- A search rebuild reads profiles only and counts them: `done`/`total` are
  profiles applied out of all stored profiles.
- A job has `kind` (`catch_up`, `rebuild`, `rescore`, `reap` frees old
  data, `drop`), `trigger`, `phase`, `done`/`total`, `applied`,
  `entries_before`/`entries_after`, `duration_ms` and a one-line `summary`.
  The same JSON comes from the API, `--json`, and each `--follow` line.
- `rebuild` of an index already rebuilding returns that job with `noop:
  true`; an admin rebuild or drop takes over a relay-started job (it ends
  `superseded`). Any other job in the way is `conflict` (exit 5,
  retryable), naming it.
- Every rebuild builds a new index and swaps it in (`--fresh` was
  removed, `usage`). A search rebuild never re-fetches a NIP-05 or
  lightning address (`--reverify` was removed; use `ncli relay verify
  <pubkey> --refresh`).
- `drop` only takes an index whose feature is off: one in use is
  `invalid_input` ("rebuild it instead"). Nothing to drop answers `noop`.
  For search it removes the Meilisearch index too, never on a read-only
  relay.
- Exit codes: a job ending `failed`/`cancelled`/`interrupted`/`superseded`
  under `--wait`/text mode is `internal` (exit 1, `input` = job id);
  `--timeout` passing is `conflict` (job keeps running); a disabled index
  is `usage` (exit 2); cancelling live search catching up is `unsupported`
  (exit 8); unknown index or job `not_found`; non-admin key `auth`;
  stopped relay `network`.
- Text mode watches by default (Ctrl-C stops watching, not the job;
  `--detach` returns at once); without a terminal it prints a line every
  5 s. `--json` returns at once unless `--wait` or `--follow`.
- `ncli relay stats`/`reindex`/`clear` no longer exist; they fail with
  `usage` naming the replacement. Reading the store with `dump`/`inspect`
  while the relay is stopped changes nothing.

### Profile checks, scores and NIP-85 stats

With `cache.profiles.search.enabled` or `cache.profiles.stats.enabled`, the relay
checks each profile's NIP-05, lightning address (`lud16`, else `lud06`)
and picture, and keeps the result. It fetches only when:

| When | What is fetched |
|---|---|
| A kind 0 brings a new or changed identifier | that field, right away |
| A result's `cache.profiles.checks.ttl` expires (default 14d; `"0"` never) | that field, by a background sweep |
| A timeout, block (403/429) or 5xx failed it | that field, after `retry.initial` doubling to `retry.max`, up to `retry.maxAttempts` |
| `ncli relay verify <pubkey> --refresh` | every field of that profile |

Never on an unchanged kind 0, a name/about-only edit, or an index rebuild.
A failed fetch keeps the previous result; only a 404 or a server that
doesn't list the pubkey marks it failed. Requests to one host are spaced
by `perHostInterval`.

```yaml
cache:
  profiles:
    checks:
      ttl: 14d
      perHostInterval: 250ms
      workers: 50
      retry: {initial: 1h, max: 24h, maxAttempts: 5}
    # NIP-85 user assertions, signed with nip11.privkey
    stats:
      enabled: true
      maxTargets: 500
    # override the built-in score weights (keys as in nmilat's config.yaml)
    scoring:
      social: {followers_weight: 20, zaps_weight: 20, per_sender_cap: 100000}
      verification: {nip05_score: 50}
```

```sh
# stored checks (status, next check, retries, last error), score, stats
ncli relay verify npub1... bob@example.com
# check now; the only way to force a fetch
ncli relay verify npub1... --refresh --json
```

- Score = base (name/about/picture, penalties) + checks (NIP-05 50,
  lightning 20/chain up to 40; a broken picture takes its +30 back) +
  social: `20·log10(1+followers)` capped at 100, plus received zaps in
  sats weighed per sender — each sender adds at most `per_sender_cap` sats,
  scaled by that sender's own score, so throwaway keys add little.
- A zap counts when its receipt validates (NIP-57/NIP-AZ), isn't a self-zap
  or a duplicate payment, and — once the recipient's LNURL server key is
  verified — that server signed it. The zap ranking uses the same rules.
- Each zap's coin comes from its invoice (`lnbc…` sats, `lnfc…` loki), else
  a 9521's `chain` tag; amounts are never added across coins.
- `stats` answers `{"kinds":[30382],"#d":[<pubkey>...]}` (no
  `authors`, or the relay's key) with one signed kind 30382 per known
  profile, then EOSE: `rank` (0–100), `followers`, `following`,
  `zap_cnt_recd`/`zap_cnt_sent` (every coin), `zap_amt_recd`/`zap_amt_sent`
  (sats) plus `zap_amt_recd_loki`/`zap_amt_sent_loki` when there are any
  (each other coin in `cache.zaps.coins`),
  `zappers_recd`, `zap_first_recd`/`zap_last_recd`/`zap_first_sent`/
  `zap_last_sent` (unix), `nip05_verified`/`lud16_verified` (the
  identifier). No `#d` is `CLOSED restricted:`; more than `maxTargets` is
  `CLOSED invalid:`. An unchanged profile keeps its event id. NIP-11 lists 85.
- `maxTargets` is really capped by `nip11.limitation.max_message_length`: a
  `#d` pubkey costs ~67 bytes, so 256 KiB fits ~3,900. A larger value fails
  at startup (`invalid_input`).
- `cache.profiles.stats.enabled` without `nip11.privkey` fails at startup
  (`usage`); a bad duration or unknown `cache.profiles.scoring` key is
  `invalid_input`.
- Keys from before `cache` was grouped fail at startup with `usage` naming
  the new one: `cache.topZapped` → `cache.zaps`, `cache.search` →
  `cache.profiles.search` (its `host`/`key`/`index_name` under
  `meilisearch`), `cache.profileStats` → `cache.profiles.stats`,
  `cache.verification` → `cache.profiles.checks`, `cache.scoring` →
  `cache.profiles.scoring`, `verificationWorkers` →
  `cache.profiles.checks.workers`, search's `batch_size` → `batchSize`.
  Search's `queueSize` and `max_ch_size` were removed and fail at startup
  too: profiles reach search from the store, nothing is queued in memory.

### Zap ranking and the relay's own stats

```yaml
cache:
  zaps:
    enabled: true
    # what a client may ask for; the first of each is the default
    windows: [24h, 7d, 30d, all]
    coins: [sats, loki]
    limit: 50
    maxLimit: 200
    # how long a computed list is reused
    refresh: 1m
  relay:
    stats:
      enabled: true
      interval: 1m
```

```sh
# top 10 receivers of loki over the last week
cat > ranking.yaml <<'EOF'
kind: targets
spec:
  relays: [ws://127.0.0.1:5500]
  filters:
    - cache: ["zap-ranking", {window: 7d, coin: loki, limit: 10}]
EOF
ncli find -t ranking.yaml
```

- `zap-ranking` answers one signed kind 25521, then EOSE and CLOSED. Tags:
  `window`, `coin`, `limit` (after the cap); `created_at` is when the list
  was computed; content `[{"pubkey","total"}]` in whole sats or loki,
  highest first. An unlisted window or coin, any other cache action
  (including the old `top-zapped`), or bad options is `CLOSED invalid:`;
  zaps off is `CLOSED restricted:`.
- Every window and coin is computed in one pass on first use, reused for
  `refresh`, served stale while one refresh runs, skipped when nothing was
  written, and dropped after 10 idle `refresh` periods.
- `cache.relay.stats` publishes a NIP-66 kind 30166 signed with
  `nip11.privkey`, `d` = `nip11.url` (required): content is the NIP-11
  document; tags `n`, `N`, `R`, `k` (only with NIP-86 kind rules),
  `["count","events"|"authors",n]`, `["count","profiles",n]` with search or
  stats on, and with zaps on `["count","zap-receipts",n]`,
  `["total","zaps",<whole units>,<coin>]` per coin and
  `["zap-ranking","windows"|"coins",...]`. It's rebuilt each `interval` only
  if the store or NIP-11 changed and stored only if a tag differs, so a
  subscriber gets each new version live. Zap totals come from the ranking's
  cache, so they can lag by up to `cache.zaps.refresh`.
- NIP-45 COUNT with only `kinds` (or nothing) is exact from the relay's
  counters; any other filter that hits `max_limit` answers
  `"approximate": true`.

### Prometheus and the dashboard

```yaml
admin:
  listen: 127.0.0.1:5501
  dashboard:
    enabled: true       # /admin/ui/, NIP-07 sign-in, 15m sessions
  metrics:
    enabled: true       # /metrics, bearer tokens only
    tokensFile: /etc/ncli/metrics.tokens
```

```sh
# prints the token once; the file keeps only its sha256 (mode 0600)
ncli relay metrics token add prometheus --config relay.yaml
# open the dashboard locally, signed by this CLI (no admin.dashboard needed)
ncli relay dashboard --config relay.yaml
```

- The relay **refuses to start** if `admin.listen` isn't loopback without
  `admin.tls`, equals the relay port, or `metrics` lacks a valid tokens file
  (missing, readable by others, or holding a plaintext token). A taken
  *default* admin port only warns.
- The listener answers only to its own address (plus `localhost` on
  loopback); behind a reverse proxy list its name in `admin.hosts`, or every
  request gets `421`. This is what stops a NIP-98 signature the admin gave
  another site from working here.
- Who may sign in: the relay's key, `nip86.admins`, and `admin.pubkeys`
  (npub or hex) -- list personal keys there so admins don't use the relay's.
  The dashboard signs in with a NIP-07 extension, an nsec (signed in the
  page, never sent; "remember" saves it in that browser only as a NIP-49
  `ncryptsec` under a password, unlocked on the next visit; "stay signed in"
  keeps it in the tab's memory to renew the session) or a NIP-46 signer (`bunker://` URL, or a
  `nostrconnect://` QR for Amber/nsec.app/`ncli bunker connect`). Its CSP
  allows WebSockets to any `wss://` relay (and `ws://localhost`) for that.
- A browser opening the relay's public URL gets a homepage built from its
  NIP-11, the operator's kind 0 and public NIP-29 groups. WebSocket and
  NIP-11 clients are unaffected; `homepage: false` turns it off.
- Credentials don't cross: a metrics token opens only `/metrics`; a NIP-98
  signature or dashboard session opens only `/admin/api/*`.
- Tokens rotate without a restart: add the new one, switch the scraper,
  `token remove` the old one; the relay rereads the file within a second.
- Metric names are `ncli_relay_*` (plus Go/process collectors); labels are
  bounded (message type, NIP-01 reason, kind class), never pubkeys or IPs.
  Scrapes are cached 5s (`admin.metrics.scrapeCacheTTL`).

Full route, metric and config reference: `references/admin-plane-reference.md`.

## Gotchas learned

- `ncli relay index`/`jobs`/`metrics`/`members`/`invites`/`roles` invoked with no
  further subcommand (or a misspelled one) is a `code: "usage"` error
  (exit 2), same as any other invocation mistake. It prints the group's
  help on stderr to show you the subcommands, but it still **exits 2** --
  never a help dump with exit 0. A misspelled subcommand additionally
  leads with `Error: unknown command "..."`.
- Every startup failure now goes through the same reporting path (no
  bypass via `log.Fatal`, which used to skip the `--json`/structured-error
  path entirely for the two failures below). Config-validation problems
  are caught before the server binds and classified specifically: missing
  `nip11.pubkey`/`store`/`cache.zaps.enabled`-without-`nip11.privkey`
  are `code: "usage"` (exit 2); a malformed `nip11.privkey` or a
  `nip11.pubkey`/derived-pubkey mismatch is `code: "invalid_input"` (exit
  3, the mismatch case echoing both pubkeys). Failures once the server is
  actually starting (event-store directory/open failure) fall back to
  `code: "internal"` (exit 1) -- classify these further yourself if you can
  tell from the message whether it's a permissions issue vs. the store
  already being held open by another running relay process.
- `cache.zaps.enabled: true` without `nip11.privkey` set fails with
  `cache.zaps.enabled requires nip11.privkey` — checked at config-load
  time, before the server binds. A bad window, coin, `limit` above
  `maxLimit`, or duration under `cache` is `invalid_input` (exit 3) at the
  same point; durations take `ms`/`s`/`m`/`h`/`d`/`w`/`mo`.
- `membership.enabled: true` without `nip11.privkey` set fails the same
  way (`membership.enabled requires nip11.privkey`) — the relay signs its
  own role/membership-list/add-remove-user/invite-response events with it.
  `nip11.limitation.membership_required: true` additionally requires both
  `membership.enabled: true` and `nip11.limitation.auth_required: true`;
  missing either is a config-load-time error, not a runtime surprise.
- Per-invite expiry/use limits (the `createclaimwithlimits` method and the
  `invites create --expires-at/--no-expiry` flags) read two optional
  `membership` keys: `inviteMaxTTL` (Go duration string, default `720h` = 30
  days) caps how far in the future an explicit absolute expiry may be, and
  `inviteAllowNoExpiry` (bool, default false) gates whether a never-expiring
  code (`expires_at: 0`) is allowed at all. An unparseable or non-positive
  `inviteMaxTTL` is a config-load-time error. A requested limit outside
  policy is refused, never silently clamped. `memberClaims` (bool; unset,
  false on a members-only relay and true otherwise) lets a plain member mint a
  code via `createclaim` when membership is on; false keeps the management API
  admin-only. See "Member claims"
  under the Relay Management API section above, including the no-per-member-cap
  note.
- `inviteMaxUses` (int) is how many joins one code admits when the request
  sets no limit of its own. It defaults to `0`, which means **unlimited**: a
  default code keeps admitting people until it expires. Set it (often `1`)
  on any relay that hands out codes.
- Turning on `nip11.limitation.membership_required` is a real behavior
  change, not just a new knob: EVENT publishing now checks that the
  *specific pubkey that signed the event* is authenticated on the
  connection and holds membership — not merely "is anything authenticated
  on this connection," which is all `auth_required` alone ever checked.
  Join Requests (kind 28934) and Leave Requests (kind 28936) are exempt
  from this gate by definition (a non-member's whole point in sending one
  is to become a member).
- `agent_auth.enabled: true` without `nip11.limitation.membership_required`
  set fails with `agent_auth.enabled requires
  nip11.limitation.membership_required` — checked at config-load time.
  Enabling it is also a real behavior change beyond adding the option:
  once on, an AUTH from a non-member with no NIP-OA credential now fails
  AUTH itself (`OK false`), rather than succeeding AUTH and only failing
  later at the membership gate. An agent whose owner is later removed as
  a member loses access on its *next* connection attempt only — an
  already-open session is not forcibly terminated.
- NIP-29 group hosting has no config toggle of its own -- it's always on
  (`nip29/relayreg` is unconditionally linked into the binary) -- and a
  fresh group defaults to **private and closed**. That default means
  `nip11.url` is effectively required the moment anyone creates a group,
  not just when `auth`/`membership` above turn it on deliberately: NIP-42
  AUTH validates a client's `relay` tag against it, and without it every
  AUTH attempt fails that check, silently, which leaves a private group
  invisible even to its own creator via `ncli groups show`/`list` --
  indistinguishable from the relay never having created it at all. Set
  `nip11.url` from the start on any relay hosting groups, even one with no
  other auth-dependent feature turned on. See `ncli-groups`'s own skill
  for the `groups` command surface.
- `ncli relay context add` stores the config file's *absolute* path and
  requires the file to already exist -- it's a saved shortcut to a path,
  not a copy of the file. `context remove` only deletes the shortcut, never
  the underlying config file; removing the current context clears which
  one is "current" (falls back to cwd/`$HOME` discovery) rather than
  picking another one automatically.
- `limitation.max_message_length` left unset (`0`) does **not** mean "no
  limit" — the relay actually enforces `1,101,005` bytes and substitutes
  that value into the advertised NIP-11 doc instead of `0`, to avoid
  falsely advertising "accepts no messages." Same substitution pattern for
  `max_limit` (555,555), `max_subscriptions` (355), `max_indexable_tags`
  (5) when each is left at zero.
- POST /query and POST /events live under a nested `httpBridge:` key
  (`httpBridge.query.enabled`/`httpBridge.events.enabled`), not bare
  top-level `query:`/`events:` keys. The old flat shape mounts neither
  endpoint: startup only warns about the unknown keys, `--check` fails on
  them. See `examples/relay/app-backend-relay.yaml`.
- Gift wraps (kind 1059) are served only to the pubkey their `p` tag names,
  over REQ, COUNT and `/query`, unless `recipientOnlyKinds` says otherwise
  (omitted means `[1059]`; `[]` turns it off). An anonymous REQ for 1059 is
  closed `auth-required:`, so a client must AUTH as the recipient --
  `dm read` does this with its `--signer`; `find` needs `--auth-identity`.
- Seat notices (`groups.membershipNotices`) are stored, so a client that
  reconnects with `since` gets the ones it missed; read them with
  `{"kinds":[44100,44101],"#p":[<you>]}` after AUTH as that pubkey. A
  client can't publish those kinds (or the presence query kind): only the
  relay's key may.
- Blossom on the relay takes uploads whose kind 24242 token names the
  relay's host in a `server` tag and the blob's hash in an `x` tag; a
  token with no `server` tag is refused. `ncli blossom upload --server
  https://<relay>` signs exactly that. The relay origin is the Blossom
  server for every command it serves: upload, download, HEAD and `rm`
  all work at the root (`/<sha256>[.ext]`), and the URL an upload returns
  is `/<blobPrefix>/<sha256>.<ext>` (`/media` by default), which also
  serves the blob. It has no `list`, `mirror` or `upload --optimize`:
  those answer 404 with a "not supported" reason, which `ncli blossom`
  reports as `unsupported` (exit 8). Keep S3 keys out of `relay.yaml`:
  leave `accessKey: ""`/`secretKey: ""` and set
  `NCLI_BLOSSOM_STORAGE_S3_ACCESSKEY`/`_SECRETKEY` (an `NCLI_*` variable
  only overrides a key the file already has).
- NIP-16 ephemeral event kinds (20000-29999) need no config anywhere --
  handling is automatic based on an event's own kind, not a toggle. Publish
  one against any relay to see it: accepted and briefly queryable, then
  expired rather than persisted (10 minutes by default; see nmilat/relay's
  store.go).
- `nip11.pubkey` is optional if `nip11.privkey` is set — it's derived
  automatically and must match if both are present (config-validation error
  otherwise). The admin commands use the same derivation
  if `nip11.pubkey` is absent from config but `nip11.privkey` is present.
- `pow.min` is a difficulty *requirement*, not a discount: it never lets an
  event with insufficient PoW skip other checks, and it does nothing at all
  unless `pow.strict: true` — with `strict` unset/false, `min` only shows up
  in the relay's advertised `nip11.limitation.min_pow_difficulty` (a heads-up
  to clients), while every event is still accepted regardless of its actual
  difficulty. `nip11.limitation.min_pow_difficulty` (like
  `nip11.supported_nips`) is not a config key: a start warns that it is
  unknown and ignores it, `--check` fails on it. Set `pow.min` instead.
- Both `store` and `logdir`'s parent directories are auto-created on `ncli
  relay` startup, but a missing `store` value is caught at config-load time
  (before either directory is touched), same as a missing `nip11.pubkey`.
