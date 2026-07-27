# Round R4 -- Publish and apply

The shared relay from an earlier round is running at `ws://relay:5500`.
Use your `eval-agent` identity (from R1 -- if it's not in your vault for
some reason, generate a new one under that same label first).

1. Register `ws://relay:5500` as a relay (prefs, or a targets file --
   your choice), and publish one signed kind:1 event to it with
   `ncli publish`. Confirm the per-(event,relay) report shows it was
   accepted.
2. `ncli find` that exact event back from the relay by its event ID and
   confirm it round-trips (same content, same id).
3. Write a small `apply` spec (`kind: apply`, a `stream` or `sync` flow --
   check AGENTS.md / the apply skill for the exact shape) that pulls a
   few events from `ws://relay:5500` into a local file or store, and run
   it with `ncli apply -f`.

Write your self-report to `/report/r4-publish-apply.self-report.json`,
including the published event's exact ID.
