---
name: ncli-miner
description: Mine or verify NIP-13 proof-of-work for a Nostr event with ncli's miner command -- author a note inline with --content/--content-file (parallel mining, with progress, auto-signed when an identity's private key is available), or from an unsigned event YAML/JSON file for anything richer. Then publish the result to relays with ncli publish, or check already-mined events for PoW compliance, from a file or fetched live from relays. Use when mining/publishing a note, choosing a difficulty (leading zero bits) or worker count, or auditing published events' PoW compliance.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's examples/event.yaml, cli/ncli/miner.go,
cli/ncli/publish.go, client/miner.go, and client/publish.go as of writing.
This skill is self-contained by design and won't see repo changes
automatically -- update by hand if flags/schemas change. -->

# ncli miner / ncli publish

`ncli miner` has two subcommands: `mine` (find a PoW nonce for an event,
signing it automatically when possible) and `check` (verify PoW on
already-mined events). `ncli publish` is the separate command that actually
sends a signed event to a relay -- `mine` never does this itself.

## Mine

The event to mine comes from **either** `--content`/`--content-file`
(inline authoring -- the common case) **or** `-e/--event` (a structured
file, for anything richer) -- pick one, not a mix.

### Inline authoring (`--content`/`--content-file`)

```sh
ncli miner mine --content "hello from ncli" --tag t=nostr --identity mykey -d 20 -o mined.json
# or, for longer content:
ncli miner mine --content-file note.txt --identity mykey -d 20 -o mined.json
```

- `--content <string>` / `--content-file <path.txt>` -- mutually exclusive;
  one is required if `-e/--event` isn't given. `--content-file` reads the
  file verbatim except for exactly one trailing newline (the file's own
  EOF convention, not part of the note).
- `--kind <int>` -- default `1`; only valid in this mode (rejected alongside
  `-e/--event`, which already declares its own `kind`).
- `--tag key=value` -- repeatable, builds simple 2-element tags (e.g. `--tag
  t=nostr`). Anything needing more elements (relay hints, markers,
  multi-value `e`/`p` tags) needs `-e/--event` instead.
- `created_at` is always "now" in this mode -- there's no file for a stale
  timestamp to hide in.
- `pubkey` has no source but `--identity` in this mode -- it's effectively
  required here (mine errors clearly if it's missing).

### Structured file (`-e/--event`)

For anything inline authoring can't express (a specific `created_at`, an
addressable event's `d` tag, richer tags, ...):

```yaml
# examples/event.yaml
pubkey: 3c1db3dd55e2ff09ba5317dd8eec2339797e9e2ddf74591172735c47f3a2ad6e
created_at: 1719759720 # <-- replace with the current unix time, e.g. `date +%s`
kind: 1
tags:
  - ["t", "nostr"]
content: "hello from ncli"
```

The file must be **unsigned** (`sig` omitted) and `pubkey` must already be
a valid 32-byte hex key, unless you pass `--identity`.

```sh
ncli miner mine -e event.yaml -o mined.json -d 20
```

`-e/--event` accepts `.json`, `.jsonp`, `.yaml`, or `.yml`.

### Shared flags

- `-d/--difficulty` -- leading zero bits, default `2` if omitted -- always
  pass it explicitly, since the default is low enough to be a near no-op.
- **Exactly one of `-o/--out` or `--in-place` is required** -- `mine` never
  overwrites `-e`'s file implicitly (and `--in-place` requires `-e/--event`
  in the first place -- there's no input file to overwrite in
  `--content`/`--content-file` mode). `-o`'s own extension governs the
  output format, independent of `-e`'s.
- `-w/--workers` -- parallel mining workers, default `0` (every available
  CPU core).
- `--progress-interval` -- how often to log mining progress (hashes tried,
  elapsed, hash rate), default `5s`; `0` disables it.
- `--identity <vault-label|npub|hex|nsec|nprofile|nip-05>` -- resolves a
  pubkey the same way `ncli id` does, and fills the event's empty `pubkey`
  field with it. Errors if the event already declares a *different* pubkey
  (`code: "invalid_input"`, the identity value echoed back in `input`
  unless it's an `nsec1...` string); a `name@domain` identity that fails to
  resolve is `code: "network"` instead (`retryable: true`).
- `--json` -- print the mined result (`id`, `nonce`, `difficulty`,
  `signed`, `out`) as structured JSON on stdout instead of the same fields
  as plain text on stdout; also silences progress logging (which stays on
  stderr either way -- mining progress/duration is narration, not part of
  the result). A global flag (available on every ncli command): it also
  switches every other stderr log line to single-line JSON, and a
  whole-command failure to `{"error", "code", "retryable", "input"}`.
  `-q/--quiet` (also global) drops info-level logging without switching to
  JSON.

### Auto-signing

If `--identity` resolves to a **private key**, the mined event is signed
automatically before being written -- no separate signing step:

- `--identity nsec1...` -- signs immediately, no vault/password involved.
- `--identity <vault label>` -- needs the vault unlocked: same
  `NCLI_VAULT_PASSWORD` (non-interactive) / interactive-prompt behavior as
  `ncli id --reveal`. In `--json` mode with no `NCLI_VAULT_PASSWORD` set,
  this is a `code: "usage"` error rather than a hang waiting for a prompt
  that can't happen.
- `--identity npub1.../hex/nprofile/nip-05` (not vault-saved) -- no private
  key exists for these forms; mine still succeeds, just **unsigned**
  (`sig` empty, `"signed": false` under `--json`) -- logged as a warning,
  not a silent gap.

Signing reuses the same identity that supplied the pre-mine pubkey, so the
mined `id` (which already hashes in the pubkey) stays valid after signing --
no re-mining needed.

## Publish

`ncli publish` sends one or more already-signed events to one or more
relays, waiting for each relay's `OK`:

```sh
ncli publish -e signed.json -s wss://relay.damus.io
ncli publish -e events.json -s relay.damus.io,relay.snort.social --json
```

- `-e/--events <file>` -- a single event object or a JSON array (same
  shapes `ncli miner mine`'s `-o` output and `ncli dump`'s `-o` output use
  -- no need to hand-wrap a single mined event in `[...]`).
- `-s/--relays <comma-list>` -- remote relay URLs only (no local-store
  targets yet); omit it to fall back to the relays configured via `ncli
  prefs relays add`.
- Every event is sent to every relay (the full cross product); the report
  (`attempted`, `succeeded`, `failed`, per-`(event, relay)` `results`) is
  printed as structured JSON on stdout under `--json`, or as the same
  per-`(event, relay)` outcomes and summary as plain text on stdout
  otherwise -- never log lines; stderr stays reserved for narration and the
  final error, if any.
- Exits non-zero if **any** `(event, relay)` pair fails -- same
  composes-into-CI/scripts convention as `miner check`.

End-to-end, from a plain note to a published event:

```sh
ncli miner mine --content "hello from ncli" --identity mykey -d 20 -o signed.json
ncli publish -e signed.json -s wss://relay.damus.io
```

If the mined event came out unsigned (no `--identity`, or a pubkey-only
one), `ncli id sign` is the general-purpose sign step -- see
`skills/ncli-identity/SKILL.md` -- and its output matches this same
`-e/--events` single-or-array shape, so it drops straight into `publish`/
`miner check` with no reshaping:

```sh
ncli miner mine -e draft.json -o mined.json -d 20        # PoW only, no --identity
ncli id sign --identity mykey -e mined.json -o signed.json
ncli publish -e signed.json -s wss://relay.damus.io
```

## Check

Two ways to source events to check:

**From a file** (a single event object or a JSON array -- e.g. one produced
by `ncli miner mine` or `ncli dump`):

```sh
ncli miner check -e events.json
```

`-e/--events` must be `.json`/`.jsonp` -- YAML is not accepted here, unlike
`mine`'s `-e`.

**Live, fetched from relays/local stores** -- no `ncli dump` step needed.
Same two-ways-mutually-exclusive targets+filters UX as `find`/`dump` (see
`skills/ncli-query/SKILL.md`): a `--targets targets.yaml` file that may
declare both `relays:` and `filters:`, or `--relays`/inline filter flags:

```sh
ncli miner check --targets targets.yaml
# or, with no file at all:
ncli miner check --relays wss://relay.damus.io --kinds 1 --since 7d
ncli miner check -s wss://relay.damus.io -k 1 --since 7d   # same, short forms (-s/-k = --relays/--kinds)
# --targets/--relays both omitted: falls back to `ncli prefs relays add`
ncli miner check --kinds 1 --since 7d
```

`--identity <vault-label|...>` (same resolution as `mine`'s, including
nip-05) ANDs an `authors` constraint into the filter, narrowing to one
identity's own events -- e.g. auditing "does everything I've published
clear my relay's minimum PoW difficulty":

```sh
ncli miner check --relays wss://relay.damus.io --identity mykey --kinds 1 --json
```

Live mode fetches from **every** target and merges/dedupes by event ID
(the full matching set across all relays/stores), not just the first one
that responds. An unreachable target is logged and skipped, not fatal --
unless *every* target fails, in which case live mode fails with a
`code: "network"` error (`retryable: true`) instead of silently reporting
a false "0 checked."

`--targets` is mutually exclusive with `--relays` and every inline filter
flag (`-k/--kinds`, `-a/--authors`, `-i/--ids`, `-l/--limit`, plus
long-only `--since`/`--until`/`--search`/`--tag`) -- a `--targets` file may
declare its own relays/
filters, so combining it with either is a usage error. File mode
(`-e/--events`) and live mode (`--targets`/`--relays`/inline flags/
`--identity`) are also mutually exclusive with each other.

`--json` prints a structured summary on stdout instead of the same
per-event verdicts and summary as plain text on stdout:

```json
{"checked": 3, "valid": 2, "invalid": 1, "results": [...]}
```

## Exit codes

Bare `ncli miner` (no `mine`/`check`) is a `code: "usage"` error (exit 2),
not a silent help dump with exit 0.

`ncli miner check` exits **non-zero if any checked event fails** PoW
verification -- safe to use directly in a script or CI/cron check:

```sh
ncli miner check -e events.json || alert-oncall "PoW compliance regression"
```

Under `--json`, this specific failure -- some events genuinely failed
verification, as opposed to a malformed input or a network problem -- is
`code: "internal"`, exit 1, `retryable: false` (it won't change without
re-mining the failing events). The full per-event detail (which IDs
failed and why) is already in the `results` array printed to stdout just
before this error, not duplicated into the error object itself.

`ncli publish` exits non-zero the same way if any `(event, relay)` pair
fails, with the per-pair detail in its own `results` array.

(Older versions of `miner check` always exited 0 regardless of outcome --
if you're scripting against an ncli binary, confirm it's recent enough to
have this fix.)
