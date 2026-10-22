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
`admin.listen`/`port` to find the relay.

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
want `cache.topZapped.enabled`, `cache.profileStats.enabled` or the admin
commands (`status`, `index`, `jobs`, `verify`, `members`, ...).

## Scenario examples

Each file under `examples/relay/` (besides `minimal.yaml` and `full.yaml`
below) is a real deployment shape, not just a feature demo -- copy the one
closest to what you're building and adapt its name/port/store.

| File | Scenario | Key fields beyond minimal |
|---|---|---|
| `personal-relay.yaml` | One person's own outbox/backup -- wide open, low stakes | Nothing beyond minimal, by design |
| `community-membership-relay.yaml` | Invite-gated community (paid newsletter, friend group, contributor channel) | `membership.enabled` + `nip11.privkey` + `auth_required`+`url` (+ optional `membership_required`) -- NIP-43 relay membership: enrolled pubkeys can REQ/EVENT, everyone else must invite-then-join first |
| `public-search-relay.yaml` | Public, high-traffic relay where search, "what's trending" and per-profile stats actually work | `cache.search.*` + `cache.topZapped.enabled` + `cache.profileStats.enabled` + `nip11.privkey` -- also the shape the admin commands need, since their auth requires `nip11.privkey` too |
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
`description`, `limitation.*`, `pow.*`, `cache.topZapped.*`,
`cache.profileStats.*`, `cache.verification.*`, `cache.scoring.*`,
`membership.*`, `nip86.*`, `agent_auth.*`, `httpBridge.*`,
`handshakeTimeout`/`pingInterval`/`pongTimeout`/`writeTimeout`,
`outgoingBufferSize`/`maxConcurrentStoreTasks`,
`logs.*` rotation, `huddle.*`, and full `cache.search.*`.
`examples/relay/minimal.yaml` is the bare quickstart `just dev relay` runs.

Full field table: `references/config-schema.md`.

## Running the relay

```sh
ncli relay --config relay.yaml
# validate only: exit 0, or the error the relay would fail to start with
ncli relay --config relay.yaml --check
```

Foreground process; stops cleanly on SIGINT/SIGTERM. Logs go to stderr and
to `<logdir>/nrelay.log` as JSON lines (or a custom `logs:` rotation block).
`logs.level: debug` adds per-connection lines; `logs.file: false` keeps
stderr only.

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
# every currently-stored code
ncli relay invites list
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
`listallowedpubkeys`, `createclaim` (optional code; generated when omitted),
`listclaims`, `deleteclaim`, and the role methods `createrole`, `editrole`,
`deleterole`, `assignrole`, `unassignrole`.

**`unallowpubkey` ends the member's live huddle calls**, not just their next
join — the other participants stay connected. `ncli relay members remove`
does the same.

## Monitoring and index jobs — relay must be running

Every running relay has an **admin listener** separate from its public
port: `admin.listen`, default `127.0.0.1:<port+1>` (`off` disables it).
`status`/`index`/`jobs` sign each request (NIP-98) with `nip11.privkey`,
which must be the relay's key or in `nip86.admins`. `--admin-url` overrides
where they connect.

```sh
# health and live metrics (--watch: every 2s; NDJSON with --json)
ncli relay status
# indexes (search, zaps, stats) with their running/last job and entry count
ncli relay index list
# rebuild zaps beside the live index and swap it in; wait with progress
ncli relay index rebuild zaps --follow
# search: rescore every profile from stored results, upserting in place;
# --fresh builds a new index and swaps it in
ncli relay index rebuild search --fresh --follow
# items a rebuild would scan, and entries now; nothing runs
ncli relay index rebuild all --dry-run
# empty an index (prompts; --yes required with --json or no terminal)
ncli relay index drop search --yes
# jobs: running first, then the last 20 (kept in <store>.jobs.json)
ncli relay jobs list
ncli relay jobs show <id> --follow
ncli relay jobs cancel <id>
```

- A rebuild is a job with an id. Starting the same rebuild while it runs
  returns that job (no duplicate); any other job on that index is
  `conflict` (exit 5, retryable). `--follow` exits non-zero unless the job
  succeeds; Ctrl-C stops following, not the job.
- No rebuild leaves an empty window: zaps and stats build a new generation
  and swap it atomically; `search --fresh` builds `<index_name>__next`,
  swaps, then catches up on profiles stored meanwhile.
- A search rebuild covers every stored profile, past `max_limit`. It
  rescores from stored check results, zaps and follows with the current
  `cache.scoring` weights and **never re-fetches** a NIP-05 or lightning
  address. `--reverify` was removed (it fails with `usage`); use
  `ncli relay verify <pubkey> --refresh`.
- `search` is `usage` (exit 2) when `cache.search.enabled` is off or
  `readonly` is set, and `zaps` when `cache.topZapped.enabled` is off; an
  unknown index or job id is `not_found` (exit 4); a key that isn't admin
  is `auth` (exit 7); a stopped relay is `network`.
- `estimate` is what the index holds: Meilisearch's document count for
  search (`unreachable` when Meilisearch doesn't answer), zap receipts for
  zaps, profiles with followers for stats. `scan_estimate` is what a rebuild
  reads (stored profiles for search, every stored event for zaps/stats).
  The store indexes zaps and follows on ingest even with top-zapped and
  profileStats off, so turning them on needs no rebuild; a store from an
  older version builds the stats index once in the background on start.
- `ncli relay stats`/`reindex`/`clear` no longer exist; they fail with
  `usage` naming the replacement.

### Profile checks, scores and NIP-85 stats

With `cache.search.enabled` or `cache.profileStats.enabled`, the relay
checks each profile's NIP-05, lightning address (`lud16`, else `lud06`)
and picture, and keeps the result. It fetches only when:

| When | What is fetched |
|---|---|
| A kind 0 brings a new or changed identifier | that field, right away |
| A result's `cache.verification.ttl` expires (default 14d; `"0"` never) | that field, by a background sweep |
| A timeout, block (403/429) or 5xx failed it | that field, after `retry.initial` doubling to `retry.max`, up to `retry.maxAttempts` |
| `ncli relay verify <pubkey> --refresh` | every field of that profile |

Never on an unchanged kind 0, a name/about-only edit, or an index rebuild.
A failed fetch keeps the previous result; only a 404 or a server that
doesn't list the pubkey marks it failed. Requests to one host are spaced
by `perHostInterval`.

```yaml
cache:
  verification:
    ttl: 14d
    perHostInterval: 250ms
    workers: 50
    retry: {initial: 1h, max: 24h, maxAttempts: 5}
  # NIP-85 user assertions, signed with nip11.privkey
  profileStats:
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
  social: `20·log10(1+followers)` capped at 100, plus received zaps
  weighed per sender — each sender adds at most `per_sender_cap` sats,
  scaled by that sender's own score, so throwaway keys add little.
- A zap counts when its receipt validates (NIP-57/NIP-AZ), isn't a self-zap
  or a duplicate payment, and — once the recipient's LNURL server key is
  verified — that server signed it. Top-zapped uses the same rules.
- `profileStats` answers `{"kinds":[30382],"#d":[<pubkey>...]}` (no
  `authors`, or the relay's key) with one signed kind 30382 per known
  profile, then EOSE: `rank` (0–100), `followers`, `following`,
  `zap_cnt_recd`/`zap_amt_recd`/`zap_cnt_sent`/`zap_amt_sent` (sats),
  `zappers_recd`, `zap_first_recd`/`zap_last_recd`/`zap_first_sent`/
  `zap_last_sent` (unix), `nip05_verified`/`lud16_verified` (the
  identifier). No `#d` is `CLOSED restricted:`; more than `maxTargets` is
  `CLOSED invalid:`. An unchanged profile keeps its event id. NIP-11 lists 85.
- `maxTargets` is really capped by `nip11.limitation.max_message_length`: a
  `#d` pubkey costs ~67 bytes, so 256 KiB fits ~3,900. A larger value fails
  at startup (`invalid_input`).
- `cache.profileStats.enabled` without `nip11.privkey` fails at startup
  (`usage`); a bad duration or unknown `cache.scoring` key is
  `invalid_input`. Top-level `verificationWorkers` still works but warns;
  use `cache.verification.workers`.

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
  `nip11.pubkey`/`store`/`cache.topZapped.enabled`-without-`nip11.privkey`
  are `code: "usage"` (exit 2); a malformed `nip11.privkey` or a
  `nip11.pubkey`/derived-pubkey mismatch is `code: "invalid_input"` (exit
  3, the mismatch case echoing both pubkeys). Failures once the server is
  actually starting (event-store directory/open failure) fall back to
  `code: "internal"` (exit 1) -- classify these further yourself if you can
  tell from the message whether it's a permissions issue vs. the store
  already being held open by another running relay process.
- `cache.topZapped.enabled: true` without `nip11.privkey` set fails with
  `cache.topZapped.enabled requires nip11.privkey` — checked at
  config-load time, before the server binds.
- `cache.topZapped.window` uses ncli's own duration units
  (`h`/`mo`/`m`/`s`/`d`/`w` — same set as filter `since`/`until`), not Go's
  stdlib duration format used by `pingInterval`/`pongTimeout`/
  `writeTimeout`/etc. An empty or unparseable `cache.topZapped.window` is
  *not* a config-load-time error: it silently falls back to 24h with a
  logged warning, so a typo here won't be caught until you notice
  "top-zapped" queries covering the wrong range.
- `membership.enabled: true` without `nip11.privkey` set fails the same
  way (`membership.enabled requires nip11.privkey`) — the relay signs its
  own role/membership-list/add-remove-user/invite-response events with it.
  `nip11.limitation.membership_required: true` additionally requires both
  `membership.enabled: true` and `nip11.limitation.auth_required: true`;
  missing either is a config-load-time error, not a runtime surprise.
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
  top-level `query:`/`events:` keys -- viper silently drops an unknown
  top-level key rather than erroring, so a config using the old flat shape
  loads with no complaint and simply never mounts either endpoint. See
  `examples/relay/app-backend-relay.yaml`.
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
  difficulty. Setting `nip11.limitation.min_pow_difficulty` directly in YAML
  does nothing — `pow.min` is the only field that reaches it.
- Both `store` and `logdir`'s parent directories are auto-created on `ncli
  relay` startup, but a missing `store` value is caught at config-load time
  (before either directory is touched), same as a missing `nip11.pubkey`.
