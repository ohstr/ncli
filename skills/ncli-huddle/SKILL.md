---
name: ncli-huddle
description: >-
  Host real-time voice rooms ("huddles") on an ncli relay and list the
  ephemeral transport rooms currently live on one (ncli huddle list) --
  enable the relay's huddle: block (WebSocket Opus audio, plus an optional
  WebRTC endpoint carrying video and screen share). Joining a call,
  watching the roster, chatting, and discovering NIP-53 spaces all moved
  to "ncli space" (create/list/show/join) -- see
  skills/ncli-space/SKILL.md for that, and read this skill for setting up
  the relay side, the transport-level room-listing command, the
  join/roster/audio/chat experience itself once joined, and how a join can
  be refused. Use when setting up voice on a relay, listing which rooms
  are occupied right now, understanding the audio/chat UI after joining,
  letting a browser or a buzz client into the same room, or working out
  why a join was refused.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's cli/huddle/*.go, cli/space/*.go, and
cli/relay/huddle.go as of writing. This skill is self-contained by design
and won't see repo changes automatically -- update by hand if
flags/behavior change. -->

# ncli huddle

A huddle is a voice room hosted by the relay itself. The relay forwards
Opus frames between participants and **never decodes them**, so it links no
audio codec and stays pure Go.

**`ncli huddle` itself now only lists rooms** (`ncli huddle list`, below).
Joining one, and everything about NIP-53 spaces (creating, listing,
showing), lives under `ncli space` -- see `skills/ncli-space/SKILL.md` for
those commands. This skill still covers the relay-side setup and
everything about the join/roster/audio/chat *experience* once you're in a
call, since none of that changed -- only which top-level command actually
starts it.

Two doors open onto the same room:

| Endpoint | Speaks | Carries |
|---|---|---|
| `/huddle/{id}/audio` | plain WebSocket, Opus frames | audio |
| `/huddle/{id}/rtc` | WebRTC (SDP/ICE, DTLS/SRTP) | audio, video, screen share |

The same room id is the same call through either door. Audio crosses between
them by repacketization (huddle frame <-> RTP), not transcoding. Video exists
only among WebRTC peers, so a WebSocket-only client in a screen-share call
hears everything and simply misses the picture.

## Hosting: the relay side

In `relay.yaml`:

```yaml
nip11:
  # required once huddle.enabled is true
  url: wss://relay.example
  privkey: <hex>
huddle:
  enabled: true
  maxRooms: 100
  # true = only NIP-43 relay members may join
  requireMembership: false
  # browser origins; empty allows any
  allowedOrigins: []
  authTimeout: 5s
  pingInterval: 30s
  # also mount /huddle/{id}/rtc
  rtc: true
  iceServers:
    - urls: ["stun:stun.example:3478"]
    - urls: ["turn:turn.example:3478"]
      username: user
      credential: secret
  # pin media ports; needed in a container
  udpPortRange: "21600-21650"
```

`examples/relay/community-voice-relay.yaml` is a ready-made preset;
`examples/relay/full.yaml` documents every field.

Things that bite:

- **`huddle.enabled` requires `nip11.url`.** A joining client's NIP-42 event
  names the relay, and the endpoint has to know what to validate against.
- **Omit the block and nothing is mounted** -- a client gets the same 404 an
  older relay gives it.
- **`allowedOrigins` is empty by default, which allows any origin.** Admission is
  gated by a signed NIP-42 challenge, so `Origin` is not the security boundary,
  and refusing on it would only lock out browsers while every CLI client kept
  working. Set it to narrow the endpoint to your own app.
- **`requireMembership` is independent of
  `nip11.limitation.membership_required`**, which gates the Nostr socket. A
  relay can have open reading and closed calls, or the reverse.
- **TURN matters.** With no `iceServers`, only peers on the same network
  connect; TURN is what carries peers behind symmetric NAT.
- **In a container, set `udpPortRange` and publish it.** Media does not go over
  the signalling socket. Without a pinned range the OS picks ephemeral ports,
  which no `-p` covers, so the call connects as far as signalling and then no
  audio arrives. Publish the same range as UDP (`-p 21600-21650:21600-21650/udp`).

## Spaces vs. rooms

Two different things, two different ids. Mixing them up is the easiest
mistake here:

| | **Space** (NIP-53 kind:30312) | **Room** (huddle transport) |
|---|---|---|
| What it is | a published event describing where a meeting lives and who hosts it | the live audio channel itself |
| Its id | the `d` tag; addressable as `30312:<pubkey>:<d>` | a path segment in `/huddle/{id}/audio` |
| Lifetime | durable -- it exists until replaced or closed | only while occupied; gone when the last peer leaves |
| Who assigns it | the author picks it; unique per `(kind, pubkey, d)` | whoever joins first; unique by string within one relay |
| Listed by | `ncli space list` | `ncli huddle list` |

A space's `service`/`endpoint` tag is what points at the transport, and
`status` is `open` / `private` / `closed`. Sessions inside a space are
kind:30313, each with its own `d` tag, an `a` tag back to the parent space,
and `status` going `planned -> live -> ended`.

`ncli space join` takes **either**: a room id dials the transport directly
(roster only), while an activity is resolved through the space to find the
relay and room, and brings chat with it.

`ncli relay` only validates and stores a 30312/30313, it doesn't author one
itself (NIP-53 is declared in its NIP-11 document when huddles are on) --
publishing a space is `ncli space create`, covered in
`skills/ncli-space/SKILL.md`.

## Finding a live room

```sh
ncli huddle list --relay wss://relay.example
# members-only relay
ncli huddle list --relay ws://localhost:7777 --identity satoshi
ncli huddle list --relay wss://relay.example --json
```

```
ROOM     PEERS  PROTOCOL
standup  1      v3
```

Rooms are **created on join and dropped when the last peer leaves**, so this
is every room that exists -- there is no durable list, and a room nobody is in
is not a room. Nothing ended or empty is ever listed. `(no live huddles)` in
text mode, `{"rooms":[]}` under `--json` (an array, never `null`).

This is worth running first because **a room id is otherwise pure out-of-band
knowledge**: `join` on an id nobody is using opens that room rather than
failing, so a typo puts you alone in a new call with no error to warn you.

`PROTOCOL` is the version the room was pinned to by whoever opened it. A build
speaking anything else is refused with `upgrade_required`, so a mismatch in
this column is the reason a join will fail.

An identity is only needed when the relay sets `requireMembership: true`. The
list is gated exactly like a join -- it is the set of rooms you could already
walk into, so it is open on an open relay and members-only on a closed one.
Unsigned against a closed relay is an `auth` failure (exit 7), not an empty
list. A relay with no `huddle:` block has no endpoint to ask, which reads as
`has no huddle endpoint: the relay is not running with huddles enabled`.

## Joining from the terminal

`join` is implemented here (`cli/huddle`) but only ever reachable as
`ncli space join` -- see `skills/ncli-space/SKILL.md` for why. The flags,
resolution, and everything below in this section are otherwise unchanged.

```sh
ncli space join standup --relay wss://relay.example
ncli space join standup --relay ws://localhost:7777 --identity satoshi
# --relay falls back to the first configured prefs relay
ncli space join standup

# By NIP-53 activity: resolves the space for the relay and room, and opens chat
ncli space join 30312:<pubkey>:standup --relay wss://relay.example
# relay hints come from the naddr
ncli space join naddr1...
# a session, resolved via its parent space
ncli space join 30313:<pubkey>:today
ncli space join 30312:<pubkey>:standup --no-chat
```

Without a terminal (or with `--json`) there is no board: the call streams
as JSON lines -- see "Joining without a terminal" in
`skills/ncli-space/SKILL.md`.

Given an activity, `--relay` is where the *space event* is looked up (naddr
hints, then prefs relays, if it is omitted). Which relay and room get **dialed**
comes from the space itself: a published `endpoint` naming a full
`/huddle/<id>/audio` URL is taken at its word, otherwise `service` is the relay
and the space's own `d` tag is the room id. NIP-53 does not standardize that
mapping, so a publisher doing something else needs the room id passed directly.

A `closed` space is refused; `private` is not -- it only means unadvertised, and
holding its coordinate means someone told you deliberately. A `30313` whose
status is `ended` is refused too.

`--identity` takes the same shapes as `id sign` (vault label, nsec, npub, hex,
nprofile, nip-05) and must resolve to a **private** key -- joining means
signing a NIP-42 event. With no flag it falls back to `space.identity` /
`NCLI_SPACE_IDENTITY` (matching the command it's actually reached through
now), then to the vault's sole entry when there is exactly one.

Room ids become a URL path segment, so `/`, `?`, `#` and `%` are rejected
rather than escaped: escaping would leave the client asking for one room and
the relay matching another.

### What the view shows

A live roster: every participant, a speaking indicator, a level meter, and a
state word.

```
 HUDDLE standup [3, 1 talking] ─────────────────────────
     PARTICIPANT            LEVEL        STATE
  ·  npub1abcdefghij...wxyz (you)  ··········  no audio yet
  ●  npub1klmnopqrst...uvwx        █████·····  speaking
  ·  npub1yzabcdefgh...ijkl        ··········  silent
```

- **Speaking** comes from the `level_dbov` telemetry every audio frame already
  carries, above `-55` dBov, held for 600 ms so a gap between words does not
  blink the indicator off.
- **Rows never reorder as people talk** -- self first, then by routing index.
  Sorting by loudness would make the list unreadable.
- **"no audio yet"** means nothing has arrived from that peer; **"silent"**
  means audio arrived but is below the threshold.
- **A remote peer is never shown as "muted".** The protocol has no mute
  signal -- muting is implemented by not sending -- so a muted peer and a quiet
  one are genuinely indistinguishable from the outside.

Keys: `Tab` cycles panels, `q` or `Ctrl+C` asks before leaving. Ctrl+C is
deliberately confirmed, so one stray keystroke does not drop a call.

### Hearing the call

Playback is compiled in only under a build tag:

```sh
# macOS, Windows: still cgo-free
go build -tags huddleaudio ./cmd/ncli
# Linux, needs libasound2-dev
CGO_ENABLED=1 go build -tags huddleaudio ./cmd/ncli
```

Decoding is pure Go (`pion/opus`) and builds everywhere. **Output is not**:
`ebitengine/oto` needs cgo and ALSA on Linux, and `ncli` ships every release
target with `CGO_ENABLED=0`, so it cannot be in the default build. macOS and
Windows do build the tagged binary cgo-free.

Without the tag the roster still works and the status line reads `watching only`
instead of `playing audio` -- otherwise a silent call and a working one look the
same.

Each speaker gets their own Opus decoder, since a decoder carries stream state
across frames; the mix sums in 32-bit and clamps, so several loud speakers clip
rather than wrap into a click.

### What it does not do

**It never captures a microphone, so joining puts no audio into the room.** There
is no established pure-Go capture library, and the CGO-free default build rules
out the cgo ones. Mic capture is planned behind its own `-tags huddlemic`.

Consequently there is **no mute control** -- there is no microphone to gate.

**No raise-hand** either: that publishes a NIP-53 kind 10312 with a `hand`
tag, and while the relay connection chat now brings would carry it, presence
has its own refresh/expiry rules that are not wired up. It arrives with those,
rather than shipping as a button that does nothing.

## Chat

Joining by space opens the conversation beside the roster:

```
┏━━━━━ HUDDLE standup [1] ━━━━━┓┌──────────── CHAT ─────────────
┃   PARTICIPANT        LEVEL   ┃│ 19:26 npub1hdgw9ky6f...39qn (you)
┃ ·  npub1hdgw9ky6f...39qn     ┃│   hello from the pty
┃                              ┃│   ↳ 19:27 npub1klmnopqrst...uvwx
┃                              ┃│     a threaded reply
┃                              ┃│ <r> reply  <y> quote  <Esc> clear  <Enter> send
┃                              ┃│ > message
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛└───────────────────────────────
```

Messages are NIP-53 **kind:1311**, scoped by an `a` tag to the activity you
joined -- the space, or the session if you named a 30313. Keys: `Tab` cycles
roster -> transcript -> composer, `<r>` replies to the selected message, `<y>`
quotes it, `<Esc>` clears a pending reply/quote, `<Enter>` sends.

- **Chat requires joining by space.** A 1311 message MUST name its activity,
  and a transport room id is not an addressable event, so `join standup` gives
  the roster only. There is no workaround -- it is what the message would be
  tagged with.
- **Threading is real**, not flat. A reply carries an `e` tag and is nested
  under its parent; quotes carry `q` tags and are noted inline. A reply that
  arrives before the message it answers sits at the root marked *"replying to a
  message not here"* and re-nests itself once the parent shows up, because
  relays deliver stored events in no guaranteed order. Indentation caps at four
  levels so a deep thread cannot squeeze the text away, and a reply *cycle*
  (which any peer can author) costs a dropped nesting, never a hung terminal.
- **What you see is what the relay accepted.** A sent message is not echoed
  locally; it appears when it arrives back through the subscription. If the
  relay rejects or does not confirm it within 10s, the hint line says `not
  sent:` with the reason rather than leaving a message that looks delivered.
- **Backlog is the last 200 messages**, so joining mid-conversation shows what
  was already said.
- **`--no-chat`** joins by space without the panel.
- The conversation uses its **own Nostr relay connection** (the space's
  `service` URL). The audio socket cannot carry it: that one speaks Opus frames
  and a few JSON control messages, nothing else.

## When a join is refused

The relay answers with a code, and the command translates it:

| Code | Means |
|---|---|
| `huddle_audio_unavailable` | the relay has no `huddle:` block, or `enabled: false` |
| `room_full` | the room is at capacity (25 peers) |
| `room_ended` | the call already finished |
| `room_unavailable` | the relay could not open the room (e.g. `maxRooms` reached) |
| `upgrade_required` | the room pinned a different protocol version -- the message names it |
| `join_rejected` / `auth_failed` | the relay refused this identity (often `requireMembership`) |

A code the build does not recognize is shown with the relay's own message
rather than a guessed explanation.

The most common self-inflicted failure is the **NIP-42 `relay` tag**: it must
name the relay's base URL (`wss://relay.example`), not the huddle endpoint.
The client handles this; a hand-rolled client usually does not.

## Interop

The WebSocket wire format matches `block/buzz`'s huddle transport
byte-for-byte (8-byte header: `seq` u16, `ts_48k` u32, `level_dbov` i8,
`flags` u8 with bit 0 = DTX; routing prefix `[peer_index]` on v1/v2,
`[peer_index][epoch]` on v3), so a buzz client can join a room on an `ncli`
relay. Buzz routes room ids as UUIDs, so use a UUID-shaped id when a buzz
client has to dial in.

Room lifecycle and discovery are NIP-53: a kind 30312 room whose `service` tag
points at the endpoint, a 30313 session going planned -> live -> ended, kind
10312 presence, and kind 1311 for in-call chat. `ncli relay` declares NIPs 29,
53, 71 and A0 in its NIP-11 document when huddles are enabled.
