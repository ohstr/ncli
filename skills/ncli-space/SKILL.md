---
name: ncli-space
description: Create, list, show, and join NIP-53 meeting spaces with ncli space -- publish a kind:30312 space, see which ones a relay knows about (with or without a live call), inspect one's full detail, or join it. Use when publishing a space for others to find, browsing what spaces/calls a relay hosts, or working out whether ncli huddle or ncli space is the right entry point for a given task.
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

`ncli space` is the generic front door onto this: create one, list what a
relay has, show one's detail, join one. **`ncli huddle` still exists
unchanged** — it's the right name for the ephemeral-transport-specific
operations (`huddle list`, listing *rooms* rather than *spaces*), and
`space join` is literally `huddle join`'s own command, reused rather than
reimplemented (same flags, same resolution/dial/TUI). Read
`skills/ncli-huddle/SKILL.md` for the join experience itself (roster,
chat, audio playback); this skill covers the space side: publishing one
and discovering what's out there.

## Command surface

| Command | Notes |
|---|---|
| `space create <id>` | Publishes kind:30312. `--service` defaults to `--relay` |
| `space list [--all]` | Every open space by default; `--all` adds closed/private |
| `space show <id>` | One space's full detail, every match if the id isn't unique |
| `space join <id\|naddr\|room>` | = `ncli huddle join`, mounted here too |

`--relay` (falls back to the first configured prefs relay) and `--identity`
(vault label/nsec/npub/hex/nprofile/nip-05, falling back to `space.identity`
/ `NCLI_SPACE_IDENTITY`, then the vault's sole entry when there is exactly
one) are persistent flags on `space` itself, inherited by every
subcommand — `create` needs identity to sign with; `list`/`show` are
anonymous reads and ignore it; `join` uses its own identical handling,
inherited from `huddle`.

```sh
ncli space create standup --summary "Daily sync" --hashtag standup
ncli space list --relay wss://relay.example
ncli space show standup
ncli space join standup
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
  `/huddle/<id>/audio` by `transportFromSpace`, so `space join`/`huddle
  join` can dial it. Pass `--service`/`--endpoint` explicitly to describe
  a space pointing at some other meeting technology instead — still a
  completely valid NIP-53 space, just not one `ncli` itself can join.
- The signing identity's own pubkey becomes the space's `Host`.

## `list` vs `huddle spaces` — different filters, same events

Both read kind:30312 + kind:30313 off a relay, but they answer different
questions:

| | `ncli space list` | `ncli huddle spaces` |
|---|---|---|
| Shows a space with no live session | yes | no |
| Default status filter | open only (`--all` widens it) | open only, no override |
| Row shows | a `LIVE` column | only spaces that already have a live session |

Use `space list` to see what a relay hosts at all (including a space
nobody is currently in); use `huddle spaces` when you specifically want
"what can I join right now" and don't care about anything else. Both
treat a session claiming `live` whose event hasn't been refreshed within
`--stale-after` (default 1h) as ended, per the spec's allowance that
clients may read an un-refreshed live event as abandoned.

## `show` and non-unique ids

A kind:30312's `d` tag is only unique per-pubkey — two different hosts can
both use `standup` as their identifier. `space show <id>` queries by `d`
tag alone (no pubkey filter), so if more than one space matches, every one
is printed, not just the first. Pass a full `30312:<pubkey>:<d>` coordinate
or an `naddr` to `space join` (not `show`, which has no such form yet) to
disambiguate when dialing a specific one matters.

## `join` is not reimplemented here

`cli/space`'s `join` subcommand is the exact same `*cobra.Command` value
`huddle.NewJoinCommand()` returns, just mounted under `space` too (with
only its `Short` text tweaked for context — flags, `RunE`, and every
resolution/dial/TUI code path are untouched). Concretely this means:

- The argument can be a bare room id, an `naddr`, or a `30312:`/`30313:`
  coordinate — identical to `ncli huddle join`'s own rules.
- Chat, the roster view, audio playback (`-tags huddleaudio`), and every
  refusal code (`room_full`, `upgrade_required`, etc.) behave exactly as
  documented in `skills/ncli-huddle/SKILL.md`'s "Joining from the
  terminal" section — read that for the actual join experience.
- A future fix to huddle's join path (e.g. a reconnect/race fix) is
  inherited by `space join` automatically, with zero duplicated logic to
  keep in sync.
- If the resolved space's `service`/`endpoint` don't map onto an ncli
  huddle transport, the same resolution code's own error surfaces --
  there's no separate "this isn't joinable" message specific to `space`.

## Explicitly out of scope (for now)

- Creating/editing a kind:30313 session (scheduling a specific meeting
  inside a space, with its own title/start time) — only the space itself
  is covered.
- `space show`'s `--relay`/`--identity` et al. taking a full coordinate to
  disambiguate a non-unique id directly — pass the bare id and read every
  match for now.
