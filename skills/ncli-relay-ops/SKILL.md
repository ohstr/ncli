---
name: ncli-relay-ops
description: Configure, run, and operate a live ncli Nostr relay — author relay.yaml (NIP-11, auth, search, cache, NIP-43 membership), start the server, and monitor/administer it remotely over NIP-98 (`ncli relay stats`/`reindex`/`clear`, and `ncli relay members`/`invites`/`roles` for NIP-43 membership admin), including rebuilding search/zap indexes on a running relay (`ncli relay reindex`) and switching between named relay configs without repeating `--config` (`ncli relay context`). Use when writing a relay config file, deploying/operating an ncli relay, managing NIP-43 relay membership/invite-codes/roles, troubleshooting search/zap index state, or juggling multiple relay targets.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's examples/relay/*.yaml, cli/relay/command.go,
cli/relay/admin.go, cli/relay/service_membership.go, and
cli/reindex/command.go as of writing. This skill is self-contained by
design and won't see repo changes automatically — update by hand if
flags/schemas change. -->

# ncli relay / relay stats, reindex, clear

One config file (`--config relay.yaml`), read by both `ncli relay` and
`ncli relay stats`/`reindex`/`clear` — the latter need `port` +
`nip11.privkey` from it to authenticate over HTTP.

## Minimal config to start

```yaml
port: 5500

nip11:
  name: ncli-dev-relay
  pubkey: 9b14dff44fb8d74e2b90d5ae1501b935e073a5245749458a3e261021646f4e11
  contact: admin@example.com

store: ./data/db/notes.db
logdir: ./data/logs
```

`pubkey` alone is enough to start; `privkey` is only required if you also
want `cache.topZapped.enabled` or `ncli relay stats`/`reindex`/`clear`/`ncli id
delegate`'s relay-signing side.

## Preset shapes

| Preset | What it toggles vs. minimal |
|---|---|
| open | Adds `cache.search.*` (Meilisearch) enabled |
| auth | Adds `cache.search.*` **and** `nip11.limitation.auth_required: true` **and** `nip11.url` (required once auth is on) |
| ephemeral | Same shape as open, distinct `store`/`port`/`index_name` for a throwaway instance. Nothing ephemeral-specific to configure -- NIP-16 handling (kinds 20000-29999) is automatic based on kind, not a toggle. |
| cache-search | Adds `cache.search.*` **and** `cache.topZapped.enabled: true` **and** `nip11.privkey` — the shape you need for `ncli relay stats`/`reindex`/`clear` too, since their auth also requires `nip11.privkey` |
| membership | Adds `membership.enabled: true` **and** `nip11.privkey` **and** `nip11.limitation.auth_required: true` + `nip11.url` (auth is a prerequisite for membership) **and** (optionally) `nip11.limitation.membership_required: true` — NIP-43 relay membership: enrolled pubkeys can REQ/EVENT, everyone else must invite-then-join first. See `examples/relay/membership.yaml`. |
| pow | Adds `pow.strict: true` **and** `pow.min` (a nonzero difficulty) — actually rejects under-difficulty events instead of just advertising the requirement. See `examples/relay/pow.yaml`. |
| full | Documents every field: `description`, `limitation.*`, `pow.*`, `cache.topZapped.enabled`, `cache.topZapped.window`, `membership.*`, `agent_auth.*`, `handshakeTimeout`/`pingInterval`/`pongTimeout`/`writeTimeout`, `outgoingBufferSize`/`maxConcurrentStoreTasks`/`verificationWorkers`, `logs.*` rotation, full `cache.search.*` |

Full field table: `references/config-schema.md`.

## Running the relay

```sh
ncli relay --config relay.yaml
```

Foreground process; stops cleanly on SIGINT/SIGTERM. Logs go to `logdir`
(or a custom `logs:` rotation block).

## `ncli relay context` — stop repeating `--config`

Operating more than one relay means repeating `--config path/to/relay.yaml`
on every admin call. Save each relay's config under a name instead:

```sh
ncli relay context add bee_community ~/relays/bee-community.yaml
ncli relay context use bee_community  # every relay command now targets bee_community

ncli relay context                    # list saved contexts, "*" = current
ncli relay context remove bee_community
```

Precedence when `--config` is omitted: a `ncli.yaml`/`relay.yaml` in the
current directory still wins (unchanged from before contexts existed); the
current context is consulted only when neither is present, ahead of the
`$HOME` fallback. Every example below that omits `--config` relies on one
of these three -- cwd discovery, a context, or `$HOME/ncli.yaml` -- already
resolving to the right relay.

## `ncli relay members`/`invites`/`roles` — NIP-43 membership admin, relay must be running

Same NIP-98-over-HTTP mechanism as `stats`/`reindex`/`clear` below, gated on
`membership.enabled: true` in the relay's own config (see the `membership`
preset). Manage enrolled members, issue/revoke invite codes out-of-band, and
define roles — all against the relay's live `MembershipService`, so changes
take effect for already-connected clients immediately.

```sh
ncli relay members list                              # every enrolled member
ncli relay members show <pubkey>                      # one member's record (404 if not a member)
ncli relay members add <pubkey> --role vip             # admin bypass: enroll directly, no invite code
ncli relay members remove <pubkey>                     # idempotent — no error if already not a member

ncli relay invites create --ttl 24h --max-uses 1        # issue a code to hand out out-of-band
ncli relay invites list                                 # every currently-stored code
ncli relay invites revoke <code>                        # idempotent

ncli relay roles list
ncli relay roles create <id> --label "VIP" --color 280   # NIP-43 has no "delete role" — see reference doc
```

Same `--json`/error-classification convention as stats/reindex/clear, plus
`invalid_input` (`400` — a malformed pubkey, an unparseable `--ttl`, a
missing role `id`, `--color` outside 0-360), `not_found` (`404` — `members
show` on a non-member), and `usage` (`501` — membership isn't enabled on the
relay at all; not `network`, since retrying won't fix a config problem).

Full endpoint list and subcommand reference: `references/membership-admin-reference.md`.

## `ncli relay stats`/`reindex`/`clear` — relay must be running

NIP-98-signed HTTP requests to `http://localhost:<port>` on the *same*
relay this config describes. Requires `nip11.privkey` in config to sign
requests as admin.

```sh
ncli relay stats                      # live reindexer + verification-worker metrics
ncli relay reindex search             # trigger a live search reindex (202 Accepted, runs async)
ncli relay reindex zaps               # trigger a live zap-stats reindex
ncli relay clear search               # delete the search index on the live relay
ncli relay clear zaps                 # delete zap counters on the live relay
```

All subcommands take `--json` (a global flag) for scripted use: the
relay's raw JSON response on stdout on success, or `{"error", "code",
"retryable", "input"}` on stderr on failure -- never both, and never on
stdout. `code` is classified from *how* the request failed: `network` if
the request couldn't even reach the relay (connection refused/timeout, or
the relay itself returned `5xx` -- both `retryable: true`), `auth` for a
`401`/`403` (bad or mismatched NIP-98 signature -- check `nip11.privkey`),
`conflict` for a `409` (the exact case below: a reindex of that kind is
already running -- `retryable: true`, safe to retry once it finishes), or
`usage` if `nip11.privkey` is missing from `--config` entirely (nothing to
sign the request with). `reindex` calls return immediately (`202`) with
the job started in a background goroutine inside the relay process — poll
`ncli relay stats` for progress. If a reindex of that kind is already
running, the endpoint returns `409 Conflict` instead of starting a second
one.

This is the only way to trigger a search/zap reindex — there is no offline
CLI equivalent. `ncli relay reindex {search,zaps}` runs a background
goroutine inside the relay process (`reindex.ExecuteSearchReindex`/
`ExecuteZapReindex`) that walks every event already in `store` and rebuilds
the index from scratch. That means `nip11.privkey` must be configured
whenever you might need to rebuild an index, not just for
`cache.topZapped.enabled`.

Full endpoint list and subcommand reference: `references/admin-reindex-reference.md`.

## Gotchas learned

- `ncli relay reindex`/`clear`/`members`/`invites`/`roles` invoked with no
  further subcommand (or a misspelled one) is a `code: "usage"` error
  (exit 2), same as any other invocation mistake -- not a silent help dump
  with exit 0.
- Every startup failure now goes through the same reporting path (no
  bypass via `log.Fatal`, which used to skip the `--json`/structured-error
  path entirely for the two failures below). Config-validation problems
  are caught before the server binds and classified specifically: missing
  `nip11.pubkey`/`store`/`cache.topZapped.enabled`-without-`nip11.privkey`
  are `code: "usage"` (exit 2); a malformed `nip11.privkey` or a
  `nip11.pubkey`/derived-pubkey mismatch is `code: "invalid_input"` (exit
  3, the mismatch case echoing both pubkeys). Failures once the server is
  actually starting (event-store directory/open failure) fall back to
  `code: "internal"` (exit 1) -- classify these further yourself if you can
  tell from the message whether it's a permissions issue vs. the store
  already being held open by another running relay process.
- `cache.topZapped.enabled: true` without `nip11.privkey` set fails with
  `cache.topZapped.enabled requires nip11.privkey` — checked at
  config-load time, before the server binds.
- `cache.topZapped.window` uses ncli's own duration units
  (`h`/`mo`/`m`/`s`/`d`/`w` — same set as filter `since`/`until`), not Go's
  stdlib duration format used by `pingInterval`/`pongTimeout`/
  `writeTimeout`/etc. An empty or unparseable `cache.topZapped.window` is
  *not* a config-load-time error: it silently falls back to 24h with a
  logged warning, so a typo here won't be caught until you notice
  "top-zapped" queries covering the wrong range.
- `membership.enabled: true` without `nip11.privkey` set fails the same
  way (`membership.enabled requires nip11.privkey`) — the relay signs its
  own role/membership-list/add-remove-user/invite-response events with it.
  `nip11.limitation.membership_required: true` additionally requires both
  `membership.enabled: true` and `nip11.limitation.auth_required: true`;
  missing either is a config-load-time error, not a runtime surprise.
- Turning on `nip11.limitation.membership_required` is a real behavior
  change, not just a new knob: EVENT publishing now checks that the
  *specific pubkey that signed the event* is authenticated on the
  connection and holds membership — not merely "is anything authenticated
  on this connection," which is all `auth_required` alone ever checked.
  Join Requests (kind 28934) and Leave Requests (kind 28936) are exempt
  from this gate by definition (a non-member's whole point in sending one
  is to become a member).
- `agent_auth.enabled: true` without `nip11.limitation.membership_required`
  set fails with `agent_auth.enabled requires
  nip11.limitation.membership_required` — checked at config-load time.
  Enabling it is also a real behavior change beyond adding the option:
  once on, an AUTH from a non-member with no NIP-OA credential now fails
  AUTH itself (`OK false`), rather than succeeding AUTH and only failing
  later at the membership gate. An agent whose owner is later removed as
  a member loses access on its *next* connection attempt only — an
  already-open session is not forcibly terminated.
- `ncli relay context add` stores the config file's *absolute* path and
  requires the file to already exist -- it's a saved shortcut to a path,
  not a copy of the file. `context remove` only deletes the shortcut, never
  the underlying config file; removing the current context clears which
  one is "current" (falls back to cwd/`$HOME` discovery) rather than
  picking another one automatically.
- `limitation.max_message_length` left unset (`0`) does **not** mean "no
  limit" — the relay actually enforces `1,101,005` bytes and substitutes
  that value into the advertised NIP-11 doc instead of `0`, to avoid
  falsely advertising "accepts no messages." Same substitution pattern for
  `max_limit` (555,555), `max_subscriptions` (355), `max_indexable_tags`
  (5) when each is left at zero.
- `nip11.pubkey` is optional if `nip11.privkey` is set — it's derived
  automatically and must match if both are present (config-validation error
  otherwise). `ncli relay stats`/`reindex`/`clear` use the same derivation
  if `nip11.pubkey` is absent from config but `nip11.privkey` is present.
- `pow.min` is a difficulty *requirement*, not a discount: it never lets an
  event with insufficient PoW skip other checks, and it does nothing at all
  unless `pow.strict: true` — with `strict` unset/false, `min` only shows up
  in the relay's advertised `nip11.limitation.min_pow_difficulty` (a heads-up
  to clients), while every event is still accepted regardless of its actual
  difficulty. Setting `nip11.limitation.min_pow_difficulty` directly in YAML
  does nothing — `pow.min` is the only field that reaches it.
- `nip11.delegation` (the block `ncli id delegate` prints — see
  `ncli-identity`) is validated at server startup if present; an invalid
  token or issuer signature is a fatal error, not a soft warning.
- Both `store` and `logdir`'s parent directories are auto-created on `ncli
  relay` startup, but a missing `store` value is caught at config-load time
  (before either directory is touched), same as a missing `nip11.pubkey`.
