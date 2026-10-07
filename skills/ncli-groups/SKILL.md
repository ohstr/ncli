---
name: ncli-groups
description: Create and manage NIP-29 relay-hosted groups with ncli groups -- create/edit/delete a group, invite/join/leave, add/remove members, replace the pinned-events list, delete a group's own event, organize/browse a NIP-29 Subgroups hierarchy, and list/show groups a relay knows about. Use when setting up or administering a self-service group on a relay, writing a group-scoped moderation event, linking groups into a parent/child hierarchy, or reading a group's current metadata/admins/members.
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
| `groups create [group-id] [--parent <id>]` | 9007 (+9002 if `--parent`) | random id if omitted |
| `groups edit <group-id> [--parent <id>]` | 9002 | reads current metadata first, see below |
| `groups delete <group-id>` | 9008 | relay enforces admin-only; cascades any children to root, see "Subgroups" |
| `groups invite <group-id>` | 9009 | random code if `--code` omitted |
| `groups join <group-id>` | 9021 | relay auto-grants on acceptance |
| `groups leave <group-id>` | 9022 | relay auto-removes |
| `groups members add <group-id> <pubkey> [role...]` | 9000 | |
| `groups members remove <group-id> <pubkey>` | 9001 | |
| `groups pins set <group-id> [--event...] [--address...]` | 9010 | whole-list replace |
| `groups delete-event <group-id> <event-id>` | 9005 | |
| `groups list [--mine\|--member <pubkey>]` | reads 39000 (+39002 when scoped) | every group the relay has metadata for, or just the ones a pubkey belongs to |
| `groups show <group-id>` | reads 39000/39001/39002 | one group's full detail |
| `groups tree` | reads 39000 | every visible group assembled into a NIP-29 Subgroups hierarchy, see below |

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
ncli groups list --identity mykey --mine     # only groups mykey belongs to
ncli groups list --member <pubkey>           # only groups that pubkey belongs to
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
- That current-metadata read authenticates as `--identity` (NIP-42), the
  same identity the edit itself signs with. It has to: a fresh group
  defaults to **private and closed**, and an unauthenticated read of a
  private group's kind:39000 is always denied -- reading it unauthenticated
  would look exactly like "no metadata yet" and silently reset
  `Private`/`Closed`/`Parent`/`Children` to blank on an edit that never
  meant to touch any of them. So a read the relay actually restricts (the
  `--identity` given isn't a member, or the group doesn't exist) fails the
  whole edit with the same exit `7`/`"relay restricted this query"` shape
  `show` uses, rather than quietly falling back to a blank slate.

## Posting a message into a group

There's no `groups post`: a group message is an ordinary event carrying
the group's `h` tag (kind 9 for chat), signed and published like any
other. The relay accepts it only from a member (closed or private group);
it checks the signed author, so `publish` needs no `--identity`.

```sh
PUB=$(ncli id mykey --json | jq -r .pub_hex)
jq -n --arg p "$PUB" '[{kind:9, pubkey:$p, created_at:(now|floor),
  content:"hello group", tags:[["h","standup"]]}]' > msg.json
ncli id sign -e msg.json -o signed.json --identity mykey
ncli publish -e signed.json -s wss://relay.example --json   # .results[0].id is the event id
```

That id is what `groups pins set --event` and `groups delete-event` take.

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
connection authenticates (NIP-42); omitted, the read stays anonymous.
The relay's NIP-29 visibility gate sits at a different level for each,
so they fail differently:

- `show` names one group (a "d" tag) -- the gate keys off exactly that
  tag, rejecting a REQ for a *private* group outright (a "restricted:
  ..." CLOSED) unless the session is authenticated as a member. A fresh
  group defaults to **private and closed**, so `ncli groups show standup`
  needs `--identity` to see anything. Missing or non-member: `show` exits
  `7` (`auth`), `"relay restricted this query (private/membership
  required)"`, not the usual `(nothing found)` -- same shape
  `find`/`dump`'s `--auth-identity` uses. **The same exit `7` now also
  fires for a group that doesn't exist at all**, not just a private one
  you can't access -- the relay deliberately makes "private, no access"
  and "doesn't exist" indistinguishable, closing an id-enumeration
  oracle an unauthenticated prober could otherwise use to brute-force
  which group ids exist. `ncli groups show <typo'd-id>` exits `7`, not
  `(nothing found)`.
- `list` names no group (`{"kinds":[39000]}`, plus a kind:39002 lookup
  for `--mine`/`--member`), so that gate never fires. Visibility is
  enforced per result instead: the relay silently drops any private
  group's metadata the session isn't a member of. There's no refusal to
  report, so `list` never exits `7` for this -- an empty/filtered result
  is always an ordinary `(no groups found -- ...)` success.

## Subgroups

NIP-29 groups MAY be organized hierarchically (a group's `parent` tag
names another group's id). `--parent` on `create`/`edit` sets it;
`groups tree` renders the whole hierarchy.

```sh
ncli groups create standup-notes --parent standup   # create, then link in one call
ncli groups edit standup-notes --parent standup      # link an existing group
ncli groups edit standup-notes --parent ""           # detach to root
ncli groups tree                                     # render the hierarchy
ncli groups tree --identity mykey --json             # same, scripted
```

`create --parent` is sugar for two events (9007, then 9002) -- under
`--json` they're combined into one `{"create": ..., "set_parent": ...}`
value, never two concatenated JSON blobs, so a scripted caller still
gets exactly one well-formed result to parse.

The relay enforces every NIP-29 `MUST` here, each a distinct `restricted`
rejection: naming yourself as your own parent, naming a parent that
doesn't exist, a parent assignment that would close a cycle, and -- new
beyond the letter of the spec -- the submitter must also be an admin of
the *new* parent (not just of the group being edited), and both groups
must share the same `--private`/`--public` setting. That last one isn't
a NIP-29 requirement; it's because a group's mirrored kind:39000 is
signed once and cached, so its tags can't be redacted per viewer -- a
public parent's `child` tag naming a private group would permanently
leak that group's id to everyone who can see the public one, regardless
of their own membership.

A subgroup's own membership is entirely independent of its parent's:
joining the parent grants no access to the child and vice versa, by
spec design, and `ncli groups show`/`members` on a subgroup behaves
exactly as it would for any standalone group.

**Deleting a parent (`groups delete`) cascades**: every remaining child
automatically becomes a root, not an orphaned dangling reference.

kind:9002 is a full replace, and the relay rejects any edit that omits or
adds to a group's current children (there's no `--child` flag to name
them explicitly). `groups edit` handles this the same way it handles
`--private`/`--closed` -- see "`groups edit` reads before it writes"
above: it reads the group's current `Children` (authenticated as
`--identity`) and carries that list forward unconditionally, so renaming
a group with subgroups, or any edit that doesn't mention `--parent`,
doesn't drop a single one.

A relay that hosts NIP-29 groups at all advertises
`{"nip29":{"subgroups":true}}` in its NIP-11 document.

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
