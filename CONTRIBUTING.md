# Contributing

Thanks for your interest in improving `ncli`.

## Getting set up

Install [`just`](https://github.com/casey/just) (e.g. `cargo install just`,
or via your package manager) — it's the one tool here that isn't part of
the standard Go toolchain. Then:

```sh
git clone https://github.com/ohstr/ncli
cd ncli
just build   # build the binary
just check   # go vet + go test
```

See the [README](README.md) for the full `just` command list.

## Before opening a PR

- Run `just check` and make sure it passes.
- Keep changes focused; unrelated formatting/refactors make review harder.
- Add or update tests for behavior changes.

Every test is hermetic: no test dials a public relay
(`TestNoPublicRelayInTests` enforces it). Streaming and fan-in are covered by
`TestStreamIntegration`'s real relay containers, and negentropy by
`TestSyncIntegration`'s `NegentropyPropagatesBetweenRelayInstances`, which
runs the same relay config twice and moves a known event set from one
instance to the other. `TestMultiRelaySync` used to stream from public
relays; it was removed as redundant with the container-based coverage and
unreliable against a live firehose.

Tests needing a realistic corpus should use `testdata/events.json` (339
signed events across 10 kinds) rather than fetching from a relay — see
`testdata/README.md`.

`integration/agent-eval` is a separate, manual harness: every round is a
real, billed Claude Code session. It is never run by `just check` or CI —
see its README before invoking it.

## Reporting issues

Please include the `ncli version` output, your OS, and steps to reproduce.
