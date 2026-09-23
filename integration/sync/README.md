# Sync e2e integration test stack

Two real `ncli relay` containers -- the same relay config run twice --
testing `ncli apply -f sync.yaml`'s negentropy two-way reconciliation
end-to-end. Replaces the only prior negentropy coverage,
`client/neg_sync_test.go`'s `TestNegSync_Integration`, which depended on a
live public relay and has been deleted: once that relay stopped serving
websockets the test had no way to tell an outage apart from a regression.

Sync itself is a **two-endpoint** flow, never multi-relay
(`SyncSpec.UnmarshalJSON` rejects more than one local store or remote
relay). The second container isn't a second endpoint for one sync -- it's
the destination of a *second* sync run, which is how
`NegentropyPropagatesBetweenRelayInstances` gets events from one relay to
the other without the two ever talking directly. Relays only answer
NEG-OPEN; they never initiate reconciliation with each other.

## What's here

- `compose.yaml` -- `remote` (45520) and `remote2` (45521, empty at
  startup), project `ncli-sync-itest`.
- `relay.yaml` -- minimal relay config.
- `sync.yaml` -- spec fixture, `direction: both`.

## Automated: the Go test

```
just test-integration-sync
```

Runs `TestSyncIntegration`. Needs Docker; runs automatically in CI.

Five scenarios:

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
- **`NegentropyPropagatesBetweenRelayInstances`** -- seeds `remote` with a
  known set, reconciles it down into a temp local store, then pushes that
  store up into the empty `remote2`, asserting every seeded ID arrives.
  Checks `remote2` is empty first, so the assertion can't pass on
  pre-existing data. This is the hermetic replacement for the deleted
  live-relay negentropy test.

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
