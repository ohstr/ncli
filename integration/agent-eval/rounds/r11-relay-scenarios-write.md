# Round R11 -- Relay scenarios: what gets accepted or rejected

Pull in the `ncli-relay-ops` skill if you don't already have it from an
earlier round. Its "Scenario examples" table names several real relay
deployment shapes. This round is about four of them, each a config you
author yourself from the skill's description -- not a file handed to you.
For each one: author the config, start it as a background process on the
port given, confirm its defining behavior actually holds, then stop it
before moving to the next.

## 1. Personal relay (port 6501) -- control case

Author a `personal-relay` config (minimal -- no auth, no membership).
Start it, then publish an ordinary kind:1 event from a freshly generated
identity with no special setup. It should be accepted. This is the
control: if this one fails, something is wrong with your setup, not with
the scenario.

## 2. Community-membership relay (port 6502)

Author a `community-membership-relay` config: NIP-43 membership enabled,
`auth_required` and `membership_required` both on. Start it.

1. Generate a fresh identity and try to publish a kind:1 event as it.
   Record whether it was accepted or refused.
2. Enroll that same pubkey as a member (`ncli relay members add` --
   you're the relay's own operator here, so the admin-bypass path applies,
   no invite code needed).
3. Publish again as the same identity. It should now be accepted.

The point is the *contrast*: refused before enrollment, accepted after,
same identity both times.

## 3. Anti-spam relay (port 6503)

Author an `anti-spam-relay` config: `pow.strict: true`, a `pow.min` of
around 20. Start it. Publish an ordinary kind:1 event with no proof-of-work
mined into it. It should be rejected, and the rejection message should
mention `pow`. (You do not need to also prove the *positive* case by
mining real PoW -- `ncli miner mine` can do that if you want to, but
confirming the rejection is the part this round is after.)

## 4. Agent-swarm relay (port 6504)

Author an `agent-swarm-relay` config: NIP-43 membership plus `agent_auth`
enabled. Start it.

1. Generate an "owner" identity and enroll it as a member the same way as
   step 2.
2. The skill's `ncli-identity` or `ncli-relay-ops` material should tell you
   how an agent key presents a NIP-OA credential during AUTH to inherit
   its owner's membership (a NIP-46/bunker pairing is one real path; if
   you can construct the credential and a second identity to act as the
   agent, do so and publish as the agent). If you cannot find a scriptable
   way to mint and present a NIP-OA credential from the CLI alone within
   this round, say so plainly in your self-report rather than faking it --
   that's a real finding, not a failure on your part.

Stop every relay process you started before finishing.

Write your self-report to `/report/r11-relay-scenarios-write.self-report.json`
(schema: `/rounds/_report-schema.json`), one entry per scenario, including
the exact config you authored for each (inline in the report, not just a
path -- the container may not persist it).
