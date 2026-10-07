# Round R9 -- Relay-hosted groups (NIP-29)

The relay at `ws://localhost:5500` hosts NIP-29 groups. You act as two
people: `eval-agent` (already in your vault) runs a group, and a second
identity you create, `eval-member`, joins it. Read the groups skill
before starting.

1. As `eval-agent`, create a group with id `r9-team`. Find out whether
   it's private and/or closed by default, and show it both as
   `eval-agent` and with no identity at all. Explain the difference you
   see.
2. Rename it to `R9 Team` with a one-line description, without changing
   its privacy or membership settings.
3. Create a subgroup `r9-notes` under `r9-team`, and show the group
   hierarchy.
4. Create the `eval-member` identity. Let it in with an invite code: as
   `eval-agent` create a code, then as `eval-member` join `r9-team` with
   it. Confirm `eval-member` now sees `r9-team` among *its own* groups.
5. As `eval-member`, post a chat message into `r9-team`. As
   `eval-agent`, pin that message, confirm the pin, then delete the
   message from the group and clear the pins.
6. As `eval-agent`, add a freshly generated pubkey as a member, confirm
   it's listed, then remove it.
7. As `eval-member`, leave `r9-team`. Confirm it's no longer a member.
8. As `eval-agent`, delete the subgroup `r9-notes` (keep `r9-team`).

Write your self-report to `/report/r9-groups.self-report.json`. Include
the id of the message you posted in step 5.
