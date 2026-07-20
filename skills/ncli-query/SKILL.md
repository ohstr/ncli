---
name: ncli-query
description: Query and export Nostr events with ncli's dump and find commands, or test relay reachability with ncli ping — look up an event by ID or NIP-01 filter across relays/local event stores, export matches to JSON, or probe whether relays actually connect and answer a subscription. Use when writing a targets.yaml (relays + filters, one file), choosing dump vs find, relying on `ncli prefs` default relays, or running `ncli dump`/`ncli find`/`ncli ping`.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's examples/{filters,targets}.yaml and
cli/ncli/{dump,find,ping,prefs,query}.go behavior as of writing. This
skill is self-contained by design and won't see repo changes
automatically — update by hand if flags/schemas change. -->

# ncli dump / find / ping / prefs

`dump` exports everything matching filters to JSON. `find` looks up a
specific event by ID and/or filter and stops at the **first** target that
returns any match — it never aggregates across all targets. `ping` probes
whether targets are reachable at all, without fetching any events. `miner
check`'s live mode (see `skills/ncli-miner/SKILL.md`) uses this exact same
targets+filters UX as `dump`/`find` too.

`dump` and `find` take their targets and filters **two ways, mutually
exclusive**:

- **`-t/--targets <file.yaml>`** — one YAML file that may declare `relays:`
  and/or `filters:` together (see `targets.yaml` below).
- **`-s/--relays <comma-list>`** plus inline filter flags
  (`-k/--kinds`, `-a/--authors`, `-i/--ids`, `-l/--limit`, plus long-only
  `--since`/`--until`/`--search`/`--tag`) — for a quick one-off query with
  no file at all.

Combining `--targets` with `--relays` or any inline filter flag is a usage
error — pick one. `ping` has a friendlier, and simpler, variant of the
same idea: plain positional arguments instead of `-s/--relays` (`ncli
ping relay.primal.net` needs no flag at all), and no filter flags at all
— see `## ping` below for why.

## `dump`

```sh
ncli dump -s wss://relay.damus.io -o out.json
ncli dump -s wss://relay.damus.io,./data/db/notes.db -o out.json   # comma-separated: merge a relay + a local store
ncli dump -s ./data/db/notes.db -k 1 --since 7d -o out.json        # inline filters against a local store (-k = --kinds)
ncli dump -t targets.yaml -o out.json                              # relays + filters from one file
ncli dump -o out.json                                               # -s/-t omitted: every `ncli prefs` relay, merged
```

`-o/--out` is required (`.json`/`.jsonp`). Results are merged and deduped
by event ID across every target; an unreachable relay is logged and
skipped rather than failing the whole export -- unless *every* target is
unreachable, which is a `network` error instead (see below), not a
silent empty/missing output file.

## `find`

```sh
ncli find <event-id> -t targets.yaml
ncli find <event-id> -s wss://relay.damus.io
ncli find note1... -s wss://relay.damus.io                          # positional also decodes note1.../nevent1... NIP-19 strings
ncli find npub1...                                                  # author-shaped positional: no other filters, so just their profile (kind 0); uses `ncli prefs` relays
ncli find alice@example.com -k 1 -l 5 -s wss://relay.damus.io       # nip-05 too, widened past the kind-0 default and ANDed with -k/-l
ncli find -k 1 -a alice@example.com -s wss://relay.damus.io         # --authors flag form still available (-a short form)
ncli find <event-id>                                                # -t/-s omitted: uses `ncli prefs` relays
```

At least one of a positional identifier, an inline filter flag, or
`-t/--targets` (whose file may itself declare filters) is required. The
identifier is purely positional, resolved by `client.ResolveFindIdentifier`
to exactly one of:
- **event-shaped** (matched by ID, added as its own OR'd filter): a plain
  hex event ID, or a `note1.../nevent1...` NIP-19 string (an `nevent`'s
  embedded relay/author/kind hints are ignored, only its ID is used).
- **author-shaped** (matched by Authors, ANDed into every other filter so
  it composes with `--kinds`/`--limit`/etc. the way you'd expect — "this
  person's kind-1 notes," not "this person's anything, OR any kind-1 note
  from anyone"): `npub1...`, `nprofile1...` (relay hints likewise ignored),
  or a nip-05 `name@domain` address. **With no other filters at all**
  (`ncli find <npub>` alone), this defaults to `kinds: [0]` — just their
  profile — instead of an unbounded fetch of everything they've ever
  published (`cli/ncli/find.go:mergeFindIdentifier`; a live test against
  `wss://relay.damus.io` returned 147 events of every kind before this
  default existed). Pass `--kinds` explicitly to widen it.

There's no `-i`/`--id` flag: `docker`/`kubectl`/`git` never offer a flag
alternative for the thing a command targets, only for modifiers, so `find`
doesn't either — passing more than one positional argument is a usage
error. `--authors` (inline, or inside a `targets.yaml`'s `filters:`) is
still there as a flag form, and also accepts nip-05.

`find`'s stdout is always exactly one JSON array — `[]` when nothing
matches, never bare `null` and never empty output — so it's safe to pipe
straight into `jq` or a script's JSON parser with no empty-result special
case. All narration (`querying <host>`, `no events found`) and errors
(`target unreachable, skipping`) go to stderr instead, on the assumption
that stdout/stderr are captured as separate streams. `-q/--quiet` and
`--json` are global flags (available on `dump`/`miner check` too, not just
`find`): `-q/--quiet` drops the narration (warnings/errors still show)
for callers that can't rely on stdout/stderr separation, e.g. logging
wrappers that merge `2>&1`; `--json` switches every stderr line -- both
routine narration and a partial failure like one unreachable target --
from a colored console line to a single-line JSON object, and a
command-level failure (the whole run erroring out, not just one target)
to a `{"error", "code", "retryable", "input"}` shape (`dump` and `find`
have no JSON *success* mode of their own -- `dump` writes its result to
`-o`, and `find`'s stdout is already unconditionally JSON as above). For
this command group, `code` is typically `invalid_input` (a malformed
`--targets`/`--relays`/`--kinds`/`--since`/`--tag` value, `input` echoing
back the specific bad token), `not_found` (no `--targets`/`--relays` given
and no `ncli prefs` relays configured either), or `network` (a `--authors`
nip-05 lookup failed to resolve, or -- see next paragraph -- every target
unreachable; both `retryable: true`).

**An unreachable target is logged and skipped, not fatal to the whole
query -- unless *every* target fails.** `find`/`dump`/`miner check` all
tolerate a mix of reachable and unreachable targets: the unreachable ones
are logged (`target unreachable, skipping`) and the result reflects
whatever the reachable ones returned, exit 0, even if that's genuinely
empty. But if every single target fails to connect or times out, that's
`code: "network"` (`retryable: true`), not a false `[]`/empty-file
success -- a typo'd hostname or a fully-down relay set is now
distinguishable from "queried successfully, found nothing." Test this
yourself with a bogus host: `ncli find <id> -s relay.invalid.example
--json` should fail with a network error, not print `[]`.

## `ping`

```sh
ncli ping relay.damus.io                                     # no flag needed -- scheme optional too, tries wss:// then falls back to ws://
ncli ping relay.damus.io relay.snort.social                  # space-separated, not comma -- each is its own positional argument
ncli ping -t targets.yaml                                    # relays from a file (its filters, if any, are ignored)
ncli ping                                                    # no relays, no --targets: every `ncli prefs` relay
ncli ping relay.damus.io --json                              # structured report on stdout, no narration
```

Connects to every target and issues a Limit-1, match-everything
subscription -- unlike `find`/`dump`, `ping` doesn't fetch or return any
events, it only proves each relay actually speaks the protocol (a bare
TCP/WebSocket connect isn't enough: a relay behind a misconfigured reverse
proxy can accept the connection and still never answer). Local store paths
that end up in the target list are skipped silently -- there's nothing to
dial.

`ping` has **no filter flags at all** (`-k/--kinds`, `--search`, etc.) --
unlike `find`/`dump`/`miner check`, it never reads a relay's response, so
a filter's content couldn't change the result even if you set one; there
was no honest reason to expose them. Relays are **plain positional
arguments** instead of `-s/--relays` -- there's no query-shaped identifier
competing for that position the way `find`'s event/author positional
does, so the friendlier direct form (`ncli ping relay.primal.net`) is
unambiguous and is the primary UX here. `-t/--targets <file.yaml>` is
still available for a relay list kept in a file (the same shape
`find`/`dump` use, though `ping` only reads its `relays:`, never its
`filters:`), and is mutually exclusive with positional relays -- same
"pick one" rule as `find`/`dump`, just spelled with an argument instead of
a flag.

Unlike `find`/`dump`, one unreachable target here **is** the failure being
tested for, not something to tolerate and route around: `ping` exits
non-zero (`code: "internal"` in `--json` mode) if *any* target failed,
listing exactly which. `--timeout` (default `30s`) bounds how long a
single relay's connect-and-subscribe gets before it's counted
unreachable and `ping` moves to the next one.

Presentation follows the same terminal-awareness `apply` uses: an
interactive board when stdout is a real terminal and neither `--json` nor
`-q/--quiet` is set, plain log lines on stderr otherwise. `--json` prints
a `{results: [{relay, reachable, error?}], checked, reachable,
unreachable}` report to stdout instead, with no narration at all -- the
report is the only thing on stdout, safe to pipe into `jq`.

`apply` itself has no connectivity pre-check of its own (it trusts that
relays named in a spec already work) -- run `ncli ping` first, separately,
if you want that guarantee before an `apply` run.

## `-s/--relays`

Comma-separated entries, each either a relay URL or a path to an existing
local `.db` file — the same two shapes a `targets.yaml` entry accepts. A
single entry works exactly like a single relay/store.

A relay entry's `ws(s)://` scheme is optional: `relay.primal.net` works the
same as `wss://relay.primal.net`, trying `wss://` first and falling back to
`ws://` only if that fails to connect. Writing the scheme explicitly (either
one) is taken at face value with no fallback. An existing local file always
wins the ambiguity over the bare-host relay interpretation, so this doesn't
change the local `.db` path shorthand.

## `targets.yaml`

One file for both "where to look" and "what to match" — used by `-t` on
`dump`, `find`, and `miner check` alike:

```yaml
kind: targets
spec:
  relays:
    - wss://relay.damus.io
    - relay: wss://relay.snort.social
      trusted: true
    - path: ./data/db/notes.db

  # optional — omit entirely to match everything. Same NIP-01 fields as
  # filters-reference.md below; multiple filters are OR'd, fields within
  # one filter are AND'd.
  filters:
    - kinds: [1]
      authors:
        - alice@example.com   # nip-05 addresses resolve to hex automatically
      limit: 10
```

`find` tries `relays` **in order** and stops at the first one with any
match; `dump`/`miner check` fetch from **every** target and merge/dedupe by
event ID.

Full filter field list and the `since`/`until` sign table:
`references/filters-reference.md`.

## `ncli prefs`

```sh
ncli prefs relays add wss://relay.damus.io
ncli prefs relays add relay.primal.net     # scheme optional here too -- see -s/--relays above
ncli prefs relays list
ncli prefs relays remove wss://relay.damus.io
ncli prefs relays clear
ncli prefs path            # print prefs.yaml's location
```

This is the fallback `find`/`dump`/`miner check` use when **both**
`-t/--targets` and `-s/--relays` are omitted — it does **not** apply to
`ncli apply`, which always names relays/paths explicitly in its spec.

Every subcommand above takes `--json` for scripted/agent use: `add`/
`remove` report `{"relay", "added"|"removed": bool}` (`false` means it was
already in/out of the list, not an error), `list` reports `{"relays": [...]}`
(`[]`, never `null`, when the list is empty), `clear` reports
`{"cleared": true}`, and `path` reports `{"path": "..."}`. Bare `ncli prefs`
or `ncli prefs relays` (no further subcommand) is a `usage` error (exit 2),
not a silent help dump.

## Gotchas learned

- `find` stops at the **first** target with any match — it does not
  aggregate results across targets. Order matters: put your most
  authoritative/likely source first.
- `--authors` (inline, or inside a `targets.yaml`'s `filters:`) accepts hex
  pubkeys, hex prefixes, and nip-05 `name@domain` addresses mixed
  together. A nip-05 lookup is a live HTTPS fetch to the domain's
  `.well-known/nostr.json` — it fails the whole command if that lookup
  fails, same as any other invalid filter value.
- All-digit values (`ids`, `authors`, bare numeric tag values) **must** be
  quoted in YAML, or the parser reads them as numbers and unmarshalling
  fails.
- Tag filter keys (`"#e"`, `"#p"`, etc.) **must** be quoted — an unquoted
  leading `#` starts a YAML comment and the field is silently dropped, no
  error.
- `since`/`until` duration-string sign direction is asymmetric and easy to
  get backwards — see the full table in `references/filters-reference.md`
  before writing a relative time window.
- `dump` with `-s`/`-t` omitted merges every prefs relay's results, deduped
  by event ID. `find` with `-s`/`-t` omitted uses prefs relays as the
  target list, but keeps the same first-match-wins behavior — it does not
  become an aggregate search just because multiple relays are configured.
- A schemeless relay entry (`relay.primal.net`) must look host-like — a dot,
  `localhost`, or a bare IP — or it errors instead of being treated as one;
  a plain typo with no dot (`ncli find -s relaydamusio`) is rejected up
  front rather than silently attempted.
- The positional `nevent1...`/`nprofile1...` decoding only uses the
  embedded ID/pubkey — relay hints are **not** added to the search targets,
  so a bare `ncli find nevent1...` with no `-s`/`-t` still only searches
  `ncli prefs` relays, even if the nevent points somewhere else.
- An event-shaped positional (ID/note/nevent) is added as its own filter,
  OR'd with `--kinds`/`--limit`/etc. — harmless in practice since an ID
  already pins one exact event. An author-shaped positional
  (npub/nprofile/nip-05) is different: it's ANDed into every filter
  instead, because "this person's kind-1 notes" (AND) is what a user means
  by `ncli find alice@example.com --kinds 1`, not "this person's anything,
  OR any kind-1 note from anyone" (the OR reading, which is wrong here).
- A bare author-shaped positional with **no other filters and no
  `--targets`** defaults to `kinds: [0]` (just their profile) instead of
  matching every kind with no limit. This only kicks in when there's
  nothing else to AND into — the moment you add `--kinds`/`--limit`/a
  `--targets` filter, the kind-0 default is gone and your filter is what
  runs (ANDed with the author, as above).
- `find`'s empty-result case prints `[]` to stdout (and logs `no events
  found` to stderr) rather than skipping stdout output entirely — an
  agent parsing stdout as JSON never needs a special case for "nothing
  matched" vs. "something went wrong before printing anything." This only
  holds when at least one target was actually reachable, though — if every
  target failed to connect or timed out, `find`/`dump`/`miner check` all
  return a `code: "network"` error instead of printing `[]`/an empty file,
  so "queried and found nothing" stays distinguishable from "never
  actually got to check."
