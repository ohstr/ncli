# Round R3 -- Relay lifecycle and administration

## Part A -- run your own relay

1. Author a minimal relay config YAML from scratch. Check AGENTS.md and
   (if you pulled it in R0) the relay-ops skill for exactly which fields
   are required -- at minimum you'll need a `store` path, `logdir`, and
   either `nip11.pubkey` or `nip11.privkey`.
2. Start `ncli relay --config <your-file>` as a background process on
   port `6500` (nothing else on this machine uses that port).
3. Confirm it's actually serving -- e.g. `ncli ping localhost:6500` or
   fetching its NIP-11 document -- then register it with
   `ncli relay context add`.
4. Stop the background process cleanly before moving to Part B.

## Part B -- administer the shared relay

A second relay is already running elsewhere on this machine, on port
`5500`, and you have its config file at `/relay/relay.yaml` (you didn't
start it, but having its config is what lets you authenticate as its
admin -- see AGENTS.md's `relay stats`/`reindex`/`clear` and
`relay members`/`invites`/`roles` sections for why the config file itself
is the credential here).

1. `ncli relay stats --config /relay/relay.yaml`.
2. Create an invite code (`ncli relay invites create`), list invites,
   then revoke the one you just created.
3. Enroll a freshly generated pubkey as a member (`ncli relay members
   add`), list members, show that one member's record specifically, then
   remove it again.
4. List roles, then create one new role.
5. Trigger `ncli relay reindex search` and confirm the request was
   accepted (it's async -- check `relay stats` to see it start, you don't
   need to wait for it to finish).

Write your self-report to `/report/r3-relay-ops.self-report.json`,
covering both parts.
