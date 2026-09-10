# Stream e2e integration test stack

Real `ncli relay` containers (1 destination + 3 sources), testing
`ncli apply -f stream.yaml` end-to-end. Built to reproduce and
regression-test a destination silently dropping events during its own
reconnect window (`client/stream.go`'s `deliverToSubscriber`).

## What's here

- `compose.yaml` -- `destination` (45500) + `source1-3` (45501-45503),
  plus `destination2` (45505, used only by
  `MultipleDestinationsBothReceiveEvents`). Project `ncli-stream-itest`.
- `relay.yaml` -- shared minimal relay config.
- `stream.yaml` -- spec fixture, usable by the test and by hand.

## Automated: the Go test

```
just test-integration-stream
```

Runs `TestStreamIntegration`. Needs Docker; runs automatically in CI's
`integrations` job. The client runs in-process (not as a compose service)
so the test can observe `FlowContext.paused()` directly.

Four scenarios:

- **`DestinationDisruptionDoesNotDropEvents`** -- `/Restart` (explicit
  disconnect) and `/Stall` (`docker compose pause`, a silent stall
  detected via ping/pong timeout). Events published into the observed
  `paused()` window must never be silently lost.
- **`SourceReconnectDoesNotHang`** -- restarting a source must not hang or
  drop the stream.
- **`HighVolumeBurstAcrossAllSourcesIsNotLost`** -- hundreds of events
  across all 3 sources at once, e2e coverage for
  [PR #45](https://github.com/ohstr/ncli/pull/45)'s write-concurrency cap.
- **`MultipleDestinationsBothReceiveEvents`** -- fan-out to 2 destinations.

## Manual: poke at it with the real CLI

```
just stream up
ncli apply -f integration/stream/stream.yaml
just stream down
```

`raw: true` keeps this non-interactive. `just stream up --build` picks up
local source changes.

## Stress stack

The stack above proves the fan-in mechanism generalizes past one source;
it doesn't stress production's real ~55-source scale. A separate, heavier
stack does:

- `stress-compose.yaml` -- 20 sources (45561-45580) + 1 destination
  (45560), project `ncli-stream-stress-itest`. Uses a YAML anchor
  (`x-relay: &relay`) to avoid repeating the service block 20 times.
- `stress-stream.yaml` -- two filter objects (`kinds: [1]`, and
  `kinds: [7]` scoped to one author) instead of a catch-all, so filter
  correctness can be tested under load. See the file's header for the
  exact inclusion/exclusion matrix. Only `source1` is listed in `from`
  (valid standalone spec); the Go test overrides it with all 20 URLs.

```
just test-integration-stream-stress
```

Runs `TestStreamStress`. **Not run in CI** (heavier/slower) -- run before
a release or when touching stream's fan-in/concurrency/recovery paths.
`just stream-stress up`/`down` for manual poking.

Five scenarios:

- **`ManySourcesHighVolumeIsNotLost`** -- 1000 events (50/source) across
  all 20 sources at once.
- **`FilterCorrectnessUnderLoad`** -- every source publishes the same
  4-event mix; asserts matching events arrive and non-matching ones never
  do (checking exclusion, not just inclusion).
- **`SustainedLoadOverTime`** -- every source publishes continuously for
  10s instead of one burst.
- **`ConcurrentMultiSourceDisruption`** -- 5 of 20 sources restarted
  simultaneously, matching production's real flakiness.
- **`DestinationStallUnderHighSustainedLoad`** -- all 20 sources publish
  while the destination is paused, stressing recovery's capacity for a
  large simultaneous failed-delivery burst.
