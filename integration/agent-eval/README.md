# ncli agent-capability eval

Tests **ncli as consumed by an agent**, not this repo's source. A fresh
Claude Code agent, in a container that's never seen this checkout, is
pointed at `https://ohstr.github.io/ncli/PROMPT.md` and follows it --
same as any real external user/agent picking `ncli` up for the first
time. Unlike `.claude/skills/verify` (builds from local source to check
a change), this installs the **published** image/docs, so it catches
docs drifting from what's actually shipped.

## Architecture

`compose.yaml` runs three services:

- **`relay`** -- published `ghcr.io/ohstr/ncli:latest`, admin enabled
  (so R3 has something real to administer).
- **`blossom`** -- reference Blossom server, for R7.
- **`agent`** -- the agent-under-test's machine (`agent/Dockerfile`): no
  Go/`ncli`/repo copy, just curl/bash/Node + Claude Code. Runs non-root
  (Claude Code refuses `bypassPermissions` as root) and shares `relay`'s
  network namespace, since ncli's admin commands hard-target
  `localhost` with no `--url` override.

Nine rounds (`rounds/r0-bootstrap.md` .. `r8-error-contract.md`) cover
install, identity, public-relay queries, relay admin, publish/apply,
PoW mining, NIP-46 bunker signing, Blossom, and the documented
error-code contract. Each round is a fresh, non-interactive `claude -p`
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

**Cost**: each round is a real, billed Claude Code session, not a mock
-- a full 9-round run costs about as much as a handful of normal coding
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

## Follow-up & extending

`followup/issues.md` tracks confirmed ncli bugs and known
round-coverage gaps -- append findings there, not just in chat.

To add a round: write `rounds/rN-name.md` + `bin/verify/rN-name.sh`,
then add `rN-name` to `ALL_ROUNDS` in `bin/run.sh`. A verifier must
check ground truth directly (relay/Blossom/agent state) -- re-parsing
the self-report's own claim isn't verification.
