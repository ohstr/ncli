# Stream e2e integration test stack

A local Docker Compose stack of **real `ncli relay` server processes**
(one destination + three sources), used to test `ncli apply -f
stream.yaml`'s stream feature end-to-end without touching a production
relay. Built to reproduce and regression-test a specific bug: a
destination silently dropping events during its own reconnect window (see
`client/stream.go`'s `deliverToSubscriber`) -- something that needs a real
relay on the other end of a real websocket connection, not a mock, to
observe honestly.

## What's here

- `compose.yaml` -- `destination` (port `45500`) + `source1`/`source2`/`source3`
  (ports `45501`-`45503`), each a real `ncli relay` built from this repo's
  own `build/relay/Dockerfile`, plus `destination2` (port `45505`), used
  only by the `MultipleDestinationsBothReceiveEvents` scenario below.
  Project-named `ncli-stream-itest` so it never collides with
  `build/relay/docker-compose.dev.yaml`'s stack.
- `relay.yaml` -- minimal relay config shared read-only by every service
  (each has its own volume, so the identical in-container paths never
  collide).
- `stream.yaml` -- a stream spec fixture pointed at this stack's ports.
  Same schema as `examples/apply/stream.yaml`; usable both by the
  automated test and by hand with the real CLI.

## Automated: the Go test

```
just test-integration-stream
```

This runs `TestStreamIntegration` (`client/stream_integration_test.go`), which brings
the stack up itself, drives `client.NewStream`/`(*Stream).Sync` **in
process** (the same call `ncli apply` itself makes) against the compose
stack's published ports, and tears the stack down when done. Not part of
`just test`/`test-integration` (skipped under `-short`, same convention as
`client/multi_relay_test.go`), but runs automatically in CI on every
push/PR via its own `integrations` job
(`.github/workflows/ci.yml`) -- it needs Docker, which that job's
`ubuntu-latest` runner already has.

The client runs in-process rather than as a `ncli apply` subprocess/compose
service specifically so the test can observe `FlowContext.paused()`
directly and inject events into the exact reconnect-drop window
deterministically, instead of guessing from timing or log output.

Four top-level scenarios, table-driven where a scenario has more than one
natural case:

- **`DestinationDisruptionDoesNotDropEvents`** -- table-driven across the
  two ways a destination can become unavailable mid-stream:
  - `/Restart` -- the original regression test: forces several real
    destination reconnects (`docker compose restart`) and confirms events
    published into the observed `paused()` window are never silently
    lost.
  - `/Stall` -- `docker compose pause` freezes the destination process
    without touching its TCP connection, a *silent* stall distinct from
    `restart`'s abrupt teardown. Confirms `stream.yaml`'s configured
    ping/pong timeouts actually detect it (a previously-untested code
    path) instead of hanging forever.
- **`SourceReconnectDoesNotHang`** -- restarting a source mid-stream must
  not hang or drop the stream.
- **`HighVolumeBurstAcrossAllSourcesIsNotLost`** -- hundreds of events
  across all 3 sources at once, the e2e regression coverage
  [PR #45](https://github.com/ohstr/ncli/pull/45)'s write-concurrency-cap
  fix never got (only unit-level coverage existed before this).
- **`MultipleDestinationsBothReceiveEvents`** -- every other scenario here
  uses exactly one destination; this adds a second real one and confirms
  `broadcastEvents`' fan-out actually reaches both.

## Manual: poke at it with the real CLI

```
just stream up
ncli apply -f integration/stream/stream.yaml
# ... watch it forward events between the local containers ...
just stream down
```

`raw: true` in `stream.yaml` keeps this non-interactive (plain stdout
logging, no TUI) even run by hand -- safe under a script or CI shell with
no tty, and consistent with how the automated test never touches the TUI
either.

`just stream up` rebuilds the image from your current checkout each time
(`--build`), so local source changes (including to `nmilat` if you're
using the workspace's local checkout) are picked up without an extra step.

## Stress stack

The stack above (3 sources) proves the fan-in *mechanism* generalizes past
a single source; it was never meant to stress production's actual scale.
Bug 2 (this branch's first commit) came from a real ~55-source -> 1
destination topology, so a separate, heavier stack exists for that:

- `stress-compose.yaml` -- 20 real source relays (ports `45561`-`45580`) +
  1 destination (port `45560`), same `build/relay/Dockerfile` image,
  project-named `ncli-stream-stress-itest` (distinct from `compose.yaml`'s
  `ncli-stream-itest`, so both can run at once without colliding). Uses a
  `x-relay: &relay` YAML anchor + `<<: *relay` merge per service to avoid
  20 repeats of the identical build/image/command/restart block --
  independently confirmed the merge actually resolves correctly (`docker
  compose config`) and that all 21 containers build/start/respond
  correctly before relying on it.
- `stress-stream.yaml` -- pointed at the stress stack, with **two filter
  objects** (`kinds: [1]`, and `kinds: [7]` scoped to one author) instead
  of `stream.yaml`'s single catch-all `since: 0` -- see the file's own
  header for the exact inclusion/exclusion matrix
  `client/stream_stress_test.go`'s filter-correctness scenario asserts
  against. The checked-in file lists only `source1` in `from` (so it stays
  a valid, hand-runnable spec); the Go test overrides `from` with all 20
  URLs.

```
just test-integration-stream-stress
```

Runs `TestStreamStress` (`client/stream_stress_test.go`). Not part of
`just test`/`test-integration`/`test-integrations`, and **not run
automatically in CI** -- heavier and slower than the correctness suite
(21 containers vs. up to 5), so run it explicitly before a release or when
touching stream's fan-in/concurrency/recovery paths, via `just
test-integration-stream-stress` or `just stream-stress up`/`down` to poke
at it by hand.

Five scenarios:

- **`ManySourcesHighVolumeIsNotLost`** -- the direct "bug 2 at real scale"
  case: 1000 events (50 per source) across all 20 sources concurrently,
  asserting zero loss where the correctness suite's 3-source version only
  proves the mechanism, not the scale.
- **`FilterCorrectnessUnderLoad`** -- every source publishes the same
  4-event inclusion/exclusion mix (see `stress-stream.yaml`'s header) at
  once; asserts every matching event arrives *and* every non-matching one
  never does. Checking exclusion under real multi-source load is new --
  nothing else in this package proves a filter actually excludes anything,
  only that matching events get through.
- **`SustainedLoadOverTime`** -- every source publishes continuously for
  10s instead of one instantaneous burst, covering throughput/pacing
  behavior a single-shot burst can't.
- **`ConcurrentMultiSourceDisruption`** -- restarts 5 of the 20 sources
  *simultaneously* mid-stream, matching production's real flakiness shape
  (several of ~55 sources flapping at once, not one at a time).
- **`DestinationStallUnderHighSustainedLoad`** -- stream_integration_test.go's
  `DestinationDisruptionDoesNotDropEvents/Stall`, stress-tested: all 20
  sources publish concurrently *while* the destination is paused, instead
  of one source publishing one event, exercising the recovery store's
  capacity to absorb a genuinely large simultaneous failed-delivery burst.
