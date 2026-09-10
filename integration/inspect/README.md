# Inspect e2e integration test stack

Real `ncli relay` containers (3 targets), testing
`ncli apply -f inspect.yaml`'s read-only multi-target flow end-to-end.
Closes a real coverage gap: before this, `kind: inspect` had zero
real-relay coverage (`client/inspect_store_test.go` only exercises the
local store).

## What's here

- `compose.yaml` -- `target1-3` (45510-45512), project `ncli-inspect-itest`.
- `relay.yaml` -- shared minimal relay config.
- `inspect.yaml` -- spec fixture pointed at this stack's ports.

## Automated: the Go test

```
just test-integration-inspect
```

Runs `TestInspectIntegration`. Needs Docker; runs automatically in CI.

Three scenarios:

- **`CollectsFromAllTargets`** -- table-driven `/Small` and `/Large`:
  each target holds events none of the others do; a session pointed at
  all three must collect every one.
- **`TargetDisruptionDoesNotMissEvents`** -- `/Restart` and `/Stall`
  (mirrors stream's `DestinationDisruptionDoesNotDropEvents`). `/Stall`
  is necessarily slow (~70s): `InspectSpec` has no `timeouts:` block, so
  it waits out `relayclient`'s default 60s `PongTimeout`.
- **`DuplicateEventAcrossOverlappingTargetsIsNotDoubleStored`** --
  publishes one event to two targets, asserts exactly one stored row
  (two *live, concurrent* deliveries racing, not just sequential
  same-goroutine `Insert` calls).

Inspect has no destination, so stream's specific reconnect-drop bug has no
counterpart here -- what both stacks share is the fan-in shape.

## Known ncli limitations found while building this

- `client.Client.init()` refuses `kind: inspect`/`sync` headlessly at all
  (no tty → immediate failure). Only `stream` supports `raw: true`. That's
  why this test constructs `*Inspector` directly instead of going through
  `Client`/`ncli apply`. See `integration/README.md`'s backlog.
- `InspectSpec` has no `timeouts:` block at all -- every target uses
  `relayclient`'s hardcoded defaults, unlike stream/sync.

## Manual: poke at it with the real CLI

```
just inspect up
ncli apply -f integration/inspect/inspect.yaml
just inspect down
```

Needs a real terminal, per the limitation above.

## Stress stack

Mirrors `integration/stream/`'s stress stack for real scale:

- `stress-compose.yaml` -- 15 targets (45590-45604), same YAML-anchor
  pattern, project `ncli-inspect-stress-itest`.
- `stress-inspect.yaml` -- the same two-filter-object pair as stream's
  stress fixture. Only `target1` is listed in `targets`; the Go test
  overrides it with all 15 URLs.

```
just test-integration-inspect-stress
```

Runs `TestInspectStress`. **Not run in CI**. `just inspect-stress up`/`down`
for manual poking.

Four scenarios:

- **`ManyTargetsHighVolumeAllLand`** -- hundreds of events across all 15
  targets at once.
- **`FilterCorrectnessUnderLoad`** -- every target publishes the same
  4-event mix; asserts matching events land and non-matching ones never do.
- **`ConcurrentMultiTargetDisruption`** -- 4 of 15 targets restarted
  simultaneously.
- **`ConcurrentMultiTargetStallDoesNotHangSession`** -- 4 targets paused
  simultaneously; costs no more time than pausing one, since all wait out
  the same default concurrently.
