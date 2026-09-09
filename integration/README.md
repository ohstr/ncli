# Integration / e2e testing: architecture and backlog

This is the index for everything under `integration/`, and the design doc
for how ncli's e2e coverage is meant to grow from here. Written after
building `integration/stream/` to fix a real, previously-unknown zero-loss
violation in production's ~55-source fan-in stream (a destination silently
dropping events during its own reconnect window -- see
`integration/stream/README.md` and this branch's first commit), then
extending the same pattern to `integration/inspect/` and
`integration/sync/`. The goal from here is to make this the default way
ncli's harder-to-unit-test behavior gets regression-tested, not a
one-off built for a single bug.

## The four layers of testing that exist in this repo today

It's worth being explicit about these, because they solve different
problems and shouldn't be conflated:

1. **Unit tests with mocks/`httptest`** -- the bulk of `client/*_test.go`,
   `cli/*/*_test.go`. Fast, run in CI on every push (`go test -short -race
   ./...`), no Docker/network needed. Good for logic that doesn't depend on
   real relay-protocol timing.

2. **Hermetic Docker-Compose e2e tests (this directory's subject)** --
   `integration/stream/`, `integration/inspect/`, `integration/sync/`. Real
   `ncli relay` server processes (built from this repo's own
   `build/relay/Dockerfile`) in containers; the client module under test
   (`client.NewStream`, `client.NewInspector`, `client.NewSyncModule`) runs
   **in-process** inside the Go test, not as a compose service, so the test
   gets white-box access to internal state (e.g. `FlowContext.paused()`)
   for deterministic scenario injection instead of guessing from timing.
   Needs Docker; runs automatically in CI on every push/PR
   (`.github/workflows/ci.yml`'s `integration-docker` job) as its own
   job, separate from `check`'s fast unit-test run -- unlike `just
   test-integration` (deliberately excluded from CI: it hits real public
   relays and isn't deterministic), these are fully hermetic, so there's
   no reason not to gate merges on them. Each stack also has its own `just
   test-integration-<feature>` recipe for running just that one locally,
   plus `just test-integration-docker` to run all three exactly as CI
   does. See "Conventions" below for the pattern every stack here
   follows.

3. **Black-box process-level tests** -- `cli/blossom/blackbox_test.go`,
   `cli/ncli/miner_test.go`: build the real `ncli` binary once and shell out
   to it, asserting on actual exit codes/stdout/stderr/argv, against an
   in-process fake HTTP server (`newFakeBlossomServer`) rather than a
   container. Answers "does the CLI surface itself behave correctly" --
   argv parsing, error contract, output formatting -- independent of
   protocol-level timing. Complementary to layer 2, not a replacement: a
   real Blossom/relay server and a fake one exercise different risks (see
   backlog below on eventually running some of these against a real
   server).

4. **`integration/agent-eval/`** -- a structurally distinct layer: black-box
   test of ncli **as consumed by an agent**, against the **published**
   `ghcr.io/ohstr/ncli:latest` image and public docs (not local source), so
   it also catches docs/release drift that layers 1-3 can't see by
   construction. Billed (real Claude Code sessions), exploratory rather
   than a fixed assertion set, judged partly by an LLM. Not a regression
   gate for a specific code change -- a periodic capability/UX audit. Its
   own `followup/issues.md` tracks confirmed bugs and coverage gaps
   separately from this document.

**This document is about layer 2.** It's the layer best suited to "test
real scenarios so we don't create future regressions" for anything that's
fundamentally about real relay-protocol behavior under real network
conditions (reconnects, timing windows, multi-endpoint fan-in/fan-out) --
exactly the shape of bug the stream fix in this branch's first commit was.

## Conventions every stack here follows

Established by `integration/stream/`, followed by `inspect`/`sync`, and
should be followed by anything added next:

- **Directory**: `integration/<feature>/` containing `compose.yaml`,
  `relay.yaml` (or whatever config the service(s) need), a checked-in spec
  fixture (`<feature>.yaml`, same schema `ncli apply` itself reads), and a
  `README.md` explaining what the stack is, port map, and manual vs.
  automated usage.
- **Compose project naming**: `name: ncli-<feature>-itest` in each
  `compose.yaml`, and a distinct host port range per stack (`stream`:
  `45500-45503`, `inspect`: `45510-45512`, `sync`: `45520`) -- so every
  stack can be brought up independently, or all at once, without colliding
  with each other or with `build/relay/docker-compose.dev.yaml`'s fixed
  container names.
- **Real relay images, not mocks**: every service is built from this
  repo's own `build/relay/Dockerfile`. The entire point of this layer is
  that a mock relay can't honestly reproduce a real network-timing race
  (bug 2's exact lesson).
- **In-process client, not a compose service**: the Go test drives the
  client package's own constructor (`NewStream`/`NewInspector`/
  `NewSyncModule`) directly -- the same call `ncli apply` itself makes --
  rather than shelling out to `ncli apply` as a subprocess/compose service.
  This is what makes deterministic scenario injection possible (see next
  point), and matches every existing `client`-package integration test's
  convention (`client/integration_test.go`, `client/multi_relay_test.go`).
- **Force reconnects deterministically, don't guess from timing**: use
  `docker compose restart <service>` to force a real disconnect, then poll
  observable internal state (`FlowContext.paused()` for stream's
  destination; a plain settle-sleep for source/target reconnects, matching
  `testSourceReconnectDoesNotHang`'s existing precedent) rather than
  sleeping a guessed duration and hoping the window was hit.
- **Independent verification, never trust the client's own self-report**:
  after the operation under test, query the *destination* (or local store)
  directly and independently -- `fetchEventIDsFromRelay`/
  `waitForEventsAtRelay` hit the relay over the wire via
  `relayclient.ReadEventsFromRelay`; `waitForEventsInLocalStore` reopens a
  local store fresh -- and assert that agrees with whatever stat the
  client itself reports (e.g. `Lost() == 0`). This is what closes the
  literal gap that motivated this whole layer: "the client believes it
  delivered N events" vs. "the relay actually has them."
- **Shared harness, not copy-paste**: `client/dockerharness_test.go` holds
  every docker-lifecycle/publish/fetch helper generic across features
  (`runDockerCompose`, `newDockerHarnessEvent`, `publishEventToRelay`,
  `publishEventWithRetry`, `fetchEventIDsFromRelay`, `waitForEventsAtRelay`,
  `waitForRelayReady`). Each feature's own `*_docker_test.go` only holds
  what's actually specific to it (spec-loading, feature-specific
  assertions/polling like `waitForEventsInInspectStore`/
  `waitForSyncComplete`). Add to the shared file, don't fork it, unless a
  new need is genuinely feature-specific.
- **Skip-gating**: `if testing.Short() { t.Skip(...) }` + a `docker` PATH
  check at the top of the outer `Test<Feature>Docker` function. This keeps
  every stack out of the `check` job's `go test -short -race ./...` (which
  has no Docker step) with zero extra build-tag machinery, matching
  `client/multi_relay_test.go`/`client/neg_sync_test.go`'s pre-existing
  convention -- CI instead runs them via the separate `integration-docker`
  job's plain (non-`-short`) `go test -run '...Docker'`. Note
  `cli/bunker/daemon_integration_test.go` uses a *different* convention
  (`//go:build integration`, run via `-tags integration`) -- worth
  reconciling onto one convention if/when bunker gets a docker-based stack
  (see backlog), rather than adding a third.
- **Fixed test-only keys**: a single hardcoded private key
  (`dockerHarnessPrivKey` in `client/dockerharness_test.go`) signs every
  synthetic event across every stack; each `relay.yaml` hardcodes the same
  relay identity key. Nothing in this layer relies on distinct identities,
  so there's no reason to generate fresh ones per run.

### Checklist for adding a new feature's stack

1. `integration/<feature>/{compose.yaml,relay.yaml,<feature>.yaml,README.md}`
   -- copy an existing stack as a starting point, pick an unused port range.
2. `client/<feature>_docker_test.go` -- `Test<Feature>Docker`, reusing
   `client/dockerharness_test.go`'s helpers; add new ones there only if
   genuinely generic.
3. `justfile` -- a `test-integration-<feature>` recipe (mirrors the
   existing three), add `Test<Feature>Docker` to `test-integration-docker`'s
   `-run` regex, and a `<feature> cmd="up" *args` recipe for manual poking
   (mirrors `stream`/`inspect`/`sync`).
4. `.github/workflows/ci.yml`'s `integration-docker` job -- add
   `Test<Feature>Docker` to that step's `-run` regex too, so the new stack
   actually fires on every push/PR instead of only running locally.
5. If a manual run writes local state outside `t.TempDir()` (a recovery
   store, a local sync/inspect DB), gitignore it (see `.gitignore`'s
   `/integration/stream/.recovery/` and `/integration/sync/.data/`
   entries).
6. Update this file's backlog table below.

## Backlog: e2e coverage gaps by feature, prioritized

"Real scenario" here means a scenario shaped like an actual reported bug
or a plausible production failure mode, not just a happy-path smoke test
-- matching the standard `integration/stream/` set (see bug 2 in this
branch's first commit).

| Feature | Current e2e coverage | Real-scenario gap | Priority |
|---|---|---|---|
| `apply -f stream.yaml` | `integration/stream/` -- destination reconnect-drop (the bug this layer was built for), source reconnect | Concurrent multiple destinations (only ever tested with one); recovery-store replay surviving a process restart, not just an in-memory `RecoveryManager` (today's test never kills and restarts the *client* process, only the relay containers); the write-concurrency burst bug fixed in PR #45 (`fix/apply-stream-publish-concurrency-cap`) has no docker-level regression test at all, only whatever unit coverage that PR added | High -- these are two more confirmed-real production bug shapes with zero e2e coverage |
| `apply -f inspect.yaml` | `integration/inspect/` -- multi-target fan-in aggregation, target reconnect | Mixed remote+local targets in one session (this stack is relay-only; `examples/apply/inspect.yaml` explicitly mixes both); overlapping relays serving the *same* event (dedup path -- `InspectStore.Insert`'s `ErrEventDuplicated` handling has a unit test but no live-relay-duplication test) | Medium |
| `apply -f sync.yaml` | `integration/sync/` -- two-way reconcile (push local-only, pull remote-only) against a real relay | `direction: up`/`direction: down` in isolation (only `both` is covered); a sync interrupted mid-reconciliation (context cancel/relay restart mid-negentropy-round) and re-run to confirm it still converges; `maxReconcileRounds` actually being hit (large divergent sets) | Medium |
| `ncli relay` admin surface (`stats`/`reindex`/`members`/`invites`/`roles`/`clear`) | None at the docker/real-relay level; `cli/relay/*_test.go` covers config/context/service-lifecycle in isolation | A full realistic lifecycle against one running container: create an invite, redeem it, assign a role, hit an admin endpoint requiring that role, reindex, clear, confirm state after each step via the admin API itself -- this is exactly the surface `integration/agent-eval`'s R3 already exercises via an LLM agent, but with no deterministic Go-level regression gate underneath it | High -- admin auth/authorization bugs are exactly the kind that "looks fine in a unit test with a mocked auth layer" but breaks for real |
| `blossom` (upload/download/list/rm/mirror/servers/report) | `cli/blossom/blackbox_test.go` against an in-process fake server (thorough for CLI-surface correctness, e.g. the mirror BUD-11 `x`-tag bug agent-eval R7 found -- see `integration/agent-eval/followup/issues.md` #3 -- has a regression test there, `TestBlossomMirror_AgainstServerRequiringHashScope`, extending the fake) | A docker-based stack against a **real** reference Blossom server (e.g. `hzrd149/blossom-server`, already used by `integration/agent-eval/compose.yaml`) would catch protocol-conformance gaps a hand-rolled fake can't by construction -- the mirror bug above is a good example of exactly that class of bug, caught by agent-eval's real server, not by the fake that existed at the time | Medium-High |
| `bunker` (NIP-46) | `cli/bunker/daemon_integration_test.go` (`-tags integration`, live `wss://relay.ohstr.com`) + extensive in-process unit coverage | Same fragility class as the old `neg_sync_test.go`: depends on a live public relay's uptime. A docker-based real-relay equivalent, plus a relay-restart-mid-session scenario (does bunker's own websocket layer silently drop a signing request the same shape as bug 2, or does it surface/retry cleanly?) is untested in either direction today | Medium |
| CLI process-level commands (`publish`/`find`/`decode`/`ping`/`dump`/`miner`/`prefs`/`id`/`version`) | Unit tests per-command against mocked/fake relays; `cli/ncli/miner_test.go` builds the real binary | A single "as a real user" docker-based smoke (publish → find → dump → ping → decode round-trip against one real local relay, mirroring `cli/blossom/blackbox_test.go`'s subprocess pattern but with a real relay instead of nothing/mocks) would close the same "does the compiled binary actually work end to end" gap this whole layer closes for stream/inspect/sync | Low -- lower risk surface, and `integration/agent-eval` already exercises most of this against a real (published) relay from the outside |

Also directly relevant, already tracked elsewhere rather than duplicated
here: `integration/agent-eval/followup/issues.md`'s "Known test-coverage
gaps in the rounds themselves" section lists untested CLI surface from that
layer's own perspective (`relay clear`, `miner check -e <file>` file mode,
`blossom report`/`servers remove`/`servers discover`, `ncli id list`,
`ncli dump` against a local `.db`, `version`/`prefs path`/`completion`).
Worth cross-referencing when picking up any of the above, since closing a
gap at this layer sometimes also closes (or clarifies) one there.

## A real ncli limitation, found while building `inspect`/`sync`'s stacks

`client.Client.init()` (`client/client.go`) refuses to run `kind: inspect`
or `kind: sync` headlessly at all: without a real tty, `ncli apply -f
inspect.yaml`/`sync.yaml` fails immediately with "this workflow's kind
requires an interactive terminal ... use a stream workflow (with raw:
true) for unattended/agent use". Only `stream` supports `raw: true`. This
is why `client/inspect_docker_test.go` and `client/sync_docker_test.go`
(like `client/stream_docker_test.go` before them) construct their module
under test directly rather than going through `Client`/`ncli apply` --
there is currently no other way to run either headlessly at all, for a
test, a script, an agent, or CI.

This is very likely also *why* `integration/agent-eval`'s R4 round only
ever exercises `stream` or `sync` (never `inspect`, per that layer's own
tracked gap) -- if `inspect` genuinely cannot run non-interactively, an
agent driving it via `claude -p` has no path to it at all short of the same
`script`-based TTY workaround `agent-eval` already needs for bunker (R6).
Extending `raw: true` (or an equivalent) to `inspect`/`sync` would fix this
at the source, benefiting real users/agents/CI, not just this test layer --
worth a real ncli issue, not just a test-infra workaround.

## Verifying these stacks

Each needs Docker with **host-reachable published ports** (the containers
must be reachable from wherever `go test` itself runs, e.g. `curl
localhost:<port>` must work) -- a normal dev machine or a standard CI
runner satisfies this. Some sandboxed/rootless container environments do
not: their `go test` process runs in a network namespace with no route to
the Docker daemon's own bridge network at all, not even to a container's
raw bridge IP, not just `localhost` forwarding. If `just test-integration-<feature>`
times out in `waitForRelayReady` even though `docker compose ps`/`docker
logs` show the relay container up and listening, check for exactly this
before assuming the test itself is broken -- confirm with `docker run --rm --network
container:<container-name> curlimages/curl:latest -sS <container's
internal port>` (bypasses host-port forwarding entirely, isolating whether
the *relay* is reachable at all vs. whether only the host-forwarding path
is broken).

This isn't hypothetical: it's exactly what happened while building this
doc's own test suite, both independently confirmed (`docker exec`/`docker
run --network container:...` into the running container succeeded; `curl`
from the test-runner shell against both `localhost:<port>` and the
container's raw bridge IP both failed with connection refused/timeout).
The stacks themselves (compose files, relay startup, in-container
reachability) were verified working; the automated Go tests' actual pass/
fail against a live stack was not confirmed end-to-end in that
environment, and should be before relying on them as a merge gate.
