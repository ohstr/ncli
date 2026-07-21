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
| `cache` | object | all off | Cache-response and search features (`cache.topZapped.enabled`, `cache.topZapped.window`, `cache.search.*`) |
| `pow` | object | no requirement | NIP-13 proof-of-work gate (`pow.strict`, `pow.min`) — see below |
| `membership` | object | disabled | NIP-43 relay membership (`membership.enabled`, invite/join/leave flow) |
| `agent_auth` | object | disabled | NIP-AA agent authentication (`agent_auth.enabled`); requires `membership` above |
| `handshakeTimeout` | Go duration string | relay-client default | |
| `pingInterval` | Go duration string | relay-client default | |
| `pongTimeout` | Go duration string | relay-client default | |
| `writeTimeout` | Go duration string | relay-client default | |
| `outgoingBufferSize` | int | `1024` | Session engine tuning |
| `maxConcurrentStoreTasks` | int | `2048` | Session engine tuning |
| `verificationWorkers` | int | `50` | Session engine tuning |

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
| `filename` | string | `<logdir>/nrelay.log` |
| `maxSize` | int (MB) | `100` |
| `maxBackups` | int | `3` |
| `maxAge` | int (days) | `28` |
| `compress` | bool | `true` |

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

| Field | Type | Default | Notes |
|---|---|---|---|
| `topZapped` | object | all off | Signed "top zapped" cache response, see below |
| `search` | object | disabled | Optional Meilisearch integration, see below |

### `cache.topZapped`

| Field | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `false` | Requires `nip11.privkey`; fatal at config load if `true` without it |
| `window` | duration string | `24h` | Default window for "top-zapped" queries when a client's cache filter omits its own; units `h`/`mo`/`m`/`s`/`d`/`w` (same set as filter since/until). Empty or unparseable silently falls back to `24h` (logged as a warning) |

## `membership` (optional, NIP-43)

Omitting the whole `membership:` block is identical to `{enabled: false}`
— fully backward compatible with configs that predate it.

| Field | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `false` | Requires `nip11.privkey`; fatal at config load if `true` without it |
| `inviteTTL` | Go duration string | `24h` | How long an issued invite claim (kind 28935) stays valid; unset/unparseable falls back to the default |
| `inviteMaxUses` | int | `0` (unlimited) | How many times a single invite claim may be consumed via a Join Request; must be `>= 0` |
| `publishAddRemoveEvents` | bool | `false` | Also publish a signed kind:8000/8001 event on membership changes, in addition to the relay's own internal membership store |

## `cache.search` (optional, Meilisearch)

Indexes kind-0 profile fields only (name/about/nip05/lud16) — a client's
`search` filter against this relay is a **people** search, matched authors
then filtered by kind, never a full-text match over note content.

| Field | Type | Notes |
|---|---|---|
| `enabled` | bool | Must be `true` for `ncli relay` to initialize search |
| `readonly` | bool | Wraps the service in a read-only decorator |
| `host` | string | Meilisearch URL |
| `key` | string | Meilisearch API key |
| `index_name` | string | |
| `batch_size` | int | |
| `max_ch_size` | int | |
