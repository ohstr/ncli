# Huddle e2e integration test stack

One real `ncli relay` container with `huddle.enabled`, testing both doors
end-to-end: `/huddle/{id}/audio` and `/huddle/{id}/rtc`, NIP-42 admission,
and fan-out between real `huddleclient` peers over real WebSockets and real
pion peers over real WebRTC.

Unlike the stream/inspect/sync stacks this is **not** an `ncli apply`
workflow, so there is no spec fixture -- the peers are `huddleclient`
clients constructed in the test.

## What's here

- `compose.yaml` -- one `relay` service (21530 TCP for signalling,
  21600-21650/udp for media), project `ncli-huddle-itest`.
- `relay.yaml` -- relay config with the `huddle:` block enabled, `rtc: true`,
  and a pinned `udpPortRange`.

`udpPortRange` is not optional here. Media does not travel over the
signalling socket, and the ephemeral ports the OS would otherwise pick are
not ones compose can publish -- so without it every WebRTC scenario
negotiates fine and then never carries a packet. The range is published in
`compose.yaml` and has to match.

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

Fourteen scenarios. The first nine drive the WebSocket door:

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

The remaining five drive the WebRTC door. Each publishing peer sends a
random payload per track, recorded and matched on arrival, so a crossed
stream fails rather than a count merely coming out right. The relay never
decodes any of it, which is what makes byte-identity the assertion:

- **`MixedTransportConversation`** -- 2 WebRTC peers and 2 WebSocket peers in
  one room, every peer hearing every other, each payload matched to the peer
  that actually sent it, and nobody hearing themselves. This is the whole
  point of sharing one `room.Manager` between the two endpoints.
- **`VideoAndScreenShare`** -- both browsers publish a camera and a screen at
  once and receive the other's two tracks, told apart by track id.
- **`WebSocketPeerSeesNoVideo`** -- the documented graceful degrade: a
  WebSocket peer hears the call and misses only the picture.
- **`LateJoinerGetsTheLiveCall`** -- a third browser joins a call in progress
  and receives audio and video sent after it arrived. This is the
  renegotiation path: the SFU offers the newcomer a track per speaker, and a
  peer that ignored those offers would sit in a silent room that still looks
  connected.
- **`CandidatesBeforeTheOffer`** -- trickled candidates sent ahead of the
  offer, which is the order a browser actually produces. The relay used to
  reject and discard them, which cost the call every path it could not
  rediscover.

Audio and video are labelled differently on the wire, and a peer reading
them has to know that. An outbound audio track is renamed after its speaker
(`audio-<pubkey>`, stream id the pubkey) so a receiver can group one
speaker's streams; a video track keeps the publisher's own id, which is what
keeps a camera distinguishable from a screen share.

## Running it where Docker is not on this host

The scenarios dial the published port on `localhost` by default. Set
`NCLI_ITEST_HUDDLE_ENDPOINT` when the daemon runs elsewhere (a
Docker-out-of-Docker setup, where published ports do not land on localhost)
and point it at wherever the relay is actually reachable, e.g.
`ws://172.17.0.8:5500`. Only the dialled address changes: the NIP-42 relay
tag stays `relay.yaml`'s `nip11.url`, because the endpoint validates the tag
against its own config rather than against wherever the client connected
from.

WebRTC needs more than a reachable signalling port, though -- ICE has to
find a working path between the test process and the container. On an
ordinary Docker host that is automatic. Where the two sit on networks that
cannot route to each other, attach the relay to a network the test process
shares (`docker network connect bridge ncli-huddle-itest-relay-1`) or the
media scenarios will negotiate and then time out with no packets.

## Not covered here

- **A browser.** pion is a full WebRTC client, so the scenarios above
  exercise the real wire -- what a browser adds on top is capture and
  playback, not a different protocol. Confirming the capture UI stays a
  manual step.
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

## Stress stack: the latency soak

```
just test-integration-huddle-stress
```

Runs `TestHuddleStress` against `stress-compose.yaml` — one relay on its own
compose project and port (21540), so it can run alongside `just huddle`.
Not in CI: two 60s scenarios plus setup is ~2.5 minutes. Run it before a
release, or when touching the fan-out, the per-peer queue, or the WebSocket
write path.

**Read the log output, not just pass/fail.** The assertions are deliberately
loose — there is no historical baseline, so they catch catastrophe and
regressions in *shape* (senders stalling, latency drifting upward over the
minute, corrupted payloads, goroutine leaks). The numbers it prints are the
actual deliverable.

Two scenarios, both with the room at `room.MaxPeers` (25):

- **`RealisticTwoSpeakers`** — 2 people talking, which is what a meeting looks
  like, and the case whose latency a participant actually experiences.
- **`PathologicalEveryoneSpeaks`** — all 25 at once. Delivery is *allowed* to
  suffer; the per-peer queue drops when full by design. What must hold is that
  latency does not run away and senders are never blocked.

### How latency is measured

Every peer runs in the test process, so the reader's clock is the sender's
clock — no clock sync to get wrong. The send time is written into the Opus
payload, which the relay treats as opaque and forwards untouched, so the
difference on arrival is a true mouth-to-ear figure for the relay hop.

### First observed run

In-container loopback, AMD EPYC-Genoa:

| scenario | delivery | p50 | p95 | p99 | max | drift (first→last tenth) |
|---|---|---|---|---|---|---|
| 2 of 25 speaking | 100.00% | 526µs | 747µs | 938µs | 5.4ms | 531µs → 512µs |
| 25 of 25 speaking | 65.5% | 976µs | 1.79ms | 2.45ms | 7.2ms | 1.019ms → 1.012ms |

Both paced exactly — senders finished within 1 ms of the 60s target — so the
queue sheds load rather than blocking, and neither case drifts upward.

**What this does not say.** Peers reach the relay over loopback, so these are
the relay's *own* latency and shedding behaviour, not end-to-end call latency.
A real call adds network RTT and a client jitter buffer, both of which dominate
these figures. Measuring it this way is what isolates the part this repo
controls: against a ~150 ms mouth-to-ear budget, the relay's contribution in the
realistic case is well under 1%.

The 34% shed in the saturated case cannot be attributed precisely between the
relay's 8-frame queue and the test's own readers without server-side
instrumentation. At 5× the realistic frame rate, either way it is the
drop-don't-block policy doing its job.
