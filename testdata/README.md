# testdata

Fixtures for the Go tests.

- `cases_*.yaml` -- spec-loading cases, see `client/spec_loading_test.go`.
- `events.json` -- 339 real, signed events across 10 kinds, so a test
  needing a realistic corpus doesn't have to reach a relay.

## events.json

Dumped from `wss://nos.lol` on 2026-09-23, one query per kind:

```sh
ncli dump -s wss://nos.lol --kinds <kind> --limit <n> -o <kind>.json
```

| kind | n | why it's here |
|---|---|---|
| 0 | 40 | metadata -- `ncli profile` |
| 1 | 100 | text notes -- the common case |
| 3 | 5 | contact lists -- following count; ~16KB each, so few |
| 6 | 30 | reposts |
| 7 | 40 | reactions |
| 1984 | 20 | reports |
| 9735 | 24 | zap receipts -- relay zap indexing |
| 10002 | 30 | relay lists (NIP-65) -- profile relay list |
| 10063 | 30 | blossom servers (BUD-03) -- profile blossom list |
| 30023 | 20 | long-form articles -- relay search indexing |

The whole file round-trips through `ncli relay`: publishing all 339 to a
local relay is accepted 339/339 and reads back 339/339, so seeding a relay
from this file loses nothing. Replaceable kinds (0, 3, 10002, 10063) are
unique per author, and 30023 per (author, `d` tag), so nothing collapses on
ingest.

`client/fixtures_test.go` re-verifies every signature and the kind mix on
each `just test`, with no network.

Don't hand-edit -- changing any field breaks that event's id and signature.
Regenerate instead, and expect a different set: the filter is "most
recent", not a fixed one.

### Why only 24 zap receipts

Of 60 sampled kind:9735 events, ncli's relay rejected 36. Two causes, both
in NIP-57 validation:

- an `lnurl` tag holding a lightning address (`name@domain`) instead of
  bech32 LNURL -- common in the wild, so this may be an interop gap worth
  checking rather than bad data;
- bolt11 description-hash mismatches, including receipts whose invoice
  carries no description hash at all.

The fixture keeps only the 24 that store cleanly, so it round-trips. If
that validation is loosened later, regenerate and this count should rise.
