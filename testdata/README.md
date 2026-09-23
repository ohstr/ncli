# testdata

Fixtures for the Go tests.

- `cases_*.yaml` -- spec-loading cases, see `client/spec_loading_test.go`.
- `events.json` -- 100 real, signed kind:1 events, so a test needing a
  realistic event set doesn't have to reach a relay.

## events.json

Dumped from a public relay on 2026-09-23:

```sh
ncli dump -s wss://nos.lol --kinds 1 --limit 100 -o testdata/events.json
```

Kept verbatim as `ncli dump` wrote it, so the command above reproduces the
same shape. Real events from real authors: every `id` hashes to its own
content and every `sig` verifies, which
`client/fixtures_test.go`'s `TestFixtureEventsAreVerifiable` re-checks on
every `just test`.

Don't hand-edit -- changing any field breaks that event's id and signature.
Regenerate with the command above instead, and expect different events:
the filter is "most recent", not a fixed set.
