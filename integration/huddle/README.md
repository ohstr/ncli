# Huddle e2e integration test stack

One real `ncli relay` container with `huddle.enabled`, testing the audio
endpoint end-to-end: `/huddle/{id}/audio`, NIP-42 admission, and frame
fan-out between real `huddleclient` peers over real WebSockets.

Unlike the stream/inspect/sync stacks this is **not** an `ncli apply`
workflow, so there is no spec fixture -- the peers are `huddleclient`
clients constructed in the test.

## What's here

- `compose.yaml` -- one `relay` service (21530), project `ncli-huddle-itest`.
- `relay.yaml` -- relay config with the `huddle:` block enabled.

`nip11.url` must be the **published** address the test dials
(`ws://localhost:21530`), not the in-container port: a joining client's
NIP-42 event names the relay in its `relay` tag, and the endpoint validates
the tag against `nip11.url`. Point it at `:5500` and every join fails
auth.

`requireMembership` is left false on purpose -- the membership gate has unit
coverage in `cli/relay/huddle_test.go`, and enabling it here would make
every scenario enrol first for no added signal.

## Automated: the Go test

```
just test-integration-huddle
```

Runs `TestHuddleIntegration`. Needs Docker; runs automatically in CI.

Each scenario uses its own room id. Rooms are created on first join and
dropped when the last peer leaves, so isolating by id keeps a lingering
peer out of the next scenario's roster -- and `maxRooms` sits above the
scenario count rather than exactly on it, because that teardown is not
instant.

Nine scenarios:

- **`TwoPeersHearEachOther`** -- the core guarantee: the Opus payload
  crosses the relay **byte-identical**, attributed to its author, with the
  header intact, and never echoes back to its sender. Byte-identity is the
  assertion that proves the relay runs no codec.
- **`EveryPeerHearsEveryOther`** -- 5 peers, each sending a distinct
  payload. Checks the payload matches its *author*, so a crossed stream
  fails rather than merely a count being right.
- **`JoinMidCall`** -- a third peer joins a call in progress: its own
  `joined` roster already lists the incumbents, they learn about it, and it
  receives audio sent after it arrived.
- **`LeaveRemovesThePeer`** -- a peer disconnects, its roster entry goes,
  and the call continues for whoever is left.
- **`SimultaneousSpeech`** -- 4 peers talking at once for ~1s. Deliberately
  does **not** assert zero drops: the per-peer queue drops when full by
  design. It asserts every peer keeps hearing every other, and that senders
  are not stalled -- the "drops, never queues" guarantee seen from the
  sending side.
- **`DTXAndLevelSurvive`** -- a comfort-noise frame at the silence floor
  with a reserved flag bit set alongside DTX. The relay must forward the
  flags byte untouched rather than normalizing it.
- **`RoomFullRefusesBeyondCapacity`** -- fills a room to `room.MaxPeers`
  (25) and checks the next joiner gets `room_full` while the full room
  keeps working.
- **`AuthForAnotherRelayIsRejected`** -- an auth event minted for a
  different relay must not admit its bearer, or a challenge captured
  elsewhere would be replayable here.
- **`VersionMismatchRequiresUpgrade`** -- a room pins its first peer's
  protocol version; a later peer on another version gets
  `upgrade_required` naming the room's version, rather than silently
  mis-parsing the routing prefix (whose shape differs between v2 and v3).

## Not covered here

- **The WebRTC door** (`/huddle/{id}/rtc`), video and screen share. Those
  need a WebRTC peer; `huddlesfu`'s own tests cover them in-process, and a
  browser is the only way to verify screen share for real.
- **Audio playback.** Nothing in this repo decodes Opus yet.
- **A real `buzz` client.** Wire compatibility is byte-for-byte by
  construction, but confirming it stays a manual acceptance step.

## Manual: poke at it by hand

```
just huddle up
ncli huddle join standup --relay ws://localhost:21530 --identity <nsec>
just huddle down
```

`ncli huddle join` needs a real terminal (it refuses otherwise). Two
terminals joining the same room id is the quickest way to watch the roster
and speaking indicator behave.
