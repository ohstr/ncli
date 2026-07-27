# Round R5 -- Proof-of-work mining

Use your `eval-agent` identity and the shared relay at `ws://relay:5500`.

1. Build a small unsigned kind:1 event spec and mine NIP-13 proof-of-work
   into it with `ncli miner`, at a modest difficulty (12-16 bits -- this
   should take well under a minute on this machine; if it's taking much
   longer than that, stop and lower the target rather than waiting
   indefinitely).
2. Confirm the mined event's own tags actually reflect the
   nonce/target you asked for -- don't just trust the command's exit
   code.
3. Sign and publish that mined event to `ws://relay:5500`.
4. Use `ncli miner check` against `ws://relay:5500` to verify the
   published event actually meets that difficulty.

Write your self-report to `/report/r5-miner.self-report.json`, including
the difficulty you targeted and roughly how long mining took.
