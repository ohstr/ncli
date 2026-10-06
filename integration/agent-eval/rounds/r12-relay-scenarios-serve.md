# Round R12 -- Relay scenarios: what extra surface gets served

Pull in the `ncli-relay-ops` skill if you don't already have it from an
earlier round. This round covers four more scenarios from its "Scenario
examples" table -- each a config you author yourself from the skill's
description, not a file handed to you. For each one: author the config,
start it as a background process on the port given, confirm its defining
promise actually holds, then stop it before moving to the next.

## 1. Dev-test relay (port 6511)

Author a `dev-test-relay` config (minimal, nothing scenario-specific).
Start it. Publish an event with a kind in the NIP-16 ephemeral range
(20000-29999) and immediately query for it -- it should be there. You do
not need to wait around to confirm it later expires; that's a documented
property, not something this round has time to observe directly.

## 2. Public-search relay (port 6512)

A real Meilisearch is already running on this machine at
`http://localhost:7700` (master key `masterKey`) -- you did not start it,
but it's there for this scenario specifically. Author a `public-search-relay`
config pointing its `cache.search` block at that address, with
`cache.topZapped` also enabled as the scenario describes. Start it.

1. Publish a kind:0 profile with a distinctive, unusual name (something
   you make up that couldn't coincidentally already exist).
2. Use `ncli find` (or whatever the skill says is the right way to search
   profiles) against your relay with a `--search` for that name. It should
   come back. If it doesn't show up within a few seconds, say so plainly
   in your self-report along with exactly what you ran -- don't retry
   silently and omit that it took tuning.

## 3. Community-voice relay (port 6513)

Author a `community-voice-relay` config (`huddle.enabled`, `rtc: true`).
Start it. You don't have a microphone or a browser here, so you can't join
a real call -- but confirm both doors exist: `ncli huddle list` (or
whatever the skill names as the live-room listing) and a plain unauthenticated
probe of both `/huddle/{id}/audio` and `/huddle/{id}/rtc` should behave
like a real endpoint is there (not a 404), even if the probe itself can't
complete a full join.

## 4. App-backend relay (port 6514)

Author an `app-backend-relay` config (`nip86.enabled`, the `httpBridge.query`
bridge enabled). Start it. Confirm both surfaces exist and are
authenticated, not wide open: a request to either with no NIP-98 signature
should be refused, not silently served and not a plain 404 either (404
would mean the endpoint never mounted at all).

Stop every relay process you started before finishing.

Write your self-report to `/report/r12-relay-scenarios-serve.self-report.json`
(schema: `/rounds/_report-schema.json`), one entry per scenario, including
the exact config you authored for each (inline in the report, not just a
path -- the container may not persist it).
