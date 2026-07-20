# ncli relay members/invites/roles — full reference

<!-- Mirrors ohstr/ncli's cli/relay/admin.go and cli/relay/service_membership.go,
and ohstr/nmilat's relay/membership_cache.go and relay/store_membership.go,
as of writing. Update by hand if these change. -->

NIP-43 relay-membership admin surface: enroll/remove members directly, issue
invite codes out-of-band, and define roles — all against an **already
running** relay, over the same NIP-98-authenticated HTTP mechanism as `ncli
relay stats`/`reindex`/`clear` (see `references/admin-reindex-reference.md`).
Requires `membership.enabled: true` in the relay's config (see
`examples/relay/membership.yaml`) — every endpoint below returns `501` if it
isn't.

Every write goes through the relay's live `MembershipService` (the same
instance every open connection consults for auth decisions), never a direct
store write — so a member added via `ncli relay members add` is visible to
already-connected clients immediately, not just after a relay restart.

## HTTP endpoints

All require a NIP-98 `Authorization` header signed with `nip11.privkey`,
verified against `nip11.pubkey`. All return `501` + a plain-text body if
`membership.enabled` is not `true` on the relay.

| Method | Path | Triggered by | Response |
|---|---|---|---|
| `GET` | `/admin/membership/members` | `ncli relay members list` | `200` + `{"members":[{"pubkey","roles","joined_at"}, ...]}` |
| `GET` | `/admin/membership/members/{pubkey}` | `ncli relay members show` | `200` + the member record, or `404` if not a member |
| `POST` | `/admin/membership/members` | `ncli relay members add` | body `{"pubkey","roles"}` → `200` + the resulting member record, or `400` for a malformed pubkey |
| `DELETE` | `/admin/membership/members/{pubkey}` | `ncli relay members remove` | `200` + `{"status":"removed"}` — idempotent, no error if not a member |
| `POST` | `/admin/membership/invites` | `ncli relay invites create` | body `{"ttl","max_uses","roles"}` → `200` + the issued invite claim (`code`, `expires_at`, `max_uses`, `roles`) |
| `GET` | `/admin/membership/invites` | `ncli relay invites list` | `200` + `{"invites":[...]}`, full (unredacted) codes |
| `DELETE` | `/admin/membership/invites/{code}` | `ncli relay invites revoke` | `200` + `{"status":"revoked"}` — idempotent |
| `GET` | `/admin/membership/roles` | `ncli relay roles list` | `200` + `{"roles":[{"id","label","description","color","order"}, ...]}` |
| `POST` | `/admin/membership/roles` | `ncli relay roles create` | body matching the same shape → `200` + the created role |

`members add`/`remove`, when the relay config has
`membership.publishAddRemoveEvents: true`, also publish a signed
kind:8000/8001 event — same advisory broadcast the self-service join/leave
flow makes, fire-and-log (a publish failure never fails the admin request
itself, since the authoritative membership state was already committed).

## Subcommands

```
ncli relay members list
ncli relay members show <pubkey>
ncli relay members add <pubkey> [--role <id>]...
ncli relay members remove <pubkey>

ncli relay invites create [--ttl <duration>] [--max-uses <n>] [--role <id>]...
ncli relay invites list
ncli relay invites revoke <code>

ncli relay roles list
ncli relay roles create <id> [--label <s>] [--description <s>] [--color 0-360] [--order <n>]
```

Same persistent `--json` flag as stats/reindex/clear: raw relay JSON on
stdout on success, `{"error","code","retryable","input"}` on stderr on
failure. `code` adds two cases beyond stats/reindex/clear's set:
`invalid_input` for a `400` (a malformed pubkey, an unparseable `--ttl`, a
`--color` outside 0-360, a missing role `id`) and `not_found` for a `404`
(`members show` on a pubkey that isn't a member). A `501` (membership not
configured on the relay at all) is `usage`, matching how a missing
`nip11.privkey` is classified locally — not `network`, since retrying
without an operator enabling `membership.enabled` will never succeed.

`members add` is the admin bypass of the self-service invite-then-join
flow — no invite claim required, useful for enrolling someone directly (a
known contact, a migration from another system).

`invites create --ttl` accepts a Go duration string (`24h`, `30m`); omitted
or `0` falls back to the relay's own configured `membership.inviteTTL`
default. `--max-uses 0` (the default) means unlimited uses. The issued code
is bearer secret material — anyone holding it can join — so `invites list`'s
human-output table shows only its first 8 characters; `--json` shows the
full code.

`roles create --color`/`--order` are true optional ints (0 is a valid
explicit value for both, distinct from "unset") — the flag is only sent if
you pass it explicitly (`cmd.Flags().Changed`), not merely non-zero.

## No `roles delete`

NIP-43 defines no "delete role" event. A role, once created, can only be
superseded — run `roles create` again with the same `id` and a different
`label`/`description`/`color`/`order`; the new kind:33534 event (addressable
by `(kind, pubkey, d-tag)`) replaces the old one. There is no cascading
effect on membership-list entries that already reference the old role id —
NIP-43 has no cascading-delete semantics, and this admin surface doesn't
invent any.

## Session enumeration/termination by owner — not implemented

`ncli relay sessions list --owner <pubkey>` / `sessions terminate --owner
<pubkey>` (killing a NIP-AA owner's already-open agent connections
immediately on membership revocation, rather than waiting for their next
connection attempt) requires relay-side owner-indexed session enumeration
that doesn't exist yet (`SessionHandler`'s session map is keyed by session
ID only). Deferred; see `NIP43_ADMIN_UX.md` in the ncli repo history for the
full design writeup.
