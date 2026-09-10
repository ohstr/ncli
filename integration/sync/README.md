# Sync e2e integration test stack

One real `ncli relay` container, testing `ncli apply -f sync.yaml`'s
negentropy two-way reconciliation end-to-end. Replaces the only prior sync
coverage, `client/neg_sync_test.go`'s `TestNegSync_Integration`, which
depends on a live public relay -- kept, still a useful smoke test, just
not one this repo can gate a regression on with any determinism.

Sync is a **two-endpoint** flow, never multi-relay (`SyncSpec.UnmarshalJSON`
rejects more than one local store or remote relay) -- one relay service,
not several.

## What's here

- `compose.yaml` -- one `remote` service (45520), project `ncli-sync-itest`.
- `relay.yaml` -- minimal relay config.
- `sync.yaml` -- spec fixture, `direction: both`.

## Automated: the Go test

```
just test-integration-sync
```

Runs `TestSyncIntegration`. Needs Docker; runs automatically in CI.

Four scenarios:

- **`ReconcileCompleteness`** -- `/Small` and `/Large`: seeds each side
  with events the other lacks, asserts both directions land (verified
  independently over the wire / by reopening the local store).
- **`MaxReconcileRoundsTooLowSurfacesCleanly`** -- forces
  `MaxReconcileRounds` to 1 against a large divergent set; must degrade
  gracefully, not hang.
- **`RemoteStallTriggersTimeoutNotHang`** -- `docker compose pause` on the
  remote. Only meaningful because of the `TimeoutSpec.ConnectionConfig`
  fix below. Sync has no reconnect loop, so a stall must surface a clean
  error, not hang.
- **`FilterCorrectness`** -- a single filter with 2 kinds + 1 author;
  asserts matching events reconcile and non-matching ones stay excluded.
  Not multiple filter objects -- see "Confirmed gap" below.

## Confirmed gap: multiple filter objects don't work correctly for sync

`neg_sync.go`'s `execute()` builds its **local** negentropy set from every
configured filter OR'd together, but sends only `Filters[0]` to the
remote in NEG-OPEN ("NIP-77 uses a single filter"). With 2+ filters, the
two sides build negentropy trees over different item sets whenever
something matches a later filter but not the first. Not turned into a
test here -- the exact failure shape needs live verification this
environment couldn't give with confidence. Worth a real ncli issue: either
honor all filters symmetrically, or reject `len(Filters) > 1` for sync.

## Bug found and fixed while building this

`SyncModule.execute` built its connection config as an all-zero
`&relayclient.ConnectionConfig{}` whenever `spec.Timeouts` was set --
`relayclient.NewConnection` backfills zero fields with its own defaults
either way, so a configured `timeouts:` block had zero effect. Fixed by
giving `TimeoutSpec` a shared `ConnectionConfig` method (stream already
had this logic correctly; sync didn't). Regression guard:
`client/spec_test.go`'s `TestTimeoutSpecConnectionConfig`.

## Known ncli limitation

Same as inspect: `client.Client.init()` refuses `kind: sync` headlessly.
This test constructs `*SyncModule` directly instead of going through
`Client`/`ncli apply`.

## Manual: poke at it with the real CLI

```
just sync up
ncli apply -f integration/sync/sync.yaml
just sync down
```

Needs a real terminal. A manual run writes to
`integration/sync/.data/sync.db` (gitignored); the Go test uses its own
`t.TempDir()`.
