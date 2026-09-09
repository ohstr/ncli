# Inspect e2e integration test stack

A local Docker Compose stack of **three real `ncli relay` server
processes**, used to test `ncli apply -f inspect.yaml`'s read-only,
multi-target flow end-to-end without touching a production relay. Built to
close a real coverage gap, not to chase a specific bug: before this,
`kind: inspect` had zero test coverage against a real relay at all --
`client/inspect_store_test.go` only ever exercises the local session store
in isolation, never a live relay connection. This mirrors
`integration/stream/`'s pattern (see that directory's README for the full
rationale) applied to inspect's own "several relays feeding one place"
shape: instead of many sources fanning into one destination, several
targets fan into one local session store.

## What's here

- `compose.yaml` -- `target1`/`target2`/`target3` (ports `45510`-`45512`),
  each a real `ncli relay` built from this repo's own
  `build/relay/Dockerfile`. Project-named `ncli-inspect-itest` so it never
  collides with `build/relay/docker-compose.dev.yaml`'s stack or
  `integration/stream/`'s (distinct host ports too).
- `relay.yaml` -- minimal relay config shared read-only by all three
  services (each has its own volume, so the identical in-container paths
  never collide).
- `inspect.yaml` -- an inspect spec fixture pointed at this stack's ports.
  Same schema as `examples/apply/inspect.yaml`.

## Automated: the Go test

```
just test-integration-inspect
```

This runs `TestInspectIntegration` (`client/inspect_integration_test.go`), which
brings the stack up itself, drives `client.NewInspector` **in process**
against the compose stack's published ports, and tears the stack down when
done. Not part of `just test`/`test-integration` (skipped under `-short`,
same convention as `client/multi_relay_test.go` and
`client/stream_integration_test.go`), but runs automatically in CI on every
push/PR via its own `integrations` job
(`.github/workflows/ci.yml`) -- it needs Docker, which that job's
`ubuntu-latest` runner already has.

Three top-level scenarios, table-driven where a scenario has more than one
natural case:

- **`CollectsFromAllTargets`** -- table-driven across data volume, the
  "all relays as source" case: each of the three targets holds events none
  of the others do, and a single inspect session pointed at all three must
  end up with every one of them in its local session store, not just some.
  - `/Small` -- a handful of events per target.
  - `/Large` -- the "high input" case: hundreds of events per target at
    once.
- **`TargetDisruptionDoesNotMissEvents`** -- table-driven across the same
  two disruption mechanisms as stream's
  `DestinationDisruptionDoesNotDropEvents`: the disruption must not hang
  the session, and an event published to that target once it's back must
  still be picked up. Inspect's targets run through the exact same
  `ClientSubscriptionContext.Run`/retry machinery stream's sources do (see
  `client/inspect.go`'s `NewInspector`), so this exercises the same
  mechanism on inspect's own code path instead of assuming it carries over.
  - `/Restart` -- an explicit disconnect (`docker compose restart`).
  - `/Stall` -- `docker compose pause`, a *silent* stall distinct from
    `/Restart`'s abrupt teardown. Necessarily slow (~70s): unlike stream,
    `InspectSpec` has no `timeouts:` block at all (see "Known ncli
    limitations" below), so this relies on `relayclient`'s hardcoded 60s
    default `PongTimeout` with no way to configure a shorter one.
- **`DuplicateEventAcrossOverlappingTargetsIsNotDoubleStored`** -- publishes
  one signed event to two targets and confirms the local store ends up
  with exactly one row for it: overlapping relays serving the same event
  is a routine occurrence for a real inspect session, and this exercises
  two *live, concurrent* deliveries of the same ID racing each other, not
  just two sequential same-goroutine `Insert` calls (which
  `client/inspect_store_test.go`'s `TestInspectStoreInsertToleratesDuplicateEvent`
  already covers at the store level alone).

Unlike stream, inspect has no destination, so the specific bug
`integration/stream/`'s harness was built for (a *destination* silently
dropping events during its own reconnect window) has no inspect
counterpart -- there's nothing downstream of inspect to pause. What both
stacks actually share is the fan-in shape and the need for a real relay to
observe reconnect behavior honestly.

## Known ncli limitations surfaced while building this

- `client.Client.init()` (`client/client.go`) currently refuses to run
  `kind: inspect` (and `kind: sync`) headlessly at all: `ncli apply -f
  inspect.yaml` from a script/CI/agent with no tty fails immediately with
  "this workflow's kind requires an interactive terminal ... use a stream
  workflow (with raw: true) for unattended/agent use". Only `stream`
  supports `raw: true`. That's why this test (like
  `client/stream_integration_test.go`) constructs `*Inspector` directly
  instead of going through `Client`/`ncli apply` -- there is currently no
  other way to exercise inspect non-interactively at all. See
  `integration/README.md`'s backlog for the suggested fix (extend `raw:
  true` support to `inspect`/`sync`).
- `InspectSpec` has no `timeouts:` block at all (`client/inspect.go`'s
  `NewInspector` hardcodes `NewStreamChannel(0, nil)`), so every inspect
  target always uses `relayclient`'s hardcoded defaults (30s ping / 60s
  pong / ...) with no way to configure them -- unlike `stream`/`sync`,
  which both support a `timeouts:` block (see
  `client/spec.go`'s `TimeoutSpec`). This is why
  `TargetStallDoesNotHangSession` above has to wait out the full 60s
  default rather than a short configured one.

## Manual: poke at it with the real CLI

```
just inspect up
ncli apply -f integration/inspect/inspect.yaml
# ... needs a real terminal, per the limitation above ...
just inspect down
```

`just inspect up` rebuilds the image from your current checkout each time
(`--build`), so local source changes are picked up without an extra step.
