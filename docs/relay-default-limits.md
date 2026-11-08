# ncli relay — limits: what shipped and what's open

2026-11-08 · record after v0.8.0-rc.51 (decided in [#233](https://github.com/ohstr/ncli/pull/233))

## Rule

**A limit nobody configures is no limit.** No hidden defaults, no `-1`. The
exceptions are the protocol-level caps below, which always apply. A public
relay that limits nothing is warned about, never refused.

## Limited with nothing configured

| Limit | Value when unset | Config key |
|---|---|---|
| WebSocket message size | 1,101,005 bytes | `nip11.limitation.max_message_length` |
| Subscriptions per connection | 355 | `nip11.limitation.max_subscriptions` |
| Events per filter | 555,555 (ncli's default; nmilat alone would allow 10,000,000) | `nip11.limitation.max_limit` |
| Indexable tags per filter | 5 | `nip11.limitation.max_indexable_tags` |
| Store tasks in flight per connection | 2048 | `maxConcurrentStoreTasks` |
| Concurrent store scans | 2 × CPUs | internal |
| Blossom blob size | 100 MiB | `blossom.maxBlobBytes` |
| Blossom uploads in flight per key / total | 2 / 8 | `blossom.maxUploadsPerSigner` / `blossom.maxConcurrentUploads` |
| Proof-of-work | off | `pow.min`, `pow.strict` |

## Opt-in limits (`limits:`, rc.51)

Each rate is `events` per `per`, up to `burst`. The recommended block for a
public relay is in `skills/ncli-relay-ops/SKILL.md` and `examples/relay/full.yaml`,
and the public examples and the public-search stack ship with it.

| Key | Counts |
|---|---|
| `limits.events.perPubkey` / `perIP` | EVENT over the WebSocket and `POST /events`, one shared budget. Per pubkey only for a connection logged in as the author, and always over `POST /events` (its NIP-98 signer is the author). |
| `limits.reads.perIP` | REQ, COUNT, NEG-OPEN and each AUTH |
| `limits.http.perPubkey` / `perIP` | `POST /query`, `/count`, `/events` and NIP-86 (from rc.52; NIP-86 never counts its admins) |
| `limits.blossom.uploads` / `downloads` | Blossom requests per pubkey and per IP (a download has no signer, so per IP) |
| `limits.connectionsPerIP` | WebSockets, huddle ones (`/huddle/{id}/audio`, `/rtc`) included |
| `limits.exempt: members` | Members and admins skip the per-pubkey limits; never a banned key, never per-IP ones |
| `limits.trustedProxies` | Proxies whose `X-Forwarded-For` names the client |

Refusals are `rate-limited:` (OK, CLOSED, NEG-ERR) or 429 with `Retry-After`,
counted in `ncli_relay_rate_limited_total`.

**Warnings** (startup and `relay --check --json`'s `"warnings"`): a public
relay (no `auth_required`) without `limits:`; a per-pubkey limit that never
applies; `listen` on loopback with per-IP limits but no `trustedProxies`
covering it. While running, with per-IP limits set, the relay also warns
(once per peer) about a request carrying `X-Forwarded-For` or `X-Real-IP`
from a peer `trustedProxies` doesn't list.

## Shipped in rc.51

| PR | Content |
|---|---|
| [#234](https://github.com/ohstr/ncli/pull/234) | `membership.memberClaims` follows `membership_required` when unset |
| [#235](https://github.com/ohstr/ncli/pull/235) | Chart and stacks pin every image (MinIO by digest, the relay by release) |
| [#236](https://github.com/ohstr/ncli/pull/236) | `--check` fails on unknown config keys; startup warns |
| [#237](https://github.com/ohstr/ncli/pull/237) | `listen` (host:port), winning over `port` |
| [#238](https://github.com/ohstr/ncli/pull/238) | Blossom on the relay end to end, MinIO included |
| [#241](https://github.com/ohstr/ncli/pull/241) | The `limits:` block and the no-limits warning |
| [#243](https://github.com/ohstr/ncli/pull/243) (in [#246](https://github.com/ohstr/ncli/pull/246)) | Roles with tasks (`membership.roles`, `groups.create`); an invite request (kind 28935) needs `createclaim` |
| [#244](https://github.com/ohstr/ncli/pull/244) (in [#246](https://github.com/ohstr/ncli/pull/246)) | Blossom blobs at the root; missing endpoints and 3xx/405/501 are `unsupported` |
| [#245](https://github.com/ohstr/ncli/pull/245) | Every config warning in `--check --json`; the loopback/`trustedProxies` warning |
| [#246](https://github.com/ohstr/ncli/pull/246) | `POST /events` applies NIP-29 group rules; Blossom blobs get `nosniff`, a sandboxing CSP and file extensions |

rc.51's chart and stacks still ran the rc.50 image, which ignores every key
above. From rc.52 a release ships pinned to itself, and the public configs
carry `limits:` ([#248](https://github.com/ohstr/ncli/issues/248)).

## Decisions

- Unset is none for event, read, HTTP bridge, Blossom request and connection limits.
- The protocol-level caps above keep their defaults.
- Recommended values are documented and shipped in the public examples, not applied.
- `--check` and startup warn instead of refusing.
- nmilat's scan cap stays internal backpressure; `maxConcurrentStoreTasks` stays a tuning key.

## Still open

| Item | Status |
|---|---|
| Storage quotas, per key and in total (events and blobs) | Not started: needs per-key accounting in the nmilat store and Blossom |
| NIP-86 requests counted against `limits.http` | Fixed in rc.52 ([#250](https://github.com/ohstr/ncli/pull/250)) |
| NIP-86 IP block covering every HTTP bridge request | Fixed in rc.52 ([#250](https://github.com/ohstr/ncli/pull/250)) |
| A banned pubkey joining calls when `huddle.requireMembership` is off | Fixed in rc.52 ([#250](https://github.com/ohstr/ncli/pull/250)) |
