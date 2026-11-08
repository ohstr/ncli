# relay.yaml — full field reference

<!-- Mirrors ohstr/ncli's cli/relay/command.go (RelayConfig and friends) as
of writing. Update by hand if the Go types change. -->

## Top level

| Field | Type | Default | Notes |
|---|---|---|---|
| `port` | int | `5500` | Binds every interface; `NCLI_PORT` applies even with no `port` key |
| `listen` | `host:port` | unset | The one address the relay binds; wins over `port`, and the admin listener defaults to `127.0.0.1:<its port + 1>` (set `admin.listen` on port 65535). Prefer an IP literal: a hostname with several addresses binds only one. `--check` rejects stray whitespace or a host that doesn't resolve. `NCLI_LISTEN` applies even with no `listen` key |
| `nip11` | object | required | See below |
| `store` | string | required | Path to the bbolt event store |
| `logdir` | string | cwd if unset | Also read by `logs:` block's own default filename |
| `logs` | object | see below | Optional custom rotation |
| `cache` | object | all off | Optional serving features: `cache.profiles.*` (search, stats, checks, scoring), `cache.zaps.*`, `cache.relay.stats.*` — see below |
| `pow` | object | no requirement | NIP-13 proof-of-work gate (`pow.strict`, `pow.min`) — see below |
| `membership` | object | disabled | NIP-43 relay membership (`membership.enabled`, invite/join/leave flow) |
| `agent_auth` | object | disabled | NIP-AA agent authentication (`agent_auth.enabled`); requires `membership` above |
| `groups` | object | only `admin` moderates | NIP-29 group hosting. `roles` maps a role name to the moderation kinds (9000-9020) it may send, e.g. `moderator: [9005, 9010]`; must define `admin` (a group's creator holds it); names are lowercase. `roleDescriptions` (role -> text) fills each group's kind 39003. `admins` (npub/hex) moderate every group, as the relay's own key always does. `moderationLog: true` keeps accepted moderation events for members (invites for admins). `pendingJoins: true` makes a closed group keep a codeless join for its admins instead of refusing it. `membershipNotices: {enabled, added: 44100, removed: 44101}` signs a seat notice (`p` member, `h` group, `e` cause) whenever a pubkey gains or loses a seat, by any path, readable only by that member and relay admins; needs `nip11.privkey`. A bad entry fails startup with `invalid_input` |
| `recipientOnlyKinds` | int list | `[1059]` | Kinds served only to a reader authenticated as a pubkey in the event's `p` tags (REQ, COUNT, `/query`, `/count`); `[]` serves every kind to anyone |
| `httpBridge` | object | all off | NIP-98 HTTP endpoints: `query.enabled` (`POST /query`), `events.enabled` (`POST /events`), `count.enabled` (`POST /count`, NIP-45: the same request and access rules as `/query`, answered `{"count": n}`) |
| `presence` | object | off | `enabled`, `kind` (default 40902): `POST /query` for `{"kinds":[40902],"authors":[...]}` answers one relay-signed kind 20001 per author (`online` while connected, its own latest kind 20001 status, else `offline`), never stored. Needs `nip11.privkey` and `httpBridge.query.enabled` |
| `blossom` | object | off | Blossom (BUD-01/02) on the relay's origin: `enabled`, `publicURL` (default from `nip11.url`), `uploadPaths` (`[/upload, /media/upload]`), `blobPrefix` (`/media`), `maxBlobBytes` (100 MiB), `maxUploadsPerSigner` (2), `maxConcurrentUploads` (8), `membersOnly.upload` (default `limitation.membership_required`), `membersOnly.download` (`false`), `storage.kind` `dir` (`storage.dir`, default `<store>.blobs`) or `s3` (`storage.s3.endpoint/bucket/region/prefix/accessKey/secretKey/useSSL`, e.g. MinIO) |
| `limits` | object | no limits | Rate limits — see `## limits` below. A limit that isn't set is no limit |
| `handshakeTimeout` | Go duration string | relay-client default | |
| `pingInterval` | Go duration string | relay-client default | |
| `pongTimeout` | Go duration string | relay-client default | |
| `writeTimeout` | Go duration string | relay-client default | |
| `outgoingBufferSize` | int | `1024` | Session engine tuning |
| `maxConcurrentStoreTasks` | int | `2048` | Session engine tuning |
| `pprofAddr` | `host:port` | unset (off) | Serves `net/http/pprof`, e.g. `curl 127.0.0.1:6060/debug/pprof/goroutine?debug=2`. Unauthenticated: keep it on loopback |

## `nip11`

| Field | Type | Default | Notes |
|---|---|---|---|
| `name` | string | `"ncli Relay"` | |
| `pubkey` | string | derived from `privkey` if set | Required if `privkey` unset |
| `privkey` | string | none | If set, `pubkey` is auto-derived and must match if both are given |
| `contact` | string | — | |
| `description` | string | `"ncli relay"` | |
| `url` | string | none | Relay's own canonical URL (e.g. `wss://relay.example.com`). Required if `limitation.auth_required` is `true`; fatal at config load if missing — see below |
| `limitation.auth_required` | bool | `false` | Requires `nip11.url` to be set |
| `limitation.membership_required` | bool | `false` | NIP-43 access gate. Requires `membership.enabled` **and** `limitation.auth_required`; fatal at config load if either is missing |
| `limitation.max_limit` | int | `555555` | 0/unset falls back to this default |
| `limitation.max_message_length` | int | `1101005` | 0/unset falls back to this default — see gotchas in the main SKILL.md |
| `limitation.max_subscriptions` | int | `355` | 0/unset falls back to this default |
| `limitation.max_indexable_tags` | int | `5` | 0/unset falls back to this default |
| `self` | string | mirrors `pubkey` | Not directly configurable — set automatically to `pubkey` when `membership.enabled` is `true`. NIP-43's relay-authored events (role definitions, membership lists, add/remove-user, invite responses) must be signed by this identity |

`Software`/`Version` in the advertised NIP-11 document are always
overwritten from ncli's own build info, regardless of what (if anything) is
in the config.

## `logs` (optional; if omitted, logs go to `<logdir>/nrelay.log` with the
defaults shown here)

| Field | Type | Default |
|---|---|---|
| `level` | `debug`/`info`/`warn`/`error` | `info` (per-connection and rejected-client lines are `debug`) |
| `file` | bool | `true`; `false` logs to stderr only, e.g. in a container |
| `filename` | string | `<logdir>/nrelay.log`; the rotation fields below apply only when it's set |
| `maxSize` | int (MB) | `100` |
| `maxBackups` | int | `3` |
| `maxAge` | int (days) | `28` |
| `compress` | bool | `true` |

The file is JSON lines (`jq` reads it); stderr stays human-readable unless `--json`.

## `limits`

Every limit is off until set; without the block nothing is limited. Each
rate is `{events, per, burst}`: `events` per `per` (Go duration), up to
`burst` at once (default `events`). `events` needs `per` and the other way
round; a negative number, bad duration, bad CIDR, a rate under 1ns an event
or a `burst` whose window overflows fails `--check` with `invalid_input`
naming it. `--check` and startup warn about a public relay with no limits
and about a per-pubkey limit that never applies (see each row).

| Field | Type | Default | Notes |
|---|---|---|---|
| `events.perPubkey` / `events.perIP` | rate | none | EVENT over the WebSocket and `POST /events`, by author / client IP. Over the WebSocket the author's budget counts only on a connection NIP-42-authenticated as the author, for an event the relay doesn't have yet. Refused: `OK false "rate-limited: ..."` (429 over HTTP). With `exempt: members` and `limitation.membership_required` (no `agent_auth`), `perPubkey` never applies and is warned about |
| `reads.perPubkey` / `reads.perIP` | rate | none | REQ, COUNT and NEG-OPEN by NIP-42 pubkey / client IP; each AUTH also takes one from `perIP`. Refused: `CLOSED "rate-limited: ..."` (AUTH: `OK false`, NEG-OPEN: `NEG-ERR`) |
| `http.perPubkey` / `http.perIP` | rate | none | `POST /query`, `/count`, `/events`, by NIP-98 signer / client IP. Refused: 429 with `Retry-After` |
| `blossom.uploads.perPubkey` / `.perIP` | rate | none | Blossom uploads, by token signer / client IP. 429 with `Retry-After`. With `exempt: members` while only members may upload, `perPubkey` never applies and is warned about |
| `blossom.downloads.perPubkey` / `.perIP` | rate | none | Every other Blossom request (GET, HEAD, DELETE). While downloads are public (`blossom.membersOnly.download: false`) a GET or HEAD has no signer, so `perPubkey` counts only DELETEs and is warned about |
| `connectionsPerIP` | int | `0` (no cap) | Open WebSocket connections from one IP, the relay's and huddles' (`/huddle/{id}/audio`, `/rtc`) together; one more is refused 429 before the upgrade |
| `exempt` | `none`/`members` | `none` | `members`: NIP-43 members and admins (relay key, `nip86.admins`), unless banned, skip the per-pubkey limits. Per-IP limits apply to everyone |
| `trustedProxies` | CIDR/IP list | `[]` | Reverse proxies whose `X-Forwarded-For` names the client: the rightmost hop that isn't a trusted proxy, so the proxy must set or append `X-Forwarded-For` (passing a client's own through lets it pick its key). `X-Real-IP` is read only when there is no `X-Forwarded-For`; a client hop that isn't an address falls back to the remote address. List the proxy's own address (e.g. `[127.0.0.1]`): trusting a pod network trusts every in-cluster client to name itself. Without one, everyone behind a proxy shares its IP. IPv6 clients are keyed by their /64 |

Refusals are counted in `ncli_relay_rate_limited_total{action,by}` on
`/metrics`. `blossom.maxBlobBytes`/`maxUploadsPerSigner`/`maxConcurrentUploads` are not
part of `limits` and keep their defaults (100 MiB, 2, 8) either way.

## `pow`

| Field | Type | Default | Notes |
|---|---|---|---|
| `strict` | bool | `false` | `true` rejects (`OK false`, `"pow: ..."`) events whose real difficulty is below `min`. `false` only advertises `min` via NIP-11 `limitation.min_pow_difficulty` — under-difficulty events are still accepted |
| `min` | int | `0` | Required NIP-13 leading-zero-bit difficulty. `0` means no requirement regardless of `strict` |

`min` always flows into the advertised `nip11.limitation.min_pow_difficulty`,
whether or not `strict` is on — `nip11.limitation.min_pow_difficulty` set
directly in YAML is ignored; `pow.min` is the only way to set it, so there's
one source of truth. Omitting the whole `pow:` block is identical to `{strict:
false, min: 0}` — fully backward compatible with configs that predate it.

## `cache`

Durations take Go units (`250ms`, `1h`) or `d`/`w`/`mo` (`14d`); a bad one
is `invalid_input`. A key from before this grouping (`cache.topZapped`,
`cache.search`, `cache.profileStats`, `cache.verification`,
`cache.scoring`, top-level `verificationWorkers`, `index_name`) fails
startup with `usage` naming its new key; search's removed `queueSize` and
`max_ch_size` fail with `usage` too, and `batch_size` names `batchSize`.

| Field | Type | Default | Notes |
|---|---|---|---|
| `profiles.search` | object | disabled | Profile search via Meilisearch, see below |
| `profiles.stats` | object | disabled | NIP-85 user assertions (kind 30382), see below. The store keeps the stats (follows and zaps) index only while it or writable search is on |
| `profiles.checks` | object | see below | NIP-05/lightning address checks; run when search or stats is on |
| `profiles.scoring` | object | built-in weights | Overrides profile score weights by section: `scoring`, `verification`, `social`, with nmilat's `config.yaml` keys (e.g. `social: {followers_weight: 25}`). Unknown section or key is `invalid_input` |
| `zaps` | object | disabled | The signed `zap-ranking` cache action, see below. The store keeps a zap index only while it's on |
| `relay.stats` | object | disabled | The relay's own NIP-66 kind 30166, see below |

### `cache.profiles.search`

Indexes kind-0 profile fields only (name/about/nip05/lud16) — a client's
`search` filter against this relay is a **people** search, matched authors
then filtered by kind, never a full-text match over note content.

| Field | Type | Notes |
|---|---|---|
| `enabled` | bool | Must be `true` for `ncli relay` to initialize search |
| `readonly` | bool | Wraps the service in a read-only decorator |
| `meilisearch.host` | string | Meilisearch URL |
| `meilisearch.key` | string | Meilisearch API key |
| `meilisearch.index` | string | Index name |
| `batchSize` | int | Profiles per Meilisearch request, for a rebuild and the live queue (default `10000`, at most `100000`); a request is also capped at about 50 MB |
| `maxInFlight` | int | Requests enqueued before waiting for the oldest, so Meilisearch indexes them in one pass (default `4`, at most `64`) |
| `searchAbout` | bool | Whether profiles' about text is searchable (default `true`) |

Only name, display name, about, NIP-05 and LUD-16 are searched. A query
that is one npub, nprofile or 64-character hex key returns that profile
alone. When the index settings change (an ncli upgrade or `searchAbout`),
the relay rebuilds search into a new index and swaps it in; it never
changes a live index's settings. Meilisearch 1.55 or later is recommended
(see the README).

### `cache.profiles.stats`

| Field | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `false` | Requires `nip11.privkey` (signs the assertions); `usage` at config load without it |
| `maxTargets` | int | `500` | Most `#d` pubkeys per request; more is `CLOSED invalid:`. At most `max_message_length / 67` (~3,900 at 256 KiB), checked at startup |

### `cache.profiles.checks`

| Field | Type | Default | Notes |
|---|---|---|---|
| `ttl` | duration | `14d` | How long a result is kept before a background re-check; `"0"` never re-checks an unchanged identifier |
| `perHostInterval` | duration | `250ms` | Spacing between requests to one host |
| `workers` | int | `50` | Checks running at once |
| `retry.initial` | duration | `1h` | Wait after a timeout/403/429/5xx, doubling per attempt; a longer `Retry-After` wins |
| `retry.max` | duration | `24h` | Longest wait between retries |
| `retry.maxAttempts` | int | `5` | Failures in a row before giving up until the next `ttl` |

### `cache.zaps`

| Field | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `false` | Requires `nip11.privkey` (signs the ranking); `usage` at config load without it |
| `windows` | list | `[24h]` | Windows a client may ask for; the first is the default; `all` is all time. Empty or duplicate is `invalid_input` |
| `coins` | list | `[sats, loki]` | Coins a client may ask for; the first is the default |
| `limit` | int | `50` | List length when a request sets none; at most `maxLimit` |
| `maxLimit` | int | `200` | Cap on a request's `limit` |
| `refresh` | duration | `1m` | How long a computed list is reused |

### `cache.relay.stats`

| Field | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `false` | Publishes a NIP-66 kind 30166 about the relay. Requires `nip11.privkey` and a `ws(s)://` `nip11.url` (its `d` tag) |
| `interval` | duration | `1m` | How often it checks for changes; it republishes only when a tag changed |

## `membership` (optional, NIP-43)

Omitting the whole `membership:` block is identical to `{enabled: false}`
— fully backward compatible with configs that predate it.

| Field | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `false` | Requires `nip11.privkey`; fatal at config load if `true` without it |
| `inviteTTL` | Go duration string | `24h` | How long an issued invite claim (kind 28935) stays valid; unset/unparseable falls back to the default |
| `inviteMaxUses` | int | `0` (unlimited) | How many times a single invite claim may be consumed via a Join Request; must be `>= 0` |
| `publishAddRemoveEvents` | bool | `false` | Also publish a signed kind:8000/8001 event on membership changes, in addition to the relay's own internal membership store |
