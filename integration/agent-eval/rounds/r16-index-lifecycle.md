# Round R16 -- An index across restarts, from its JSON alone

You will run your own relay and watch how it looks after its profile
stats index while the feature is turned off and on. Use only `--json`
output to decide what happened; quote the fields you relied on (`state`,
`action`, `reason`, `hint`, `entries`, `kind`, `trigger`, `summary`,
`noop`) in your self-report.

Use these exact paths and ports: config `/home/evaluser/work/r16/relay.yaml`,
store `/home/evaluser/work/r16/notes.db`, logs under
`/home/evaluser/work/r16/logs`, relay port `6600`, admin listener
`127.0.0.1:6601`. The relay needs its own key in `nip11.privkey` for
profile stats; generate one with `ncli id`.

1. Write the config with profile stats on (see the relay-ops skill's
   `cache` section) and start the relay in the background with `--json`.
2. From a second identity, sign and publish a contact list (kind 3)
   following two pubkeys. Report the stats index's `state` and `entries`
   from `ncli relay index list --config ... --json`.
3. Stop the relay, turn profile stats off, start it again, and publish a
   newer contact list from the same identity following only one of them.
   Report the stats index's `state` and `hint`.
4. Stop the relay, turn profile stats back on, start it again. Without
   starting anything yourself, find out what the relay did to the stats
   index and whether it is current: report the job (`kind`, `trigger`,
   `state`, `summary`) and the index's `entries` once it is `ready`.
5. Rebuild the stats index and wait for the outcome in one command;
   report the job's `summary`.
6. Try to drop the stats index while it is in use; report the error
   `code` and the exit code.
7. Stop the relay, turn profile stats off, start it, drop the stats
   index, then drop it again. Report both answers (`summary`, `noop`).
8. Stop the relay.

Write your self-report to `/report/r16-index-lifecycle.self-report.json`.
