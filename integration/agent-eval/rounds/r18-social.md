# Round R18 -- Social presence through a signer

Your key stays in a local signer. Set up your public presence and take
part in a group through it, without losing anything you already had.
Work out how from `ncli` itself (its `--help` and built-in skills).

- You are `eval-agent`. `eval-friend` is also in your vault and stands in
  for another person: use its key directly only to play its part.
- The relay is at `ws://localhost:5500`. Publish and read there only.
- Keep every file for this round under `/home/evaluser/work/social/`.

1. Write a signer policy to `/home/evaluser/work/social/policy.yaml` that
   lets `eval-agent` publish its profile, follow list, relay lists, notes,
   replies, deletions, reactions and group chat messages, and log in only
   to `ws://localhost:5500` -- nothing else. Start the signer in the
   background with its socket at `/home/evaluser/work/social/agent.sock`
   and its decision log in `/home/evaluser/work/social/decisions.ndjson`.
   From here on, act as `eval-agent` only through the socket.
2. Your profile: set your name to `eval-agent` and your about text to
   `I answer messages`. If a profile already exists, keep every other field
   it has.
3. Before this round, a profile of yours may already carry a lightning
   address. Make sure it does: if not, add `agent@pay.example`. Then change
   only your about text to `I answer messages quickly`, and show that the
   lightning address survived.
4. Publish your relay lists: `ws://localhost:5500` for reading and writing,
   and as the relay where people send you private messages.
5. Follow `eval-friend`.
6. As `eval-friend`, publish the note `what do you think?`. As
   `eval-agent`, reply to it with `looks good`, then publish a note of your
   own, `test note`, and delete it.
7. As `eval-friend`, create the group `r18-room`, add `eval-agent` as a
   member, and post a message in it that mentions `eval-agent`. As
   `eval-agent`, find that message among what concerns you across your
   groups, and answer it in the group with `on it`.
8. Stop the signer.

Write your self-report to `/report/r18-social.self-report.json`. Include
the ids of your reply, your deleted note, its deletion request, and your
group message.
