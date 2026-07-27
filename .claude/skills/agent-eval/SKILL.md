---
name: agent-eval
description: Run the ncli agent-capability integration test (integration/agent-eval) -- drives a fresh Claude Code agent through ncli's public onboarding/capability surface inside an isolated Docker Compose stack, independently verifies its claims against the real relay/Blossom containers, and produces a report.
license: Unlicense
---

# ncli agent-capability eval

`integration/agent-eval/` is a different integration test from
`.claude/skills/verify`: instead of building `ncli` from local source to
check a code change, it drives a **fresh** Claude Code agent -- in a
container that has never seen this repo -- through the same path any
real external user/agent would follow (`Fetch
https://ohstr.github.io/ncli/PROMPT.md`), then independently re-checks
its claims against the real relay/Blossom containers rather than trusting
its self-report. Full architecture: `integration/agent-eval/README.md`.

## Running it

```sh
cd integration/agent-eval
bin/run.sh                              # all 9 rounds (r0-bootstrap .. r8-error-contract)
bin/run.sh r0-bootstrap r2-query        # just these, in the order given
```

Requires Docker, and this host already logged into Claude Code (the
harness copies -- never bind-mounts -- the existing `~/.claude` login
into the container; see the README's "Credentials" section for exactly
what it does and cleans up).

**Before running, tell the user plainly**: each round is a real, billed
Claude Code session against their subscription/API usage, not a mock --
confirm they want to spend that before kicking off a full 9-round run.

**A round with a tiny (few-KB) transcript and no self-report almost
always means it hit a rate/spend limit mid-session, not that it failed
on its own merits** -- check `<round>.transcript.jsonl`'s final `result`
message for `"is_error":true`/`"api_error_status"` before concluding a
round actually failed the task.

## After it finishes

Read `report/<run-id>/report.md` (path printed at the end of the run,
also in `bin/run.sh`'s own stdout) and summarize it for the user:
per-round self-reported outcome, the independently-verified result (this
one is what actually matters -- see the README on why), and judge notes.
Call out any round where `verified` is `false` or the self-report is
missing, and check whether that's a real failure or a rate/spend-limit
cutoff (above) before characterizing it either way.

## If you find a new ncli bug or contract mismatch

Append it to `integration/agent-eval/followup/issues.md` (same format as
the existing entries: repro command, expected vs. actual, how it was
confirmed) rather than only mentioning it in chat -- that file is this
harness's running list of confirmed discrepancies between `AGENTS.md`'s
documented contract and ncli's actual behavior. It also has a "known
coverage gaps" section for rounds that don't yet exercise some command/
mode -- check it before assuming a gap you notice is new.
