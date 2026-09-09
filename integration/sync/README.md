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

This runs `TestSyncIntegration` (`client/sync_integration_test.go`), which brings the
stack up itself, drives `client.NewSyncModule`/`(*SyncModule).Run` **in
process** against the compose stack's published port, and tears the stack
down when done. Not part of `just test`/`test-integration` (skipped under
`-short`, same convention as this family's other docker tests), but runs
automatically in CI on every push/PR via its own `integrations` job
(`.github/workflows/ci.yml`) -- it needs Docker, which that job's
`ubuntu-latest` runner already has.

Three top-level scenarios, table-driven where a scenario has more than one
natural case:

- **`ReconcileCompleteness`** -- table-driven across data volume: seeds
  each side with events the other side doesn't have and asserts a single
  `direction: both` sync run carries every one of them the right way: the
  local-only events get pushed up and are independently confirmed present
  on the remote relay by querying it directly over the wire; the
  remote-only events get pulled down and are independently confirmed
  present in the local store by reopening it fresh once the sync run
  reports completion.
  - `/Small` -- three events on each side (three published straight to the
    remote relay, three inserted straight into a fresh local store).
  - `/Large` -- the "high input" case: 150 events on each side, forcing
    `sync.yaml`'s `pullBatchSize` (100) into multiple pull batches against
    a real relay.
- **`MaxReconcileRoundsTooLowSurfacesCleanly`** -- forces
  `spec.MaxReconcileRounds` down to 1 against a large divergent set,
  covering `client/neg_sync.go`'s round-cap branch (logs a warning, syncs
  whatever partial have/need sets it collected, still finishes) for the
  first time. Asserts graceful degradation, not an exact round count.
- **`RemoteStallTriggersTimeoutNotHang`** -- `docker compose pause`, a
  silent stall as opposed to an explicit disconnect. Only meaningful
  because `TimeoutSpec.ConnectionConfig` (`client/spec.go`) actually parses
  a configured `timeouts:` block now -- see "Bug found and fixed" below.
  Sync has no reconnect loop of its own, so this confirms a stalled
  connection surfaces a clean error and returns instead of hanging.
- **`FilterCorrectness`** -- proves sync only reconciles events matching
  its filter, in both directions, using a *single* filter object with
  multiple `kinds` (1 and 7) scoped to one author. Deliberately not
  multiple filter objects -- see "Confirmed gap" below for why that
  wouldn't test what it looks like it tests.

## Confirmed gap: multiple filter objects don't behave like NIP-01 OR-across-filters for sync

`client/neg_sync.go`'s `execute()` builds its **local** negentropy item set
from *every* configured filter, OR'd together
(`for i, f := range s.spec.Filters { items, _ := store.QueryNip77Items(ctx, &f.SubscriptionFilter) ... }`),
but sends only `s.spec.Filters[0]` to the remote in the NEG-OPEN packet
("Use the first filter for NEG-OPEN (NIP-77 uses a single filter)", per
that line's own comment). With exactly one filter (this stack's
`sync.yaml` and `FilterCorrectness`'s override) both sides agree and
everything above holds. With **two or more** filter objects, the two sides
would build their negentropy trees over different item sets whenever
anything matches a later filter but not the first -- local's tree includes
it, remote's never does, since remote never even received that filter.
Not turned into a test here: the exact observable failure shape (whether
negentropy still converges to a wrong-but-plausible result, an outright
protocol error, or something else) needs live verification this
environment couldn't give with confidence (see `integration/README.md`'s
"Verifying these stacks"). Worth a real ncli issue and, ideally, a fix
(either honor all filters symmetrically on both sides, or reject/warn on
`len(Filters) > 1` for `kind: sync` instead of silently accepting
configuration NIP-77 can't actually honor) before building a test that
asserts one particular failure mode.

## Bug found and fixed while building this

`SyncModule.execute` (`client/neg_sync.go`) used to build its connection
config as `&relayclient.ConnectionConfig{}` whenever `spec.Timeouts` was
non-nil -- an all-zero struct. `relayclient.NewConnection` backfills any
zero field with its own hardcoded defaults regardless of whether the whole
config is nil or just zero-valued, so a `sync.yaml` with an explicit
`timeouts:` block (this fixture included) had **zero effect**: every sync
connection always used `relayclient`'s own defaults no matter what the
spec said. Fixed by giving `TimeoutSpec` a single `ConnectionConfig` method
that both `stream` and `sync` now share (`client/spec_test.go`'s
`TestTimeoutSpecConnectionConfig` is the regression guard) -- found because
`RemoteStallTriggersTimeoutNotHang` needed a short configured `Pong` to
actually take effect.

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
