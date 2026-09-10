# Integration / e2e testing: architecture and backlog

Index for `integration/` and the design doc for how ncli's e2e coverage
should grow. Built after fixing a real zero-loss bug in production's
~55-source stream fan-in (destination silently dropping events during its
own reconnect window -- see `integration/stream/README.md`), then extended
to `integration/inspect/` and `integration/sync/`.

This layer has already found two real bugs: the stream fan-in bug above,
and sync's `timeouts:` block being silently a no-op (see
`integration/sync/README.md`'s "Bug found and fixed").

## Four testing layers in this repo

1. **Unit tests** (`client/*_test.go`, `cli/*/*_test.go`) -- mocks/`httptest`,
   fast, run in CI on every push.
2. **Hermetic Docker Compose e2e tests** (this doc's subject) --
   `integration/stream/`, `integration/inspect/`, `integration/sync/`. Real
   `ncli relay` containers; the client under test runs in-process for
   white-box scenario injection (e.g. `FlowContext.paused()`). Needs
   Docker; runs in CI as its own `integrations` job, separate from `check`.
   `just test-integration-<feature>` runs one locally, `just
   test-integrations` runs all three.
3. **Black-box process-level tests** (`cli/blossom/blackbox_test.go`,
   `cli/ncli/miner_test.go`) -- build the real binary, shell out, assert on
   exit codes/stdout against an in-process fake server. Tests CLI-surface
   correctness, not protocol timing.
4. **`integration/agent-eval/`** -- an agent driving the *published*
   ncli image/docs. Billed, exploratory, judged partly by an LLM. A
   periodic capability/UX audit, not a regression gate. Its own
   `followup/issues.md` tracks gaps separately.

This doc covers layer 2: real relay-protocol behavior under real network
conditions (reconnects, timing, fan-in/fan-out).

## Conventions

- **Directory**: `integration/<feature>/{compose.yaml,relay.yaml,<feature>.yaml,README.md}`.
- **Table-driven** when scenarios differ only in a few parameters (disruption
  mechanism, data volume). See `testDestinationDisruptionDoesNotDropEvents`
  (`/Restart`, `/Stall`) and `testInspectCollectsFromAllTargets`
  (`/Small`, `/Large`). For a row needing its own teardown: a strict
  `resolve` called once in the main flow, plus a separate best-effort
  `cleanupIfUnresolved` guarded by a `resolved` bool -- reusing `resolve`
  itself in `t.Cleanup` double-runs it on the success path and fails an
  otherwise-passing test. Don't force a table when cases don't share a
  body (see `testSyncMaxReconcileRoundsTooLowSurfacesCleanly` vs.
  `testSyncReconcileCompleteness`).
- **Compose naming**: `name: ncli-<feature>-itest`, a distinct port range
  per stack (stream 45500s, inspect 45510s, sync 45520, stress stacks
  45560s/45590s).
- **Real relay images, not mocks** -- built from `build/relay/Dockerfile`.
- **In-process client, not a compose service** -- same call `ncli apply`
  makes, but white-box.
- **Force disruptions deterministically**: `docker compose restart` for an
  explicit disconnect, `docker compose pause`/`unpause` for a silent stall
  (confirmed this actually freezes the process without touching the TCP
  connection). Poll observable state instead of guessing from timing.
- **Independent verification**: query the relay/store directly
  (`fetchEventIDsFromRelay`, `waitForEventsInLocalStore`), don't trust the
  client's self-report alone.
- **Shared harness**: `client/integrationharness_test.go` holds generic
  helpers; each `*_integration_test.go` holds only what's feature-specific.
- **Skip-gating**: `testing.Short()` + a `docker` PATH check, matching
  `client/multi_relay_test.go`'s convention (not `cli/bunker`'s
  `-tags integration`).
- **Fixed test-only keys**: `integrationPrivKey`/`integrationPrivKeyAlt` in
  the harness sign every event; no reason to generate fresh ones per run.

### Adding a new feature's stack

1. `integration/<feature>/` files, an unused port range.
2. `client/<feature>_integration_test.go` -- `Test<Feature>Integration`,
   reusing the shared harness.
3. `justfile` -- `test-integration-<feature>` recipe, add to
   `test-integrations`'s `-run` regex, a manual `<feature> cmd="up"` recipe.
4. `.github/workflows/ci.yml`'s `integrations` job -- add to its `-run` regex.
5. Gitignore any manual-run state outside `t.TempDir()`.
6. Update the backlog table below.

## Backlog: e2e coverage gaps by feature

| Feature | Coverage | Gap | Priority |
|---|---|---|---|
| `apply -f stream.yaml` | Destination reconnect-drop, source reconnect, destination stall, high-volume burst (PR #45), 2-destination fan-out; stress stack (20 sources): filter correctness, sustained load, concurrent disruption, stall-under-load | Recovery-store replay across a client process restart (not just relay containers); source stall (only restart is covered); still an order of magnitude short of production's ~55 sources | Low |
| `apply -f inspect.yaml` | Multi-target aggregation, target reconnect/stall, high-volume, duplicate dedup; stress stack (15 targets): filter correctness, concurrent disruption/stall | Mixed remote+local targets in one session; no configurable timeout (forces stall tests to eat the full 60s default) | Low |
| `apply -f sync.yaml` | Two-way reconcile, large divergent set, `maxReconcileRounds` degradation, remote stall, single-filter multi-kind/author correctness | `direction: up`/`down` in isolation; **confirmed bug**: 2+ filter objects don't reconcile correctly (NEG-OPEN only sends `Filters[0]`) -- see `integration/sync/README.md`'s "Confirmed gap" | Medium (the filter bug), Low otherwise |
| `ncli relay` admin (`stats`/`reindex`/`members`/`invites`/`roles`/`clear`) | None at the docker/real-relay level | A full lifecycle against one container (invite → redeem → role → admin action → reindex/clear) | High -- auth bugs look fine with a mocked auth layer but break for real |
| `blossom` | `cli/blossom/blackbox_test.go` against a fake server | A docker stack against a real reference server (e.g. `hzrd149/blossom-server`) would catch protocol-conformance gaps a fake can't -- see the mirror BUD-11 bug `integration/agent-eval` already found this way | Medium-High |
| `bunker` (NIP-46) | `cli/bunker/daemon_integration_test.go`, live `wss://relay.ohstr.com` | A docker-based real-relay equivalent, plus a relay-restart-mid-session scenario | Medium |
| CLI commands (`publish`/`find`/`decode`/`ping`/`dump`/`miner`/`prefs`/`id`/`version`) | Unit tests, `cli/ncli/miner_test.go` builds the real binary | A single "as a user" docker smoke (publish → find → dump → ping → decode) | Low -- `integration/agent-eval` covers most of this already |

Also see `integration/agent-eval/followup/issues.md`'s "Known test-coverage
gaps" section for untested CLI surface from that layer's perspective.

## ncli limitation: inspect/sync can't run headlessly

`client.Client.init()` refuses `kind: inspect`/`sync` without a real tty.
Only `stream` supports `raw: true`. That's why the inspect/sync
integration tests construct their module directly instead of going
through `Client`/`ncli apply`. Likely also why `agent-eval`'s R4 round
never exercises `inspect`. Worth a real ncli issue: extend `raw: true` (or
equivalent) to `inspect`/`sync`.

## Verifying these stacks

Needs Docker with host-reachable published ports. Some sandboxed
environments don't have this -- their `go test` process has no route to
Docker's bridge network at all, even to a container's raw bridge IP. If
`waitForRelayReady` times out despite `docker compose ps` showing the
container up, check for exactly this before assuming the test is broken:
`docker run --rm --network container:<name> curlimages/curl:latest -sS
<internal port>` bypasses host-port forwarding entirely.

This happened while building this suite: the stacks themselves (compose,
relay startup, in-container reachability) were verified working, but a
full `go test` pass against a live stack was not confirmed end-to-end in
that environment.
