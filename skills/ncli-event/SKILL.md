---
name: ncli-event
description: Fetch, verify and inspect any Nostr event with ncli's event command, whoever signed it and without a vault identity -- verify an event's id and signature (plus author, kind, PoW, expiration, delegation, zap, deletion and cross-relay consistency) from an nevent/note/naddr/hex id/coordinate, a file or stdin; print it raw (get) or as a readable card (show); build and id unsigned events (new, id); spell it as nevent/naddr with relay hints (encode); see which relays hold it (seen); copy it to other relays unchanged (rebroadcast); walk its thread; diff two versions; decrypt NIP-59 gift wraps (unwrap); and check it against nmilat's per-kind rules before publishing (validate). Use when asked whether an event is authentic, where it is, what it says, or to build/sign/publish one in a pipe.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's cli/event/*.go as of writing. This skill is
self-contained by design and won't see repo changes automatically --
update by hand if flags or output shapes change. -->

# ncli event

`ncli event <subcommand>` works on any event: no key and no vault entry is
needed for anything except `unwrap`.

| Subcommand | What it does |
|---|---|
| `verify` | id + signature, plus whatever the event claims; exit 0 only if all valid |
| `get` | the event exactly as a relay served it, for piping |
| `show` | a readable card: kind name, npub, time, links, decoded tags, content, verify badge |
| `id` | compute the id of unsigned events (fills pubkey/created_at) |
| `new` | build an unsigned event from flags |
| `encode` | note1 / nevent1 / naddr1 with relay hints (the inverse of `ncli decode`) |
| `seen` | which relays hold the event, with latency and errors |
| `rebroadcast` | publish signed events, unchanged, to `--to` relays |
| `thread` | parents up to the root, plus direct replies |
| `diff` | compare two events: fields, tags, content lines |
| `unwrap` | decrypt a NIP-59 gift wrap (kind 1059) or seal (13) with your key |
| `validate` | nmilat's structural rule for the event's kind, plus the signature |

## Naming an event

Every subcommand takes the same inputs, and any number of them:

- `nevent1...`, `note1...`, `naddr1...` (a `nostr:` prefix is fine)
- a 64-char hex id
- a `kind:pubkey:d` coordinate (pubkey may be an npub), e.g. `30023:npub1...:my-post`
- event JSON as an argument: `'{"id":...}'`
- `-f/--file <path>` (repeatable), or a positional path to an existing file
- stdin: `-` explicitly, or just pipe with no argument

Input JSON may be one event object, an array, one event per line (NDJSON),
or relay messages `["EVENT", "<sub>", {...}]` as a websocket client prints
them.

## Where events are looked up

In this order, deduplicated:

1. relay hints inside the nevent/naddr (and in `e`/`a` tags, for `thread`)
2. `-s/--relays` (comma-separated; bare hosts get `wss://` first), or, if
   not given, the relays from `ncli prefs relays`
3. the author's NIP-65 write relays (kind 10002), looked up on the above --
   only when the event wasn't found yet and the author is known (nevent
   with author, naddr, coordinate)

Narrow it with `--only-hints`, `--no-hints`, `--no-outbox`; `--offline`
never touches the network (only events given in full are read).
`--timeout` is per relay (default 10s). `--auth-identity` logs in (NIP-42)
when a relay asks -- `bunker+unix:///path.sock`, `bunker://...`, a vault
label or nsec; only the login event is signed. Without it, reads are
anonymous and no vault password is ever asked for.

For an id, the first copy that verifies wins (a relay serving a forged copy
is skipped). For an naddr/coordinate, every relay is asked and the newest
version wins.

## verify

```sh
ncli event verify nevent1...
ncli event verify note1... --author npub1... --kind 1 -s wss://relay.example
ncli event verify -f signed.json --min-pow 20 --offline --json
cat events.ndjson | ncli event verify --offline --json
ncli event verify naddr1... --cross-relays --deleted
```

Always checked: `format`, `id` (hash of the content), `sig` (BIP-340 over
the id), and, when fetched by reference, `reference` (the relay returned
what was asked for: same id, or same kind/author/d for an naddr).

Checked when present on the event: `pow` (NIP-13 nonce), `expiration`
(NIP-40; fails once expired), `delegation` (NIP-26 token and conditions),
`owner_auth` (NIP-OA `auth` tag), `zap` (NIP-57 9734/9735, MUST-level rules).

Requirements: `--author <npub|hex|nprofile|nip-05|label>`, `--kind N`,
`--min-pow N`, `--max-skew 10m` (fail events dated that far in the future).
An nevent's own author/kind are required too.

Network checks: `--deleted` fails if the author published a NIP-09 deletion
(kind 5 `e` tag, or `a` tag for addressable events); `--cross-relays` asks
every relay and fails if any serves a copy that doesn't verify, and reports
relays still holding an older version of an addressable event.

`--json` prints one report per input (an object for one input, an array
for several):

```json
{
  "input": "nevent1...",
  "status": "valid",
  "id": "...", "pubkey": "...", "kind": 1, "created_at": 1790000000,
  "source": "wss://relay.example",
  "checks": [
    {"name": "format", "status": "ok"},
    {"name": "id", "status": "ok"},
    {"name": "sig", "status": "ok"},
    {"name": "reference", "status": "ok"},
    {"name": "author", "status": "ok"},
    {"name": "pow", "status": "skip", "detail": "no nonce tag"}
  ],
  "relays": [{"relay": "wss://relay.example", "found": true, "elapsed_ms": 84}]
}
```

`status` is `valid`, `invalid` or `missing`. Text mode prints the same,
hiding `skip` checks.

Exit codes: 0 all valid; **1 (`internal`) if any event is invalid** -- the
report is on stdout before the error, like `miner check`; otherwise, for an
event that couldn't be found, 4 `not_found`, 6 `network` (no relay
reachable), 7 `auth` (a relay restricted the read).

## get / show / encode

```sh
ncli event get nevent1... | jq .content
ncli event get note1... note1... -s relay.example > events.json   # array
ncli event show naddr1...
ncli event show -f event.json --json      # view + raw event
ncli event encode <hex-id> -s wss://relay.example
ncli event encode -f event.json --hint wss://relay.example --json
```

`get` prints raw events; one reference gives an object, several an array.
Where each was found goes to stderr. `encode` uses `--hint` values, else up
to 3 relays the event was found on; `naddr` is included for replaceable and
addressable kinds.

## seen / rebroadcast

```sh
ncli event seen nevent1... --json          # {"found":2,"asked":3,"relays":[...]}
ncli event seen -f just-published.json -s relay.a.example,relay.b.example
ncli event rebroadcast nevent1... --to wss://my.relay.example
ncli event get naddr1... | ncli event rebroadcast --to relay.a.example,relay.b.example
```

`seen` asks every candidate relay (exit 4 if none holds it).
`rebroadcast` never re-signs; it refuses an event that doesn't verify
(exit 3) and exits non-zero if any relay rejects, with `publish`'s report
shape under `--json`. `--relays` is where events are read; `--to` is where
they go.

## new / id: build, sign, publish in one pipe

```sh
ncli event new --content "hello" --tag t=nostr --pubkey me \
  | ncli id sign -e - -o - --identity me \
  | ncli publish -e - -s wss://relay.example
```

- `new`: `--kind` (default 1), `--content` or `--content-file` (`-` is
  stdin), `--tag key=value`, `--tag-json '["e","<id>","wss://r","reply"]'`
  (any length), `--pubkey` (fills pubkey and id), `--created-at`,
  `--expiration 24h` (NIP-40).
- `id`: reads unsigned events, fills an empty pubkey from `--pubkey`
  (refusing a different one), sets `created_at` 0 to now, computes the id.
  A sig that no longer matches is dropped with a warning. `--id-only`
  prints ids.
- `ncli id sign` and `ncli publish` take `-e -` (stdin); `id sign -o -`
  prints the signed events instead of its summary. A vault label prompts
  for its password unless `NCLI_VAULT_PASSWORD` is set; use an nsec or a
  `--signer bunker+unix://...` socket in unattended pipes.

## thread / diff

```sh
ncli event thread nevent1... --json   # {"ancestors":[...root first],"event":{},"replies":[...]}
ncli event diff old.json naddr1...
```

`thread` follows NIP-10 markers (`reply`, else `root`, else the last
unmarked `e` tag on old events) and NIP-22 parent tags (kind 1111), up to
`--depth` (50); an unfindable parent stops the walk as `missing_parent`.
Replies are kind 1 and 1111 events whose parent is this event, verified,
oldest first, up to `--limit` (100).

`diff` takes exactly two inputs (`-` for stdin) and always exits 0;
`--json` has `same`, `fields`, `tags_removed`, `tags_added` and
`content_diff`.

## unwrap

```sh
ncli event unwrap nevent1... --identity me
ncli find --kinds 1059 --tag p=<my-hex> -s wss://inbox.example \
  | ncli event unwrap --identity me --json
```

Opens with `--identity` (vault label or nsec, or `NCLI_EVENT_IDENTITY`), or
`--signer bunker+unix://...` so the key stays in the signer. Checks the
wrap's and seal's signatures, that the inner event claims the seal's
author, and that its id (if set) is its hash. Output:
`{"wrap_id","seal_id","sender","rumor"}`. A wrap not addressed to you is
`invalid_input` (exit 3).

```sh
# open only alice's seals; others are skipped undecrypted, and the output is a list
ncli find --kinds 1059 --tag p=npub1me... -s wss://inbox.example \
  | ncli event unwrap --signer bunker+unix:///run/signer/agent.sock --from npub1alice... --json
```

`ncli event wrap --to <who>` does the reverse for any unsigned event (seal
with `--signer`, gift-wrap under a one-time key). For chat, use `ncli dm`
(skill `ncli-messaging`).

## react / reply / post / repost / delete

Signed with `--signer` (`bunker+unix://`, `bunker://` or an identity).
Published to `--to`, else to where the target was found plus `--relays`
(or the prefs relays).

```sh
ncli event react nevent1... "👀" --signer me
ncli event reply nevent1... "agreed" --signer me
ncli event post "hello" --hashtag nostr -m npub1bob... --quote note1... --signer me
ncli event repost note1... --signer me
ncli event delete note1... --reason typo --signer me
```

- `react`: kind 7 with `e`, `p`, `k`, plus `a` (addressable) and `h` (group).
- `reply`: kind 1 with NIP-10 markers for a note, kind 9 quoting it for a
  group message, else a NIP-22 comment (kind 1111) keeping the root.
- `delete`: kind 5; refuses events that aren't the signer's.

`--json` prints `{"id","kind","event","report"}`.

## validate

```sh
ncli event validate -f group-metadata.json --offline
```

Runs the rule an nmilat relay applies to the kind (NIP-29 groups and
moderation, NIP-22, NIP-57, NIP-65, NIP-34 git, NIP-43, NIP-53, NIP-71,
NIP-88, NIP-90, Blossom, ...). A kind without a rule reports structure
`skip`. Exit 1 if any event is invalid.
