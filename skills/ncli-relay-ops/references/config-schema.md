# relay.yaml — full field reference

<!-- Mirrors ohstr/ncli's cli/relay/command.go (RelayConfig and friends) as
of writing. Update by hand if the Go types change. -->

## Top level

| Field | Type | Default | Notes |
|---|---|---|---|
| `port` | int | `5500` | |
| `nip11` | object | required | See below |
| `store` | string | required | Path to the bbolt event store |
| `logdir` | string | cwd if unset | Also read by `logs:` block's own default filename |
| `logs` | object | see below | Optional custom rotation |
| `cache` | object | all off | Optional serving features: `cache.profiles.*` (search, stats, checks, scoring), `cache.zaps.*`, `cache.relay.stats.*` — see below |
| `pow` | object | no requirement | NIP-13 proof-of-work gate (`pow.strict`, `pow.min`) — see below |
| `membership` | object | disabled | NIP-43 relay membership (`membership.enabled`, invite/join/leave flow) |
| `agent_auth` | object | disabled | NIP-AA agent authentication (`agent_auth.enabled`); requires `membership` above |
| `groups` | object | only `admin` moderates | NIP-29 group hosting. `roles` maps a role name to the moderation kinds (9000-9020) it may send, e.g. `moderator: [9005, 9010]`; must define `admin` (a group's creator holds it); names are lowercase. `roleDescriptions` (role -> text) fills each group's kind 39003. `admins` (npub/hex) moderate every group, as the relay's own key always does. `moderationLog: true` keeps accepted moderation events for members (invites for admins). `pendingJoins: true` makes a closed group keep a codeless join for its admins instead of refusing it. A bad entry fails startup with `invalid_input` |
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
`cache.scoring`, top-level `verificationWorkers`, `index_name`,
`batch_size`, `max_ch_size`) fails startup with `usage` naming its new key.

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
| `batchSize` | int | Profiles per indexing request |
| `queueSize` | int | Pending profiles buffered for indexing |

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
