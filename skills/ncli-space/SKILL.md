---
name: ncli-space
description: Create, list, show, and join NIP-53 meeting spaces with ncli space -- publish a kind:30312 space, see which ones a relay knows about (with or without a live call), inspect one's full detail, or join it to watch the roster, chat, and hear the call. Use when publishing a space for others to find, browsing what spaces/calls a relay hosts, or joining a call from the terminal.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's cli/space/*.go and cli/huddle/*.go as of writing.
This skill is self-contained by design and won't see repo changes
automatically -- update by hand if flags/behavior change. -->

# ncli space

A NIP-53 meeting space (kind:30312) is a published, addressable record of
*where a meeting lives and who hosts it* — durable even when nobody is in
it, unlike the ephemeral huddle room it may point at. Its
`service`/`endpoint` tag names the meeting transport, and the spec
deliberately leaves that transport unspecified — a space can describe any
meeting technology, not just ncli's own.

`ncli space` is the generic front door onto all of this: create one, list
what a relay has, show one's detail, join one. **`join` is implemented in
`cli/huddle`** (it needs huddle's own resolution/dial/TUI code) but is
only ever reached as `ncli space join` — `ncli huddle` itself now covers
just the one thing that isn't about a space at all, listing the relay's
currently-occupied ephemeral rooms (`ncli huddle list`). Read
`skills/ncli-huddle/SKILL.md` for the join experience itself (roster,
chat, audio playback) and for setting up the relay side; this skill
covers the space side: publishing one and discovering what's out there.

## Command surface

| Command | Notes |
|---|---|
| `space create <id>` | Publishes kind:30312. `--service` defaults to `--relay` |
| `space list [--all]` | Every open space by default; `--all` adds closed/private |
| `space show <id>` | One space's full detail, every match if the id isn't unique |
| `space join [id\|naddr\|room]` | Implemented in `cli/huddle`, mounted only here. Omit the argument entirely when `--relay` has exactly one open space |

`--relay` (falls back to the first configured prefs relay) and `--identity`
(vault label/nsec/npub/hex/nprofile/nip-05, falling back to `space.identity`
/ `NCLI_SPACE_IDENTITY`, then the vault's sole entry when there is exactly
one) are persistent flags on `space` itself, inherited by every
subcommand — `create` needs identity to sign with, `join` to authenticate
the NIP-42 challenge; `list`/`show` are anonymous reads and ignore it.

```sh
ncli space create standup --summary "Daily sync" --hashtag standup
ncli space list --relay wss://relay.example
ncli space show standup
ncli space join standup
ncli space join --relay wss://relay.example   # joins the one open space, if there's exactly one
```

## "Voice is an option, not the point" — what `create` actually does

Publishing a space means filling in every field `nip53.ParseMeetingSpace`
requires to be parseable at all — confirmed by reading the parser
(`nip53.go:548-568`): `d`, `room`, `status`, `service`, and at least one
`Host`-role provider. Missing any of these makes the space invisible to
`space list`/`show` and to any other NIP-53 client, so `create` always
fills all of them, not just the ones a flag named:

- `d` = the id you passed.
- `room` = the same id. ncli's own join-resolution convention (see
  `transportFromSpace` in `cli/huddle/space_resolve.go`) is that `service`
  plus the space's own `d` tag together resolve to a huddle room id — the
  generic `room` tag NIP-53 itself defines isn't what ncli reads for
  dialing, so there's nothing meaningful to set it to independently.
- `status` = `"open"`.
- `service` **defaults to `--relay`**. This is the entire mechanism by
  which "voice is enabled": a space whose `service` is a relay running
  `ncli relay` with huddles on can be mapped straight onto
  `/huddle/<id>/audio` by `transportFromSpace`, so `space join` can dial
  it. Pass `--service`/`--endpoint` explicitly to describe a space
  pointing at some other meeting technology instead — still a completely
  valid NIP-53 space, just not one `ncli` itself can join.
- `id` doubles as that huddle room id, so it's rejected up front (same
  `/`, `?`, `#`, `%` restriction `space join` itself enforces) rather than
  publishing successfully and only failing, confusingly, at join time.
  Put a human-readable name in `--summary` instead, which has no such
  restriction.
- The signing identity's own pubkey becomes the space's `Host`.

## `list` shows every open space, not just ones you could join right now

Reads kind:30312 + kind:30313 off a relay and shows every space whose
`status` is `open` by default (`--all` widens that to closed/private
too), each row's `LIVE` column saying whether it currently has a live
kind:30313 session — a space is the durable thing worth seeing whether or
not a call is happening in it right now, so one with no live session is
still listed, not filtered out. A session claiming `live` whose event
hasn't been refreshed within `--stale-after` (default 1h) is treated as
ended, per the spec's allowance that clients may read an un-refreshed
live event as abandoned.

## `show` and non-unique ids

A kind:30312's `d` tag is only unique per-pubkey — two different hosts can
both use `standup` as their identifier. `space show <id>` queries by `d`
tag alone (no pubkey filter), so if more than one space matches, every one
is printed, not just the first. Pass a full `30312:<pubkey>:<d>` coordinate
or an `naddr` to `space join` (not `show`, which has no such form yet) to
disambiguate when dialing a specific one matters.

## `join`'s dial/TUI logic is not reimplemented here

`cli/space`'s `join` is a thin wrapper around `huddle.NewJoinCommand()`
(`cli/space/join.go`): it widens `Args` to accept zero args (huddle's own
version always required exactly one) and wraps `RunE` to fill in an id
when none was given, but once an id exists -- typed or resolved -- it
hands off to huddle's own unmodified `RunE`. Concretely this means:

- A typed argument can be a bare room id, an `naddr`, or a
  `30312:`/`30313:` coordinate -- identical to what huddle's own join
  accepts.
- Chat, the roster view, audio playback (`-tags huddleaudio`), and every
  refusal code (`room_full`, `upgrade_required`, etc.) behave exactly as
  documented in `skills/ncli-huddle/SKILL.md`'s "Joining from the
  terminal" section -- read that for the actual join experience.
- A future fix to huddle's join path (e.g. a reconnect/race fix) is
  inherited automatically, with zero duplicated dial/TUI logic to keep in
  sync.
- If the resolved space's `service`/`endpoint` don't map onto an ncli
  huddle transport, the same resolution code's own error surfaces --
  there's no separate "this isn't joinable" message specific to `space`.

### Omitting the id: the sole-open-space shortcut

With no argument, `join` queries `--relay` for open kind:30312 spaces
(the same `selectSpaces` `list` uses) before doing anything else:

- **Exactly one** -- resolved to its full `30312:<pubkey>:<d>` coordinate
  and joined through that same activity path a hand-typed coordinate
  would take, chat included. Not just its bare id: two different pubkeys
  publishing under the same identifier would otherwise be ambiguous.
- **Zero** -- a `not_found` error naming the relay, suggesting `space
  create` or passing an id directly.
- **More than one** -- a `usage` error listing every open space's
  identifier, so you know what to pass.

This mirrors the "vault's sole entry, use it" convenience already applied
to identity resolution elsewhere in this CLI (`cli/keyresolve`) -- a
relay with exactly one open space clearly has an obvious thing to join.

## Explicitly out of scope (for now)

- Creating/editing a kind:30313 session (scheduling a specific meeting
  inside a space, with its own title/start time) — only the space itself
  is covered.
- `space show`'s `--relay`/`--identity` et al. taking a full coordinate to
  disambiguate a non-unique id directly — pass the bare id and read every
  match for now.
