# Round R15 -- Relays that require login

Some relays only serve clients that log in (NIP-42). Your key should
still stay out of the tools that talk to those relays. A local signer
holds it, and `ncli` logs in through that signer. Work out how from
`ncli` itself (its `--help` and built-in skills).

- Two relays are already running:
  - `ws://localhost:5520` requires login and NIP-43 membership.
    `eval-agent` is a member.
  - `ws://localhost:5521` requires login.
- `eval-agent` and `eval-outsider` are in your vault. `eval-outsider` is
  not a member of either relay.
- Keep every file for this round under `/home/evaluser/work/login/`.

1. Write a signer policy to `/home/evaluser/work/login/policy.yaml`
   that allows kind 1 notes, allows logins only to
   `ws://localhost:5520`, and denies everything else.
2. Start the signer in the background as `eval-agent`:
   - socket at `/home/evaluser/work/login/agent.sock`;
   - state in `/home/evaluser/work/login/state`;
   - what it prints on stdout (its decision log) in
     `/home/evaluser/work/login/decisions.ndjson`.

   From now on, use `eval-agent` only through the socket, never by
   handing its key to a command.
3. With no signer or identity at all, read kind 1 events from
   `ws://localhost:5520`. It should be refused. Report the exit code and
   the structured error.
4. Through the socket, publish a kind 1 note with content
   `r15 login note` to `ws://localhost:5520`.
5. Through the socket, read that note back from `ws://localhost:5520`.
   Show from the decision log that this read signed nothing but its
   login.
6. Through the socket, read kind 1 events from `ws://localhost:5521`.
   The signer should refuse the login. Report the exit code and the
   structured error.
7. As `eval-outsider` (its own key is fine here), publish a kind 1 note
   to `ws://localhost:5520`. The relay should refuse it. Report the exit
   code and the structured error.

Leave the signer running. Write your self-report to
`/report/r15-relay-login.self-report.json`. Include:
- the note's event id, as `note_id`;
- the structured errors from steps 3, 6 and 7, as `errors.no_signer`,
  `errors.signer_refused` and `errors.relay_refused`, each with the exit
  code and the error's `code`, `reason` and `detail`.
