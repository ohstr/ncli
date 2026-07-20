# Contributing

Thanks for your interest in improving `ncli`.

## Getting set up

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

`just test`/`just check` skip two integration tests (`TestMultiRelaySync`,
`TestNegSync_Integration`) that hit live public Nostr relays instead of
using mocks. Run them explicitly with `just test-integration` when working on
relay sync or negentropy code; they're excluded from `just check` and CI
because their outcome depends on third-party relay availability.
`TestMultiRelaySync` in particular can still fail against live relays even
when connectivity is fine, since the public firehose it samples sometimes
includes spam events with dishonest NIP-13 nonce tags that get correctly
rejected — that's not a code bug.

## Reporting issues

Please include the `ncli version` output, your OS, and steps to reproduce.
