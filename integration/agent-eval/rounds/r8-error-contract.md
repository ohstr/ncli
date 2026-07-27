# Round R8 -- Error-contract conformance

AGENTS.md documents an exact contract every command follows on failure:
exactly one error report, always on stderr and never stdout; with
`--json`, a `{"error","code","retryable","input"}` object where `code` is
one of `usage`/`invalid_input`/`not_found`/`conflict`/`network`/`auth`/
`internal`, each with its own fixed exit code. Your job this round is to
deliberately break things and check the contract actually holds -- don't
fix anything, just probe and record what you actually observed.

Probe at least these, each with `--json`:

1. A command missing a required flag/arg -- expect `usage`, exit 2.
2. `ncli decode` on a malformed bech32 string -- expect `invalid_input`,
   exit 3.
3. Looking up something that doesn't exist (a vault label, an npub with
   no matching data, etc.) -- expect `not_found`, exit 4.
4. `ncli find` (or `dump`/`miner check`) against a relay host that can't
   be reached at all (made-up hostname, or a real host on a closed port)
   -- expect `network`, exit 6, `retryable: true`.
4b. `ncli ping` against that same unreachable host -- AGENTS.md documents
   this one as a deliberate exception: reachability is exactly what
   `ping` tests for, so it reports `internal`, exit 1, not `network`.
   Confirm that's actually what happens, not `network`.
5. Any relay-membership admin call that would only make sense with
   membership enabled, run against a config where it's disabled (author a
   throwaway `relay.yaml` with `membership.enabled: false` yourself if you
   need one) -- expect `usage`. If you can't construct a case for this,
   say so and explain from AGENTS.md what you'd expect instead.
6. If you can construct a case that should produce `auth` (wrong
   credentials / rejected signature), try it; otherwise note plainly that
   you couldn't exercise this one and why.

For every probe, record in your self-report: the exact command, the exit
code, stdout (should be empty on failure), and the parsed JSON error
object from stderr. If anything doesn't match AGENTS.md's table, call
that out explicitly as a finding rather than smoothing over it.

Write your self-report to `/report/r8-error-contract.self-report.json`,
with one entry per probe.
