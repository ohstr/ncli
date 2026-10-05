---
name: ncli-groups
description: Create and manage NIP-29 relay-hosted groups with ncli groups -- create/edit/delete a group, invite/join/leave, add/remove members, replace the pinned-events list, delete a group's own event, and list/show groups a relay knows about. Use when setting up or administering a self-service group on a relay, writing a group-scoped moderation event, or reading a group's current metadata/admins/members.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's cli/groups/*.go as of writing. This skill is
self-contained by design and won't see repo changes automatically --
update by hand if flags/behavior change. -->

# ncli groups

Self-service NIP-29 group actions. Every write below is a plain signed
nostr event published to the group's relay over the normal websocket
transport -- the same one `ncli publish`/`ncli huddle` use. There is no
admin HTTP surface here; identity is the event's own signature, nothing
more. That is also why this is `ncli groups`, not `ncli relay groups`:
`ncli relay members/invites/roles` is the NIP-98-signed HTTP surface for
administering *someone else's* relay-wide access, a different trust model
entirely.

## Command surface

| Command | Kind | Notes |
|---|---|---|
| `groups create [group-id]` | 9007 | random id if omitted |
| `groups edit <group-id>` | 9002 | reads current metadata first, see below |
| `groups delete <group-id>` | 9008 | relay enforces admin-only |
| `groups invite <group-id>` | 9009 | random code if `--code` omitted |
| `groups join <group-id>` | 9021 | relay auto-grants on acceptance |
| `groups leave <group-id>` | 9022 | relay auto-removes |
| `groups members add <group-id> <pubkey> [role...]` | 9000 | |
| `groups members remove <group-id> <pubkey>` | 9001 | |
| `groups pins set <group-id> [--event...] [--address...]` | 9010 | whole-list replace |
| `groups delete-event <group-id> <event-id>` | 9005 | |
| `groups list` | reads 39000 | every group the relay has metadata for |
| `groups show <group-id>` | reads 39000/39001/39002 | one group's full detail |

`--relay` (falls back to the first configured prefs relay) and `--identity`
(vault label/nsec/npub/hex/nprofile/nip-05, falling back to `groups.identity`
/ `NCLI_GROUPS_IDENTITY`, then the vault's sole entry when there is exactly
one) are persistent flags on `groups` itself, inherited by every
subcommand. Every write requires `--identity` (signing); `list`/`show`
instead treat it as a bonus -- given, it authenticates (NIP-42) so a
member can see their own private group; omitted, the read stays
anonymous (see "Private groups" below).

```sh
ncli groups create standup
ncli groups edit standup --name "Standup" --about "Daily sync" --public --open
ncli groups invite standup
ncli groups join standup --invite-code <code>
ncli groups members add standup <pubkey> admin
ncli groups list --relay wss://relay.example
ncli groups show standup
ncli groups show standup --identity mykey   # authenticated, if standup is private
```

Output matches `ncli publish`'s own shape: a `published <id> to <relay>`
line (or `failed ... : <reason>`) in text mode, the full `PublishReport` as
structured JSON under `--json`. `list`/`show` instead print a table/detail
view in text mode and a plain JSON object under `--json`.

## The `previous` tag, and why these are commands and not YAML

NIP-29 wants a `previous` tag on every group write: up to 3 references
(each the first 8 hex characters of a recent event id in that group) so a
write can't be replayed out of context. None of nmilat's `nip29.NewXxx`
event constructors add it -- the spec treats it as "advice a relay may
apply", not something the constructor enforces -- so every `ncli groups`
write command queries the group's own recent timeline (`--tag h=<group-id>`,
limit 3, newest first) and attaches it before signing. This needs a relay
round trip no hand-written YAML + `ncli id sign` + `ncli publish` could do,
which is the whole reason this is a dedicated command surface.

A query failure here (relay unreachable) fails the write outright --
there was no point attempting to publish to a relay the previous-tag
lookup couldn't even reach. A query that reaches the relay but finds no
prior events (a brand-new group, right after `create`) is **not** an
error: the event is just signed with no `previous` tag, which is correct
for a group with no history yet.

## `groups edit` reads before it writes -- on purpose

kind:9002 edit-metadata is a **full replace**, not a patch: the relay's
mirrored kind:39000 ends up with exactly what the 9002 event's tags say,
nothing carried over from before. A naive `ncli groups edit standup --name
"Standup"` that built the event from flags alone would silently reset
`--private`/`--closed`/`--about`/everything else back to
blank/public/open, because those flags were never passed.

To avoid that, `edit` queries the group's current kind:39000 first
(`nip29.ParseGroupMetadata`), then layers only the flags actually passed
(`cmd.Flags().Changed(...)`, not just non-zero-value) onto that baseline
before building the 9002 event. A flag you didn't pass keeps whatever the
group already had; a flag you did pass overrides it. This means:

- `--private`/`--public` and `--closed`/`--open` are mutually exclusive
  pairs (`cmd.MarkFlagsMutuallyExclusive`) -- passing neither of a pair
  leaves that setting exactly as it was, passing one sets it explicitly.
- If the relay has no kind:39000 for this group yet (brand new, or a relay
  that hasn't mirrored one), edit starts from a blank slate -- every
  unset flag ends up false/empty, same as `create` + `edit` in one step
  would.
- A malformed existing kind:39000 (fails to parse) is treated the same as
  "no metadata yet" rather than blocking the edit -- there needs to be a
  way to fix a broken group, not just inspect it.

## `groups pins set` is a whole-list replace too

Same shape, no read-before-write mitigation: `--event`/`--address` name
the **complete** desired pin list. Passing neither clears every pin.
There is no "pin one more" operation -- the underlying kind:9010 event
always carries the full list, so build it with every id you want pinned,
every time.

## Write/read split, and the NIP-42 dependency

Every write command (`create`/`edit`/`delete`/`invite`/`join`/`leave`/
`members`/`pins`/`delete-event`) is a plain signed publish, identified by
the event's own signature -- NIP-42 relay auth is not required to write,
and these work today against any relay that speaks NIP-29, independent of
ncli's own dependency state.

`list`/`show` both accept `--identity` now, as a bonus: given, the
connection authenticates (NIP-42); omitted, the read stays anonymous, as
before `--identity` was accepted here at all. `show` always names one
specific group (a "d" tag), which is what the relay's NIP-29 visibility
gate keys off: a REQ naming a *private* group is rejected unless the
session has an authenticated identity that is also a member of that
exact group, so an authenticated member now sees their own private
group where an anonymous read gets nothing. Since a group defaults to
**private and closed** on creation, `ncli groups show standup` against
your own freshly-created group needs `--identity` to see anything at
all. `list` asks for every group's metadata by kind alone, naming no
specific group -- see nmilat's own NIP-29 implementation notes for
exactly how a kind-only query's visibility is scoped.

Either way, if the result comes back empty specifically because the
relay refused the query (no identity against a private group, or an
identity that isn't a member) rather than because there's genuinely
nothing there, `list`/`show` exit `7` (`auth`) with `"relay restricted
this query (private/membership required)"` instead of the usual
"(nothing found)" success -- same shape `find`/`dump`'s own
`--auth-identity` uses.

## Validation

`members add`/`remove` and `delete-event` validate their pubkey/event-id
argument shape (`utils.Validate32Key`) before building anything, the same
check `ncli relay members` uses -- a typo'd id fails immediately as
`invalid_input`, not as a relay round trip that comes back confusing.

## Interop

Everything here speaks plain NIP-29 wire format to whatever relay
`--relay` names -- there is nothing ncli-specific about the events these
commands produce, and nothing about these commands requires the target
relay to be `ncli relay` itself. Running `ncli relay` as the group's host
needs nmilat's own NIP-29 relay-side support (membership enforcement,
metadata mirroring, visibility gating); check `ncli version`'s dependency
info against the nmilat release notes if group behavior on your own
`ncli relay` instance looks incomplete.
