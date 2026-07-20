# ncli apply — full field reference

<!-- Mirrors ohstr/ncli's client/spec.go as of writing. Update by hand if
the Go types change. -->

## `stream` (kind: stream)

| Field | Type | Default | Notes |
|---|---|---|---|
| `from` | list of flows | required, non-empty | Sources to read events from |
| `to` | list of flows | required, non-empty | Destinations to write matched events to |
| `filters` | list of NIP-01 filters | matches everything | OR'd together; see filter grammar below |
| `timeouts.handshake` | Go duration string | relay-client default | WebSocket handshake timeout |
| `timeouts.ping` | Go duration string | relay-client default | Ping interval |
| `timeouts.pong` | Go duration string | relay-client default | Pong wait timeout |
| `timeouts.write` | Go duration string | relay-client default | Write timeout |
| `recovery.max_retries` | int | internal default | Retry attempts for events that failed to publish |
| `recovery.retry_interval` | Go duration string | internal default | Delay between retries |
| `recovery.store_path` | string | OS temp dir, keyed to this stream's destinations | Override for a durable/fixed recovery store location |
| `raw` | bool | `false` | `true` disables the TUI, logs to stdout instead |
| `strictPow` | bool | `false` | Reject untrusted events whose `nonce` tag fails NIP-13 validation; overridden by `apply --strict-pow` when passed explicitly |

Recovery is **always on**, even if the whole `recovery:` block is omitted —
only its tuning is optional.

## `sync` (kind: sync)

| Field | Type | Default | Notes |
|---|---|---|---|
| `from` | single flow | required | One of `from`/`to` must be local, the other remote (either way round) |
| `to` | single flow | required | See `from` |
| `direction` | `both`\|`up`\|`down` | `both` | `up` = push local→remote only, `down` = pull remote→local only |
| `filters` | list of NIP-01 filters | matches everything | Scopes which events participate |
| `maxReconcileRounds` | int | `20` | Cap on negentropy round trips |
| `pullBatchSize` | int | `100` | Events requested per pull batch |
| `strictPow` | bool | `false` | Drop pulled events whose `nonce` tag fails NIP-13 validation instead of inserting them locally; overridden by `apply --strict-pow` when passed explicitly |
| `timeouts.*` | same as `stream` | same defaults | |

Unlike `stream`/`inspect`, `from` and `to` are each a single flow object
here, not a list — the 1:1 local/remote relation is baked into the schema
itself.

Validation (at spec-load time, before any network activity):
- `from` key missing entirely → `undefined \`from\` flow` (same for `to`)
- Both `from` and `to` resolve to local → `multiple local stores defined (only one allowed for sync)`
- Both resolve to remote → `multiple remote relays defined (only one allowed for sync)`
- Neither resolves to local (e.g. wrong flow type) → `undefined local store in flows`
- Neither resolves to remote → `undefined remote relay in flows`
- `direction` outside `both`/`up`/`down` → `invalid direction: <value> (must be both, up, or down)`

## `inspect` (kind: inspect)

| Field | Type | Default | Notes |
|---|---|---|---|
| `targets` | list of flows | may be empty | Mix of remote relays and local store paths to query |
| `filters` | list of NIP-01 filters | matches everything | Same grammar as `stream`/`sync` |

Never writes to a target or publishes — read-only across `targets`. The
TUI's events table lets you save individual matched events to a local
JSON file on request (see `SKILL.md`'s TUI section); that save is always
local and user-initiated, never a write to a `target`.

## Flow entry shape (`stream`'s/`inspect`'s `from`/`to`/`targets`, and `sync`'s single `from`/`to`)

Bare string shorthand:
- `"wss://relay.example.com"` → remote relay
- any other string → local path (must already exist unless the containing
  entry uses the object form with `ensure: create`)

Full object form:

| Field | Applies to | Notes |
|---|---|---|
| `relay` | remote | Relay URL, must be `ws://`/`wss://` with a non-empty host |
| `path` | local | Filesystem path to a local event store |
| `trusted` | both | Marks the flow as trusted (skips signature/format validation, and NIP-13 PoW enforcement if `strictPow`/`--strict-pow` is set, on ingested events) |
| `ensure` | local only | `exists` (default — errors if the path is missing) or `create` (creates it) |
| `writeConcurrency` | local destination only | int, default `32` — concurrent workers for this flow's writes; has no effect on a flow that's only ever read from |

## Duration-unit grammar (used inside `filters`)

Units: `s` (seconds), `m` (minutes), `h` (hours), `d` (days), `w` (weeks),
`mo` (30-day simplified month). Combine multiple units in one string, e.g.
`"1w 2d"`. A bare integer string (no unit) is parsed as an absolute unix
timestamp, not a relative duration.

`since`/`until` sign-direction table — the string's own `+`/`-` prefix sets
a sign applied to the parsed duration; `since` subtracts that signed
duration from now, `until` adds it, which is why the two react to `-` in
opposite, easy-to-get-backwards ways:

| Field | bare value (e.g. `"3d"`) | `-` prefix (e.g. `"-3d"`) | `+` prefix |
|---|---|---|---|
| `since` | N ago (looks **backward**) — the intuitive reading | flips to N **from now** — counter-intuitive, avoid | no-op, same as bare |
| `until` | N **from now** (looks forward) — the intuitive reading | flips to N **ago** — the intuitive one to reach for if you want "until N ago" | no-op, same as bare |

In short: for `since`, never prefix with `-` unless you specifically want a
future timestamp. For `until`, a `-` prefix is the normal way to say "up
until N ago." (This table is intentionally duplicated in the `ncli-query`
skill's own filter reference — each skill is self-contained and may be
installed independently of the other.)
