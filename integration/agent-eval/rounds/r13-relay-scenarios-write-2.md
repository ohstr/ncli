# Round R13 -- Relay scenarios: four more admission-control shapes

Pull in the `ncli-relay-ops` skill if you don't already have it from an
earlier round. This round covers four more scenarios from its "Scenario
examples" table -- each a config you author yourself from the skill's
description, not a file handed to you. For each one: author the config,
start it as a background process on the port given, confirm its defining
behavior actually holds, then stop it before moving to the next.

## 1. Accountability relay (port 6521)

Author an `accountability-relay` config: `auth_required` on its own, with
no membership block at all. Start it.

1. Try to publish a kind:1 event with no AUTH at all. It should be
   refused.
2. Generate a fresh identity, authenticate as it (no invite, no
   enrollment -- there's no membership here), and publish again as that
   same identity. It should now be accepted.

The point is the contrast with scenario 3's community-membership
mechanism (if you did R9): here, *any* signed-in identity is let through,
not a specific allowed list.

## 2. Enterprise-compliance relay (port 6522)

Author an `enterprise-compliance-relay` config: membership, auth, and
`membership_required` all on, *and* `pow.strict`/`pow.min` on top, *and*
`nip86` enabled. Start it.

1. Enroll a fresh identity as a member (admin-bypass `ncli relay members
   add`, you're the operator here).
2. Authenticate as that member and publish an ordinary kind:1 event with
   no proof-of-work mined into it. It should still be rejected, and the
   rejection should mention `pow`, not a membership error -- being
   enrolled does not exempt you from the PoW requirement. These checks
   stack; neither is a substitute for the other.
3. Confirm the NIP-86 management API is actually there: an unsigned POST
   to the relay's own URL with `Content-Type: application/nostr+json+rpc`
   should be refused (401-shaped -- "there but needs a signature"), not
   404 ("never mounted at all").

## 3. Family-private relay (port 6523)

Author a `family-private-relay` config (same membership mechanism as
scenario 1 of R9's community-membership relay, if you ran it -- the
difference is purely operational). Start it. Enroll two or three freshly
generated identities directly via the admin bypass -- no invite codes.
Confirm each one can publish once enrolled, and that a pubkey you never
enrolled still can't.

## 4. Tracked-membership relay (port 6524)

Author a `tracked-membership-relay` config: `membership.enabled: true`,
but leave `membership_required` (and `auth_required`) off. Start it.

1. Publish a kind:1 event from a freshly generated identity you have
   *not* enrolled anywhere, with no AUTH at all. It should be accepted --
   this scenario's whole point is that membership here doesn't gate
   anything.
2. Enroll one identity as a member with a role (`ncli relay members add
   <pubkey> --role vip`), then `ncli relay members list` and confirm the
   roster actually shows it. Membership is tracked and queryable even
   though it isn't enforced.

Stop every relay process you started before finishing.

Write your self-report to `/report/r13-relay-scenarios-write-2.self-report.json`
(schema: `/rounds/_report-schema.json`), one entry per scenario, including
the exact config you authored for each (inline in the report, not just a
path -- the container may not persist it).
