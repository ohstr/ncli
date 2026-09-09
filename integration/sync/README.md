# Sync e2e integration test stack

A local Docker Compose stack of **one real `ncli relay` server process**,
used to test `ncli apply -f sync.yaml`'s negentropy two-way reconciliation
end-to-end without touching production. Replaces this repo's only prior
sync coverage, `client/neg_sync_test.go`'s `TestNegSync_Integration`, which
depends on live `wss://relay.ohstr.com` having negentropy enabled and one
specific, hand-picked stable pubkey -- fragile and non-deterministic (that
test isn't deleted; it's still a useful "does this interop with a
real-world deployed relay" smoke test, just not one this repo can gate a
regression on with any determinism). See `integration/stream/README.md`
for the fuller rationale behind this whole family of test stacks.

Unlike `stream`/`inspect`, sync is intentionally a **two-endpoint** flow,
never multi-relay (`SyncSpec.UnmarshalJSON` rejects more than one local
store or one remote relay) -- so this stack has just one relay service, not
several.

## What's here

- `compose.yaml` -- one `remote` service (port `45520`), a real `ncli
  relay` built from this repo's own `build/relay/Dockerfile`.
  Project-named `ncli-sync-itest` so it never collides with the other
  stacks' or `build/relay/docker-compose.dev.yaml`'s.
- `relay.yaml` -- minimal relay config for that one service.
- `sync.yaml` -- a sync spec fixture pointed at this stack's port,
  `direction: both`. Same schema as `examples/apply/sync.yaml`.

## Automated: the Go test

```
just test-integration-sync
```

This runs `TestSyncDocker` (`client/sync_docker_test.go`), which brings the
stack up itself, drives `client.NewSyncModule`/`(*SyncModule).Run` **in
process** against the compose stack's published port, and tears the stack
down when done. Not run by `just test`/`test-integration` or in CI (skipped
under `-short`, same convention as this family's other docker tests) -- it
needs Docker.

The one scenario, `BothDirectionsReconcile`, seeds each side with events
the other side doesn't have (three published straight to the remote relay,
three inserted straight into a fresh local store) and asserts a single
`direction: both` sync run carries every one of them the right way: the
local-only events get pushed up and are independently confirmed present on
the remote relay by querying it directly over the wire; the remote-only
events get pulled down and are independently confirmed present in the
local store by reopening it fresh once the sync run reports completion.

## Known ncli limitation surfaced while building this

Same as `integration/inspect/`: `client.Client.init()` currently refuses to
run `kind: sync` headlessly at all (`ncli apply -f sync.yaml` from a
script/CI/agent with no tty fails immediately). This test constructs
`*SyncModule` directly instead of going through `Client`/`ncli apply` for
exactly that reason -- see `integration/README.md`'s backlog.

## Manual: poke at it with the real CLI

```
just sync up
ncli apply -f integration/sync/sync.yaml
# ... needs a real terminal, per the limitation above ...
just sync down
```

`just sync up` rebuilds the image from your current checkout each time
(`--build`). A manual run writes its local store to
`integration/sync/.data/sync.db` (gitignored) -- the automated Go test uses
its own `t.TempDir()` instead.
