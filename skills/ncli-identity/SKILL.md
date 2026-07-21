---
name: ncli-identity
description: Generate, inspect, and manage Nostr keypairs with ncli's local vault (`ncli id`), decode any NIP-19 bech32 entity (`ncli decode`), sign unsigned events with a vault/nsec identity (`ncli id sign`), and mint NIP-26 delegation tokens (`ncli id delegate`) for scripted or agent-driven signing. Use when generating or resolving a Nostr identity (hex/npub/nsec/NIP-05), decoding an npub/nsec/note/nprofile/nevent/naddr, signing a hand-authored or dumped unsigned event so it can be published, scripting vault access with NCLI_VAULT_PASSWORD, or non-interactively creating a delegation token with --issuer/NCLI_DELEGATE_ISSUER.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's cli/ncli/id.go, cli/ncli/id_sign.go, and
cli/delegate/command.go as of writing. This skill is self-contained by
design and won't see repo changes automatically — update by hand if
flags/schemas change. -->

# ncli id / ncli id sign / ncli id delegate

`sign` and `delegate` are both subcommands of `id` (`ncli id sign`, `ncli id
delegate`). Both resolve a vault label the same way `id --reveal` does:
`id sign`'s `--identity` and `delegate`'s `--issuer`/`--delegatee` all accept a
vault label, an nsec, an npub, a hex pubkey, an nprofile, or a nip-05
address, unlocking the vault (`NCLI_VAULT_PASSWORD`) when the identifier
resolves to one. A bare 64-char hex string is always read as a **public**
key, never a private one, for all three flags -- pass `nsec1...` (or a
vault label) if you have a raw private key rather than hex.

## `ncli id` — generate

```sh
ncli id                                        # interactive: shows the key once, prompts to save
ncli id --json --save --label agent-key        # scripted: no prompts, saves under an explicit label
```

`--json` disables every prompt: saving only happens with `--save`, the
label only comes from `--label` (falls back to the npub if omitted), and
the vault password only comes from `NCLI_VAULT_PASSWORD` — never
interactive in JSON mode.

## `ncli id <identifier>` — inspect

```sh
ncli id npub1...                               # or a hex pubkey, nsec, vault label, or name@domain (NIP-05)
ncli id agent-key --json --reveal              # decrypt and include the private key (vault-saved only)
ncli id list                                    # list saved vault identities
ncli id list --json --reveal                    # list with decrypted keys
```

`--reveal` only works on identities actually saved in the vault — resolving
an arbitrary npub/nsec/NIP-05 that isn't vault-saved and asking to reveal it
errors with "identity not saved in vault, nothing to reveal".

## `ncli decode` — decode any NIP-19 entity

A standalone counterpart to `id`'s inspect mode: `id <identifier>` only
resolves things that represent a *keypair* (npub/hex/nsec/nprofile/NIP-05,
plus vault lookups); `decode` handles all six NIP-19 bech32 shapes,
including the two that aren't identities at all -- `note`/`nevent` (event
pointers) and `naddr` (an addressable-event coordinate):

```sh
ncli decode npub1...              # -> pubkey
ncli decode nsec1...              # -> privkey
ncli decode note1...               # -> event id
ncli decode nprofile1...           # -> pubkey + relay hints
ncli decode nevent1...             # -> event id + optional relays/author/kind
ncli decode naddr1...              # -> identifier + pubkey + kind + relay hints
ncli decode npub1... --json        # structured JSON instead of text
```

No vault interaction, no prompts, no network — pure local decoding.
`--json`'s `kind` field is only present when the entity actually carries
one: `nevent`'s kind is optional (omitted if the encoder never set it),
while `naddr`'s kind is required by the format itself, so a literal `kind:
0` (the `set_metadata` kind) is still included rather than treated as
absent.

## Scripted vault access

```sh
NCLI_VAULT_PASSWORD=hunter2 ncli id --json --save --label agent-key
NCLI_VAULT_PASSWORD=hunter2 ncli id agent-key --json --reveal
```

Creating a brand-new vault this way (password from the env var) skips the
usual confirm-by-retyping step — that only happens on the fully-interactive
path, since there's no risk of a mistyped confirmation when the password
came from one authoritative source.

## `ncli id sign` — sign an unsigned event

```sh
ncli id sign --identity agent-key -e draft.json -o signed.json --json
ncli id sign --identity nsec1... -e draft.json -o signed.json --json    # raw nsec, no vault involved
NCLI_VAULT_PASSWORD=hunter2 ncli id sign --identity agent-key -e draft.json -o signed.json --json
```

- `--identity <vault-label|nsec>` (required) must resolve to a **private**
  key -- a vault label (needs `NCLI_VAULT_PASSWORD` under `--json`, same as
  `id --reveal`) or a raw `nsec1...`. A pubkey-only identity (npub/hex/
  nprofile/nip-05, not vault-saved) has no key to sign with and fails
  immediately with `code: "auth"` (exit 7) -- unlike `ncli miner mine
  --identity`, which tolerates a pubkey-only identity and just leaves the
  event unsigned, signing with no key at all is never a partial result.
- `-e/--events <file>` (required) -- a single unsigned event object or an
  array, the same shapes `ncli publish`'s `-e/--events` and `ncli miner
  check`'s `-e/--events` already accept.
- `-o/--out <file>` (required) -- written back in the **same shape** it was
  read in (single object stays a single object, array stays an array), so
  the result chains straight into `ncli publish --events <out>` or `ncli
  miner check --events <out>` with no reshaping.
- If an event already declares a `pubkey` that conflicts with `--identity`'s
  resolved pubkey, this fails with `code: "invalid_input"` rather than
  silently re-signing it under a different key -- the same guard `ncli miner
  mine --identity` applies before mining.

This is the general-purpose sign step for events `ncli miner mine` didn't
already sign -- a hand-authored draft with a fixed `pubkey`, a batch dumped
by another tool, or a PoW-mined-but-unsigned draft (`miner mine` without
`--identity`, or with a pubkey-only one):

```sh
ncli miner mine -e draft.json -o mined.json -d 20        # PoW only, stays unsigned
ncli id sign --identity agent-key -e mined.json -o signed.json --json
ncli publish -e signed.json -s wss://relay.damus.io
```

## `ncli id delegate` — mint a NIP-26 token

```sh
ncli id delegate                                                    # interactive Bubble Tea wizard (needs a real tty)
ncli id delegate --issuer agent-key --delegatee relay-signer --json # vault labels, NCLI_VAULT_PASSWORD to unlock
ncli id delegate --issuer nsec1... --delegatee nsec1... --json      # raw nsecs, no vault involved
ncli id delegate --issuer agent-key --delegatee relay-signer \
  --kinds 25521,10002 --duration 365 --json
```

`--issuer` (or `NCLI_DELEGATE_ISSUER`) is what skips the wizard;
`--delegatee` -- the identity being granted authority, e.g. a relay's own
signing key -- is always required. Named `--delegatee`, not `--relay`, on
purpose: this command has no relation to `relay.yaml`/`nip11`, so a flag
named "relay" would misleadingly suggest a `wss://...` URL like every
other `--relay`-adjacent flag in this CLI. Both accept the same identifier
shapes as `id sign --identity` (see the intro above) and both must resolve
to a **private** key (the issuer to actually sign, the delegatee side
because a bare pubkey isn't distinguishable from a typo here); a
pubkey-only identity fails with `code: "auth"` (exit 7). No config
fallback. Output (`--json` or text) is the token's raw fields --
`issuer_pubkey`/`delegatee_pubkey`/`conditions`/`token` -- plus, in text
mode, the literal `["delegation", issuer, conditions, token]` tag to
attach to events signed by the delegatee's key, per NIP-26 itself. It's a
standalone token generator: nothing in this codebase reads its output
back in automatically, unlike `id sign`'s output chaining into `publish`.

The wizard itself (no `--issuer`) is unchanged and still takes a raw
private key (nsec or hex) typed directly into each step -- it does not
resolve vault labels interactively.

## Gotchas learned

- `ncli id delegate`'s wizard needs a real tty on both stdin and stdout —
  in any agent/CI/non-interactive context, always pass `--issuer` (or set
  `NCLI_DELEGATE_ISSUER`). Omitting it no longer launches the wizard blind:
  `--json`, or stdin/stdout not both being a real terminal, fails
  immediately with `--issuer is required (or set NCLI_DELEGATE_ISSUER)
  when not running interactively` (`code: "usage"`, exit 2) instead of
  hanging waiting for terminal input.
- `ncli id --json` never prompts, including for the vault password — it
  must come from `NCLI_VAULT_PASSWORD`, or any vault-touching call fails
  immediately with `vault password required; set NCLI_VAULT_PASSWORD`,
  reported as `{"error": "...", "code": "usage", "retryable": false}` on
  stderr (never stdout, and never mixed into `--json`'s success-shaped
  output) -- `usage` because the fix is supplying the env var, not retrying.
  A *wrong* password (vault exists, decrypt fails) is `code: "auth"`
  instead. Resolving a malformed identifier is `invalid_input`; resolving
  one that parses fine but isn't vault-saved when `--reveal` is passed is
  `not_found`; a nip-05 `name@domain` identifier that fails to resolve is
  `network` (`retryable: true`) rather than `invalid_input`, since the
  string itself was fine and the DNS/HTTPS lookup is what failed. None of
  these ever echo the identifier back in `input` if it looks like an
  `nsec1...` (private-key) string, even a malformed one.
- `--reveal` only decrypts vault entries; it does nothing for a resolved
  identifier that isn't vault-saved (npub/hex/nsec/NIP-05 passed directly).
  Note `id`'s own flags (`--save`/`--label`/`--reveal`) are local to `id`
  (and re-declared on `id list`) -- they don't leak into `id delegate`'s
  flag set. `--json` is a global flag (available on every command,
  `id delegate` included), unlike `--save`/`--label`/`--reveal`.
- `delegate`'s default `--kinds` is `"25521"` — ncli's own "Top Zapped"
  cache-response kind, not a generally meaningful NIP kind. Pass `--kinds`
  explicitly for anything else.
- `id delegate --delegatee` has no config fallback -- omitting it fails
  immediately (`code: "usage"`, exit 2) with `--delegatee is required`
  rather than launching the wizard or reading `relay.yaml`. A malformed
  `--issuer`/`--delegatee` identifier is `invalid_input`; a pubkey-only one
  (npub/hex/nprofile/nip-05, not vault-saved) is `auth`, exit 7 -- neither
  ever echoes the raw value back in `input` (see the intro above for the
  hex-is-always-a-pubkey rule behind that last case).
- `ncli id sign`'s `-e/--events` accepts `.json`/`.jsonp`/`.yaml`/`.yml`
  (like `mine`'s `-e`, not `miner check`'s JSON-only `-e`). An empty array
  (`[]`) is rejected (`code: "invalid_input"`) rather than silently
  succeeding with a zero-length output file.
