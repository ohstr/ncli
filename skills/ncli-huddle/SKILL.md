---
name: ncli-huddle
description: Host and join real-time voice rooms ("huddles") on an ncli relay -- enable the relay's huddle: block (WebSocket Opus audio, plus an optional WebRTC endpoint carrying video and screen share), then join a room with ncli huddle join to watch the live roster and who is speaking. Use when setting up voice on a relay, joining a call from the terminal, letting a browser or a buzz client into the same room, or working out why a join was refused.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's cli/huddle/*.go and cli/relay/huddle.go as of
writing. This skill is self-contained by design and won't see repo changes
automatically -- update by hand if flags/behavior change. -->

# ncli huddle

A huddle is a voice room hosted by the relay itself. The relay forwards
Opus frames between participants and **never decodes them**, so it links no
audio codec and stays pure Go.

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
  url: wss://relay.example      # required once huddle.enabled is true
  privkey: <hex>
huddle:
  enabled: true
  maxRooms: 100
  requireMembership: false      # true = only NIP-43 relay members may join
  allowedOrigins: []            # browser origins; empty allows any
  authTimeout: 5s
  pingInterval: 30s
  rtc: true                     # also mount /huddle/{id}/rtc
  iceServers:
    - urls: ["stun:stun.example:3478"]
    - urls: ["turn:turn.example:3478"]
      username: user
      credential: secret
```

`examples/relay/huddle.yaml` is a ready-made preset; `examples/relay/full.yaml`
documents every field.

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

## Joining from the terminal

```sh
ncli huddle join standup --relay wss://relay.example
ncli huddle join standup --relay ws://localhost:7777 --identity satoshi
ncli huddle join standup                       # --relay falls back to the first configured prefs relay
```

`--identity` takes the same shapes as `id sign` (vault label, nsec, npub, hex,
nprofile, nip-05) and must resolve to a **private** key -- joining means
signing a NIP-42 event. With no flag it falls back to `huddle.identity` /
`NCLI_HUDDLE_IDENTITY`, then to the vault's sole entry when there is exactly
one.

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
go build -tags huddleaudio ./cmd/ncli                  # macOS, Windows: still cgo-free
CGO_ENABLED=1 go build -tags huddleaudio ./cmd/ncli    # Linux, needs libasound2-dev
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

Consequently there is **no mute control** (there is no microphone to gate) and
**no raise-hand** (that publishes a NIP-53 kind 10312 with a `hand` tag, which
needs a relay connection the view is not given). Both arrive with the pieces
they depend on, rather than shipping now as buttons that do nothing.

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
