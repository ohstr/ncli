# ncli relay — default limits: risks & decisions

2026-11-08 · decided; this is the reference for the relay-hardening work

## Summary

Agreed rule: **a limit nobody configures is no limit**. No hidden defaults, no `-1`.
One exception: **Blossom keeps its upload caps** (100 MiB per blob, 2 uploads in
flight per key, 8 in total).

This report lists what a relay with no limit settings is exposed to, so the
team can decide whether "unset = none" is right everywhere, or whether some
areas need a default, a warning, or just a documented recommended value.

## What is already limited with nothing configured

| Limit | Value when unset | Where |
|---|---|---|
| Blossom blob size | 100 MiB | nmilat `nipB7/server` (kept) |
| Blossom uploads in flight per key / total | 2 / 8 | same (kept) |
| WebSocket message size (`max_message_length`) | 1,101,005 bytes | nmilat `relay/session.go:35` |
| Subscriptions per connection (`max_subscriptions`) | 355 | nmilat `relay/subscription.go:13` |
| Events per filter (`max_limit`) | 10,000,000 (effectively none) | nmilat `relay/store.go:64` |
| Store tasks in flight per connection | 2048 (internal, not configurable) | nmilat `relay/config.go:23` |
| Concurrent store scans | 2 × workers (internal) | nmilat `relay/config.go:500` |
| Proof-of-work | off unless `pow.min` set | relay config |

**Not limited at all today:** events per pubkey/IP over time, REQ/COUNT rate,
connections per IP, HTTP bridge requests (`/query`, `/count`, `/events`),
Blossom downloads and request rate, storage per key or in total, listen
interface (always all interfaces).

## Risks of each unlimited-by-default area

| # | Area | What an abuser can do | Severity (public relay) | Severity (members-only relay) |
|---|---|---|---|---|
| 1 | Event writes per pubkey / IP | Flood events: storage grows, indexes (search, zaps, stats) rebuild slower, spam reaches readers. rc.49 test: 300 events in one burst, all accepted. | **High** | Low (only members can write) |
| 2 | REQ / COUNT rate | Hammer queries; scans are capped at 2×workers so honest queries queue behind them (latency DoS). `max_limit` 10M allows huge result sets. | **High** | Medium (auth_required still lets members do it) |
| 3 | Connections per IP | Open many sockets × 355 subs each: file descriptors and memory run out. | Medium | Medium (sockets open before auth) |
| 4 | HTTP bridge (`/query`, `/count`, `/events`) | Same as REQ but stateless and cheaper for the attacker; NIP-98 only when membership is required. | Medium | Low |
| 5 | Blossom requests over time | Uploads capped only by concurrency, so a key can upload back-to-back forever; any number of keys if upload is open. Downloads unlimited: egress bandwidth / S3 egress cost. | **High** if uploads open to anyone | Low |
| 6 | Storage quota (events + blobs) | No per-key or total cap: disk / bucket fills, relay stops accepting writes or crashes. | **High** | Medium |
| 7 | Listen address | Binds every interface; a relay meant for loopback is exposed unless a firewall is in front. Fixed by the planned `listen:` key. | Medium | Medium |

**Mitigations available today without new code:** `pow.min`,
`nip11.limitation.auth_required` / `membership_required`,
`blossom.membersOnly.upload`, NIP-86 bans, and a reverse proxy
(nginx `limit_req`, Traefik `RateLimit`) in front of the relay.

## Options

- **A. Unset = none (current agreement).** Predictable, no surprise on upgrade;
  a public relay is open to items 1–6 until the operator sets limits.
- **B. A + recommended values in docs and examples.** `full.yaml` and the
  relay-ops skill ship a commented `limits:` block with suggested numbers;
  still nothing applied unless copied in.
- **C. A + a `--check` / startup warning.** When the relay is public
  (no `auth_required`) and no `limits:` is set, warn: "no rate limits
  configured". No behaviour change, but the risk is visible.
- **D. Defaults on.** Generous limits applied when unset. Safest out of the
  box; contradicts the agreed rule and changes behaviour for existing relays.

**Chosen:** A + B + C. Keeps the agreed rule, but no operator ends up
unlimited without being told. Blossom's three caps stay as the only defaults.

## Decisions

- [x] **Unset = none** for event, REQ/COUNT, HTTP bridge and Blossom request-rate limits (items 1, 2, 4, 5). Only Blossom's three upload caps keep defaults.
- [x] **Connections per IP** (item 3): added to the `limits:` block, opt-in like every other limit.
- [ ] **Storage quotas** (item 6): deferred to a separate item; needs per-key accounting in the nmilat store and Blossom.
- [x] **Option B**: recommended values as a commented `limits:` block in `examples/relay/full.yaml` and the relay-ops skill.
- [x] **Option C**: `--check` and startup warn when the relay is public (no `auth_required`) and no `limits:` is set.
- [x] **nmilat internal caps** (2048 store tasks per connection, scans 2 × workers): kept as internal backpressure guards, not exposed in `relay.yaml`.

## Planned work

| PR | Repo | Content |
|---|---|---|
| 1 | ncli | `listen:` key (wins over `port`) |
| 2 | ncli | `--check` fails on unknown config keys, startup warns |
| 3 | ncli | Pin MinIO / mc images in the Helm chart |
| 4 | ncli | `membership.memberClaims` follows `membership_required` |
| 5 | nmilat | Rate limiter: per pubkey + per IP, EVENT, REQ/COUNT, HTTP bridge, Blossom requests, connections per IP; each limit applied only when set |
| 6 | ncli | `limits:` block wired to it, recommended values in docs (B), public-relay warning (C) |
| 7 | nmilat + ncli | Blossom test gaps: overall upload cap, banned key, MinIO in CI, climatrix e2e on MinIO |
