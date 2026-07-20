# filters.yaml — full field reference

<!-- Mirrors ohstr/ncli's client/spec.go (FilterSpec) as of writing. Update
by hand if the Go type changes. -->

`examples/filters.yaml` is a bare YAML array of filter objects — no
`kind:`/`spec:` wrapper. It isn't loaded by any flag directly; paste
entries from it under a `--targets` file's `filters:` key (see
`targets.yaml` below), or set the equivalent inline flags. Multiple
filters in an array/`filters:` list are OR'd; fields within one filter are
AND'd.

## Fields

| Field | Type | Notes |
|---|---|---|
| `ids` | list of strings | Event IDs. Any value shorter than 64 hex chars is a prefix match. All-digit values must be quoted. |
| `authors` | list of strings | Hex pubkeys (full or prefix) or nip-05 `name@domain` addresses, mixed freely — nip-05 entries are resolved to hex automatically. |
| `kinds` | list of ints | Event kinds |
| `since` | int (unix ts) or duration string | See sign table below |
| `until` | int (unix ts) or duration string | See sign table below |
| `limit` | int | Max events to return |
| `search` | string | NIP-50 full-text search; interpretation is relay-specific. Against an `ncli relay` with search enabled, this is a **people** search over kind-0 profile fields (name/about/nip05/lud16) — not note content — see `ncli-relay-ops` |
| `"#<letter>"` | list of strings | Generic tag filter, e.g. `"#e"` (referenced event ids), `"#p"` (referenced pubkeys). Key **must** be quoted. |

## `since`/`until` sign-direction table

Confirmed against `client/spec.go`'s `parseDurationUnit`/`parseDuration`:
the string's own `+`/`-` prefix sets a sign that's applied to the parsed
duration, and `since` subtracts that signed duration from now while `until`
adds it — which is why the two fields react to `-` in opposite,
easy-to-get-backwards ways.

| Field | bare value (e.g. `"3d"`) | `-` prefix (e.g. `"-3d"`) | `+` prefix |
|---|---|---|---|
| `since` | N ago (looks **backward**) — the intuitive reading | flips to N **from now** — counter-intuitive, avoid | no-op, same as bare |
| `until` | N **from now** (looks forward) — the intuitive reading | flips to N **ago** — the intuitive one to reach for if you want "until N ago" | no-op, same as bare |

In short: for `since`, never prefix with `-` unless you specifically want a
future timestamp. For `until`, a `-` prefix is the normal way to say "up
until N ago."

## Duration-unit grammar

Units: `s` (seconds), `m` (minutes), `h` (hours), `d` (days), `w` (weeks),
`mo` (30-day simplified month). Combine multiple units in one string, e.g.
`"1w 2d"`. A bare integer string (no unit) is parsed as an absolute unix
timestamp, not a relative duration.

## Quoting gotchas

- All-digit `ids`/`authors` values, and any all-digit string field in
  general, must be quoted (`"0000000000000000"`) — otherwise YAML parses
  them as numbers and unmarshalling into the string field fails.
- Tag filter keys (`"#e"`, `"#p"`, `"#t"`, ...) must be quoted — an
  unquoted leading `#` starts a YAML comment, so the field is silently
  dropped rather than raising a parse error. This is the more dangerous of
  the two quoting gotchas because it fails silently instead of loudly.
