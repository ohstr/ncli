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
  own `build/relay/Dockerfile`. Project-named `ncli-stream-itest` so it
  never collides with `build/relay/docker-compose.dev.yaml`'s stack.
- `relay.yaml` -- minimal relay config shared read-only by all four
  services (each has its own volume, so the identical in-container paths
  never collide).
- `stream.yaml` -- a stream spec fixture pointed at this stack's ports.
  Same schema as `examples/apply/stream.yaml`; usable both by the
  automated test and by hand with the real CLI.

## Automated: the Go test

```
just test-integration-stream
```

This runs `TestStreamDocker` (`client/stream_docker_test.go`), which brings
the stack up itself, drives `client.NewStream`/`(*Stream).Sync` **in
process** (the same call `ncli apply` itself makes) against the compose
stack's published ports, and tears the stack down when done. Not part of
`just test`/`test-integration` (skipped under `-short`, same convention as
`client/multi_relay_test.go`), but runs automatically in CI on every
push/PR via its own `integration-docker` job
(`.github/workflows/ci.yml`) -- it needs Docker, which that job's
`ubuntu-latest` runner already has.

The client runs in-process rather than as a `ncli apply` subprocess/compose
service specifically so the test can observe `FlowContext.paused()`
directly and inject events into the exact reconnect-drop window
deterministically, instead of guessing from timing or log output.

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
