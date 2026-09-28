---
name: local-verify
description: Build ncli from local source and drive it against a real relay to verify a change end-to-end.
---

# Verifying ncli changes

ncli is a CLI (`cmd/ncli`) wrapping subcommands in `cli/ncli/*.go` (dump,
find, apply, miner, relay, ...) backed by `client/*.go` and the local
`nmilat` replace modules. The runtime surface is the terminal: build the
binary and run real subcommands against it.

## Build

```sh
go build -o bin/ncli ./cmd/ncli
```

## Picking a relay to verify against

A public relay is the quickest handle for a read-path check, but don't
assume one is up: `wss://relay.ohstr.com` has returned a Cloudflare 404
(so the websocket upgrade fails with `bad handshake`) while
`wss://nos.lol` and `wss://relay.primal.net` still served events. Probe
before trusting a failure, and prefer a local relay you seed yourself
(see below) for anything you need to be repeatable.

`find`/`dump`/`miner check` all take
targets and filters the same two ways: a `--targets`/`-t` YAML file
declaring both together (see `examples/targets.yaml`), or `--relays`/`-s`
(comma-separated relay URLs/local `.db` paths) plus inline filter flags —
pick one, not a mix.

```yaml
kind: targets
spec:
  relays:
    - wss://relay.ohstr.com
  filters:
    - kinds: [1]
      limit: 2
```

Then drive commands directly, e.g.:

```sh
./bin/ncli find -t targets.yaml -o out.json
./bin/ncli find -i <event-id> -t targets.yaml
./bin/ncli find --kinds 1 --limit 2 -s wss://relay.ohstr.com -o out.json
./bin/ncli dump -s wss://relay.ohstr.com --kinds 1 --limit 2 -o out.json
```

This exercises the real `relayclient.ReadEventsFromRelay` path over the
network — actual evidence, not a mock.

## Local relay + bbolt store (when you need write/local-store coverage)

```sh
just dev relay   # runs `ncli relay --config examples/relay/minimal.yaml`, db: ./data/db/notes.db
```

The checked-in `data/db/notes.db` / `build/.dev/db/*.db` files are often
empty (dev scratch, not fixtures), so don't assume a checked-in `.db` has
data. To seed a relay yourself, sign events and publish them — no public
relay needed:

```sh
./bin/ncli id --save --label seed --json               # needs NCLI_VAULT_PASSWORD
jq -nc '[range(0;5) | {kind:1, content:("seed " + (.|tostring)), created_at:((now|floor) - .), tags:[]}]' > unsigned.json
./bin/ncli id sign -e unsigned.json -o signed.json --identity seed
./bin/ncli publish -e signed.json -s ws://localhost:5500
```

For read-path checks (`dump`/`find` against `.db` files), you can also
sync events down into a local store via `apply` with a `stream` spec.

## Gotchas learned

- `find`, `dump`, and `miner check` share the exact same targets+filters
  resolution (`cli/ncli/query.go:resolveQuery`), including the
  `--targets`-vs-`--relays`/inline-flags mutual exclusion
  (`queryMutualExclusionCheck`). `--targets`'s existence and extension are
  checked eagerly (a missing/wrong-extension file is a cobra usage error)
  before `Run`.
- `find` targets are evaluated in order and it stops at the **first**
  target that returns any matches (see the `break loop` in
  `client.Find`) — it does not aggregate across all targets. `dump`/
  `miner check` fetch from every target and merge/dedupe by event ID
  (`client.DumpFromTargets`/`CheckPOWLive`, both built on the shared
  private `mergeEventsFromTargets`).
- Every relay input (`-s/--relays`, `prefs relays add`, `targets.yaml`,
  `apply` flow entries) accepts a bare host with no `ws(s)://` scheme —
  `client.resolveRelayURL` (spec.go) defaults it to `wss://` and computes a
  `ws://` fallback candidate, which every dial call site tries via
  `connectRelayWithFallback`/`readEventsWithFallback` (client.go) if the
  primary connection fails outright. An explicit scheme skips the fallback
  entirely. Bare input still has to look host-like (a dot, `localhost`, or
  an IP — see `looksLikeRelayHost`) or it's rejected up front instead of
  silently attempted; a path separator always loses the ambiguity to the
  local-store-path interpretation.
- `client.Find`'s stdout contract: always exactly one `json.MarshalIndent`
  call, even on an empty result (`events` gets coerced to `[]*nip01.Event{}`
  first) — verify any future edit to `client.Find` preserves this. A nil
  slice marshals to bare `null`, and skipping the print entirely on empty
  leaves stdout with nothing at all; both look like a silent failure to a
  script/agent parsing stdout as JSON, which is why this is asserted here
  rather than left to be caught by a human eyeballing the output. All
  narration/errors in `Find`/`mergeEventsFromTargets` go through
  `log.Info`/`log.Error` (stderr), never a bare `fmt.Print*` — check for
  that pattern creeping back in if you touch either function.
- That `[]` contract only applies once at least one target was actually
  reachable. `Find` and `mergeEventsFromTargets` (`client/client.go`) both
  track a `reached` bool across their target loop and return
  `client.ErrNoReachableTargets` if every target hit the
  connection-error/deadline-exceeded skip path -- otherwise "every target
  was down" and "genuinely zero matching events" would be indistinguishable
  to a caller, both showing up as `[]`/exit 0. The three CLI call sites
  (`cli/ncli/find.go`, `dump.go`, `miner.go`'s `check`) each check
  `errors.Is(err, client.ErrNoReachableTargets)` and classify it as
  `common.NetworkError` (retryable) rather than the generic
  `common.RuntimeError` bucket. If you touch either function's target
  loop, keep setting `reached = true` on every non-skipped iteration, or
  this silently regresses back to a false-successful empty result.
