# Round R0 -- Bootstrap

You need a tool called `ncli`. You've been pointed at a URL to start from.

1. Fetch `https://ohstr.github.io/ncli/PROMPT.md` and follow it step by
   step, in order.
2. That file will have you confirm the install with `ncli id --json` --
   actually run it and look at the output, don't just assume the install
   worked because a command exited.
3. It will point you at a "real reference" file -- fetch that too.
4. It will tell you how to pull in task-specific skills for whatever you
   do next -- do that as well, using whichever of the two methods it
   offers fits this environment.

Write your self-report to `/report/r0-bootstrap.self-report.json`
(schema: `/rounds/_report-schema.json`). Include:
- which install method you ended up using and why
- the exact `ncli version --json` output after install
- whether the skills step actually produced anything on disk, and where
