# Round R2 -- Query a real public relay

`wss://relay.ohstr.com` is a real, live public Nostr relay reachable from
this machine. Use it as your target for this round.

1. Add it as a default relay: `ncli prefs relays add`.
2. `ncli ping` it and confirm it reports reachable.
3. `ncli find` a small number (2-5) of recent kind:1 events from it.
4. `ncli dump` a small batch of kind:1 events to a JSON file and confirm
   the file actually contains a JSON array (not empty, not `null`).
5. `ncli prefs relays list`, then remove it, then re-add it, then finally
   `ncli prefs relays clear` at the very end of this round -- leave your
   relay list empty when you're done (later rounds add whatever they need
   themselves).

Write your self-report to `/report/r2-query.self-report.json`, including
exactly how many events you actually saw at each step (not an estimate).
