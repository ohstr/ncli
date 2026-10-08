# ncli agent-capability eval

Tests **ncli as consumed by an agent**, not this repo's source. A fresh
Claude Code agent, in a container that's never seen this checkout, is
pointed at `https://ohstr.github.io/ncli/PROMPT.md` and follows it --
same as any real external user/agent picking `ncli` up for the first
time. Unlike `.agents/skills/local-verify` (builds from local source to
check a change), this installs the **published** image/docs, so it catches
docs drifting from what's actually shipped.

## Architecture

`compose.yaml` runs three services:

- **`relay`** -- published `ghcr.io/ohstr/ncli:latest`, admin enabled
  (so R3 has something real to administer).
- **`blossom`** -- reference Blossom server, for R7.
- **`agent`** -- the agent-under-test's machine (`agent/Dockerfile`): no
  Go/`ncli`/repo copy, just curl/bash/Node + Claude Code, plus a
  Meilisearch binary (not always-on -- R12's `prepare_r12` starts it only
  before that round). Runs non-root (Claude Code refuses
  `bypassPermissions` as root) and shares `relay`'s network namespace,
  since ncli's admin commands hard-target `localhost` with no `--url`
  override.

Fifteen rounds (`rounds/r0-bootstrap.md` .. `r14-signer.md`)
cover install, identity, relay queries, relay admin, publish/apply,
PoW mining, NIP-46 bunker signing (started and driven with no TTY),
a local policy signer on a unix socket (R14: policy, socket signing,
a denial, a maintainer approval and its replay),
Blossom, the documented error-code contract, NIP-29 groups, NIP-53
spaces (a headless call plus chat), and (R11-R13) whether an agent working
only from the `ncli-relay-ops` skill's scenario table can stand up each of
the 12 named `examples/relay/` scenarios and get the behavior it promises.
Each round is a fresh, non-interactive `claude -p`
call with no memory of prior rounds, though the container filesystem
persists between them (R0's install, R1's identity, etc. are still
there).

Every claim gets independently re-checked, never trusted as-is:

- **`bin/verify/*.sh`** -- one script per round, querying relay/
  Blossom/agent state directly. This is the pass/fail signal that
  matters.
- **`bin/judge.sh`** -- a separate `claude -p` call scoring *process
  quality* from the transcript. Supplementary, not the verdict.

`bin/report.sh` merges both into `report/<run-id>/report.md`, plus a
one-line summary appended to `report/history.jsonl` for diffing across
releases.

Every verifier also checks the self-report against
`rounds/_report-schema.json` (appended to every prompt). Afterwards
**`bin/coverage.sh`** counts which `ncli` commands the agents actually ran
and appends that table to the report; a full run fails if any command
outside `coverage-allow.txt` was never run.

## Running it

Requires Docker, and this host already logged into Claude Code (run
`claude` once first -- the harness copies that login into the
container).

```sh
cd integration/agent-eval
bin/run.sh                              # every round
bin/run.sh r0-bootstrap r1-identity     # just these, in order given
```

Output lands in `report/<UTC-timestamp>/`.

To run this checkout's ncli instead of the published one (relay and agent
alike; docs still come from the published site):

```sh
NCLI_LOCAL=1 bin/run.sh
```

It builds a static binary on the host (`go.mod`'s own resolution, a local
`replace` included) and layers `compose.local.yaml` on top.

**Cost**: each round is a real, billed Claude Code session, not a mock
-- a full 11-round run costs about as much as a handful of normal coding
turns.

## Credentials

`bin/run.sh` copies (never bind-mounts) your `~/.claude` login into a
locked-down `.creds-seed/` dir, deleted when the run ends -- nothing
here writes back to your host's real Claude Code state. `.creds-seed/`
and `.env` are gitignored; never commit either.

## Known constraint: R6

`ncli bunker` (bare) needs a real TTY, which a headless agent can't
provide. `bin/run.sh` pre-starts the daemon itself via `script` before
R6 runs; the round only drives its scriptable surface (`connect`,
`status`, `sessions`, `history`).

## Known constraint: R11-R13

The agent-swarm scenario's NIP-OA credential has no documented `ncli`
command that mints one directly -- the round asks the agent to find a
scriptable path itself and say plainly if it can't, rather than papering
over the gap. `prepare_r10` pre-starts Meilisearch for the public-search
scenario the same way `prepare_r6` pre-starts the bunker daemon; if it
never comes up, R12 still runs and the search scenario simply surfaces
that as a finding instead of silently skipping it.

## Follow-up & extending

`followup/issues.md` tracks confirmed ncli bugs and known
round-coverage gaps -- append findings there, not just in chat.

To add a round: write `rounds/rN-name.md` + `bin/verify/rN-name.sh`,
then add `rN-name` to `ALL_ROUNDS` in `bin/run.sh`. A verifier must
check ground truth directly (relay/Blossom/agent state) -- re-parsing
the self-report's own claim isn't verification.
