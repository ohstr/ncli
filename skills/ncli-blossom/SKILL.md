---
name: ncli-blossom
description: Upload, fetch, and manage content on Blossom media servers with ncli -- upload/download/list/rm/mirror/report blobs, and manage the default server list (add/remove/list/discover), optionally publishing or discovering it as a signed kind:10063 event (BUD-03). Use when storing or fetching media for a Nostr identity, looking up another identity's uploaded content or published servers, scripting a batch upload/mirror, or troubleshooting a rejected upload/download/delete.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's cli/blossom/*.go as of writing. This skill is
self-contained by design and won't see repo changes automatically --
update by hand if flags/behavior change. -->

# ncli blossom

`ncli blossom` is a client for the [Blossom
protocol](https://github.com/hzrd149/blossom) (BUD-01 through BUD-12):
content-addressed blob storage authenticated with a Nostr identity
instead of a login or API key. Every blob is identified by its sha256
hash; every write is authorized by a short-lived, signed token (BUD-11),
not a session or password.

## Servers: where your media lives

Before uploading or fetching anything, you need at least one Blossom
server configured:

```sh
ncli blossom servers add https://blossom.example
ncli blossom servers list
ncli blossom servers remove https://blossom.example
```

This is a separate list from `ncli prefs relays` (Nostr relays) -- it
lives in the same `prefs.yaml` but under its own key, and only `ncli
blossom` commands consult it. Every subcommand below accepts `--server
<url>` (repeatable) to override this list for one invocation without
touching the saved config.

`servers add --publish` (and `remove --publish`) additionally signs and
publishes the *entire* updated list as a kind:10063 event (BUD-03) to
your configured Nostr *relays* (`ncli prefs relays add`, not the Blossom
server list) -- this is how another person's `servers discover` (below)
finds out which servers you use:

```sh
ncli blossom servers add https://blossom.example --identity mykey --publish
```

`servers discover <identifier>` is the read half: it fetches *another*
identity's most recently published kind:10063 event and prints the
servers it declares, optionally merging them into your own list:

```sh
ncli blossom servers discover alice@example.com              # just show them
ncli blossom servers discover alice@example.com --add        # and adopt them locally
```

This only finds something if `alice` has actually run `servers add
--publish` herself -- most Blossom clients don't publish a BUD-03 list
today, so an empty/not-found result doesn't mean she has no media, only
that she hasn't announced where it lives.

## The one rule that makes every other command predictable

- **Write operations** (`upload`, `rm`, `mirror`) fan out to *every*
  configured/given server and report a result per (item, server) pair --
  the same shape `ncli publish` reports for (event, relay). The command
  exits non-zero if *any* pair failed, even if others succeeded.
- **`download`** instead tries the configured servers *in order* and
  stops at the first that answers -- redundancy on read, not a fan-out.
- **`list`** queries one server by default (`--server`, or the first
  configured), or every configured server with `--all` (merged and
  deduped by hash).

## Uploading

```sh
ncli blossom upload photo.jpg --identity mykey
ncli blossom upload *.jpg --identity mykey                    # one auth token per (file, server) pair, not one for the whole batch
ncli blossom upload photo.jpg --identity mykey --optimize      # ask the server to transcode/optimize it (BUD-05)
```

Content-type is sniffed from the file's own bytes (like `file(1)`/net/http's
own detector), not guessed from the extension. `--auth-ttl` (default 2m)
controls how long each signed token stays valid; it doesn't need to
cover the whole batch, only however long one (file, server) request
takes, since a fresh token is signed for each pair.

**If an upload reports an error, the file may already be on the
server.** The SDK can't distinguish "the request itself failed" from
"the bytes were accepted but the server's response was malformed" --
every upload/mirror failure message says so explicitly. Check with
`ncli blossom list` before assuming a retry is safe/necessary.

## Downloading

```sh
ncli blossom download <hash>                        # writes ./<hash>.<ext> if the server reports one
ncli blossom download <hash> -o photo.jpg
ncli blossom download <hash> -o -                    # stream to stdout, nothing else printed
ncli blossom download "blossom:<hash>.jpg"           # BUD-10 URI
ncli blossom download "https://server/<hash>.jpg"    # a full server URL also works
```

Any of a bare hash, a `blossom:` URI, or a hash-shaped URL is accepted
and normalized to a hash before trying your configured servers in
order. Most servers don't require auth for a plain `GET`; pass
`--identity` only if a server gates private blobs.

## Listing

```sh
ncli blossom list --identity mykey                 # your own media
ncli blossom list alice@example.com                # someone else's, by npub/hex/nprofile/nip-05
ncli blossom list --identity mykey --all           # merge across every configured server
ncli blossom list --identity mykey --limit 20 --cursor <c> --since <ts> --until <ts>
```

The target identifier -- positional, or `--identity`'s fallback when
omitted -- is resolved the same way `--identity` is everywhere else in
`ncli` (vault label, nsec, npub, hex, nprofile, nip-05), not sent to the
server literally. `--limit` is a global cap even with `--all` (results
are merged and sorted newest-first before the limit is applied), not a
per-server one.

## Deleting

```sh
ncli blossom rm <hash> --identity mykey --yes
```

`--yes` is required in any non-interactive session (no TTY on stdin) --
`rm` refuses immediately rather than hanging on a prompt nobody can
answer. Interactively, omitting `--yes` prompts for confirmation once,
then fans the delete out to every configured server.

## Mirroring

```sh
ncli blossom mirror https://example.com/some-file.png --identity mykey
```

Asks each configured server to fetch `source-url` itself -- no bytes
pass through `ncli`. Shares the same "may already be on the server
despite a reported error" caveat as `upload` (see above).

## Reporting

```sh
ncli blossom report <hash> --identity mykey --type spam --reason "..."
```

Submits a BUD-09 report directly to one server (self-authenticated by
the report event's own signature, no BUD-11 token involved). `--type` is
a NIP-56-style report type (`spam`, `malware`, `illegal`, `nudity`, ...).

## Using blossom with an AI agent

- `--json` on every subcommand switches success output to structured
  JSON on stdout, same as the rest of `ncli` -- see AGENTS.md for the
  shared error-code table (`invalid_input`, `not_found`, `network`,
  `auth`, ...).
- Nothing here blocks on a prompt an agent can't answer: `rm` requires
  `--yes` non-interactively instead of hanging, and every other command
  either succeeds, fails fast, or needs an explicit flag.
- `download <hash> -o -` composes directly into a pipe -- stdout is
  exactly the blob's bytes, no narration mixed in.
- `upload`'s and `rm`'s JSON report (`{"attempted","succeeded","failed","results"}`)
  is the same shape whether one server or ten are configured, so a
  script doesn't need a single-vs-multi-server special case.

## Gotchas learned

- **`--auth-ttl` only needs to outlive one (file, server) request, not
  the whole batch** -- a fresh token is signed per pair. Don't pass a
  sub-second value in a script expecting to control exact expiry timing:
  BUD-11 timestamps are Unix *seconds*, so anything under ~1s is rounded
  in an unpredictable direction.
- **`servers discover` finding nothing doesn't mean the identity has no
  media** -- it means they haven't published a BUD-03 server list. Most
  Blossom clients don't today. Falling back to asking them directly (or
  guessing a well-known public server and adding it yourself) is
  sometimes the only option.
- **`list`'s `--limit` is a global cap, not per-server**, when combined
  with `--all` -- results from every configured server are merged and
  sorted newest-first before the limit is applied, so you always get the
  N most recent blobs overall, not up to N from each server.
- **A rejected upload/mirror can still have stored the blob** -- see
  "Uploading" above. There's no way from the CLI alone to tell the two
  cases apart yet; check with `list` rather than assuming either outcome.
- **`servers add`/`remove` and Nostr `prefs relays add`/`remove` are two
  separate lists** in the same `prefs.yaml` -- `--publish`/`discover`
  read/write Blossom servers to/from kind:10063 events on your *relays*,
  not the other way around. Forgetting to configure a relay
  (`ncli prefs relays add`) is the most common reason `--publish`/
  `discover` fail with a "no relays configured" error.
