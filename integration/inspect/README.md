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

This runs `TestInspectDocker` (`client/inspect_docker_test.go`), which
brings the stack up itself, drives `client.NewInspector` **in process**
against the compose stack's published ports, and tears the stack down when
done. Not run by `just test`/`test-integration` or in CI (skipped under
`-short`, same convention as `client/multi_relay_test.go` and
`client/stream_docker_test.go`) -- it needs Docker.

Two scenarios:

- **`CollectsFromAllTargets`** -- the "all relays as source" case: each of
  the three targets holds events none of the others do, and a single
  inspect session pointed at all three must end up with every one of them
  in its local session store, not just some.
- **`TargetReconnectDoesNotMissEvents`** -- restarting a target mid-session
  must not hang the session or cause events published around the restart to
  be missed. Inspect's targets run through the exact same
  `ClientSubscriptionContext.Run`/retry machinery stream's sources do (see
  `client/inspect.go`'s `NewInspector`), so this is the same mechanism
  `client/stream_docker_test.go`'s `SourceReconnectDoesNotHang` covers,
  exercised on inspect's own code path instead of assumed to carry over.

Unlike stream, inspect has no destination, so the specific bug
`integration/stream/`'s harness was built for (a *destination* silently
dropping events during its own reconnect window) has no inspect
counterpart -- there's nothing downstream of inspect to pause. What both
stacks actually share is the fan-in shape and the need for a real relay to
observe reconnect behavior honestly.

## Known ncli limitation surfaced while building this

`client.Client.init()` (`client/client.go`) currently refuses to run
`kind: inspect` (and `kind: sync`) headlessly at all: `ncli apply -f
inspect.yaml` from a script/CI/agent with no tty fails immediately with
"this workflow's kind requires an interactive terminal ... use a stream
workflow (with raw: true) for unattended/agent use". Only `stream` supports
`raw: true`. That's why this test (like `client/stream_docker_test.go`)
constructs `*Inspector` directly instead of going through `Client`/`ncli
apply` -- there is currently no other way to exercise inspect
non-interactively at all. See `integration/README.md`'s backlog for the
suggested fix (extend `raw: true` support to `inspect`/`sync`).

## Manual: poke at it with the real CLI

```
just inspect up
ncli apply -f integration/inspect/inspect.yaml
# ... needs a real terminal, per the limitation above ...
just inspect down
```

`just inspect up` rebuilds the image from your current checkout each time
(`--build`), so local source changes are picked up without an extra step.
