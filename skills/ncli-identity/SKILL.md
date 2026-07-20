---
name: ncli-identity
description: Generate, inspect, and manage Nostr keypairs with ncli's local vault (`ncli id`), decode any NIP-19 bech32 entity (`ncli decode`), sign unsigned events with a vault/nsec identity (`ncli id sign`), and mint NIP-26 delegation tokens (`ncli id delegate`) for scripted or agent-driven signing. Use when generating or resolving a Nostr identity (hex/npub/nsec/NIP-05), decoding an npub/nsec/note/nprofile/nevent/naddr, signing a hand-authored or dumped unsigned event so it can be published, scripting vault access with NCLI_VAULT_PASSWORD, or non-interactively creating a delegation token with --issuer-key/NCLI_DELEGATE_ISSUERKEY.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's cli/ncli/id.go, cli/ncli/id_sign.go, and
cli/delegate/command.go as of writing. This skill is self-contained by
design and won't see repo changes automatically — update by hand if
flags/schemas change. -->

# ncli id / ncli id sign / ncli id delegate

`sign` and `delegate` are both subcommands of `id` (`ncli id sign`, `ncli id
delegate`), but neither is **functionally integrated** with the vault the
way `id`/`id list` are: `id sign`'s `--identity` *does* resolve a vault
label (unlocking it the same way `id --reveal` does), but `delegate`'s
`--issuer-key` only ever takes a raw key you pass in directly — it does not
read the vault at all. Extract a key from `id --reveal` first if you want to
delegate from a vault-saved identity (pattern shown below).

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
ncli id delegate                                                            # interactive Bubble Tea wizard (needs a real tty)
ncli id delegate --issuer-key <nsec-or-hex> --json                          # non-interactive, --relay-key falls back to config's nip11.privkey
ncli id delegate --issuer-key <nsec-or-hex> --relay-key <nsec-or-hex> \
  --kinds 25521,10002 --duration 365 --json
```

`--issuer-key` (or `NCLI_DELEGATE_ISSUERKEY`) is what skips the wizard.
Output (`--json` or text) is a ready-to-paste `nip11.delegation` block for
`relay.yaml` — see `ncli-relay-ops`.

## Bridging `id` → `id delegate`

```sh
NCLI_VAULT_PASSWORD=hunter2 ncli id agent-key --json --reveal | jq -r .nsec
# then:
ncli id delegate --issuer-key <nsec-from-above> --relay-key <relay-nsec> --json
```

`id`/`id delegate` both accept either `nsec` or hex for any key input
interchangeably — no separate flags for each form.

## Gotchas learned

- `ncli id delegate`'s wizard needs a real tty on both stdin and stdout —
  in any agent/CI/non-interactive context, always pass `--issuer-key` (or
  set `NCLI_DELEGATE_ISSUERKEY`). Omitting it no longer launches the wizard
  blind: `--json`, or stdin/stdout not both being a real terminal, fails
  immediately with `--issuer-key is required (or set
  NCLI_DELEGATE_ISSUERKEY) when not running interactively` (`code:
  "usage"`, exit 2) instead of hanging waiting for terminal input.
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
- `id delegate --relay-key` falls back to `nip11.privkey` from `--config` if
  omitted; if neither is available, it fails immediately (`code: "usage"`,
  exit 2) with `--relay-key is required (or set nip11.privkey in config)`
  rather than launching the wizard. A malformed `--issuer-key`/`--relay-key`
  is `invalid_input` instead, and never echoes the key value in `input`.
  "`--config` if omitted" also includes a saved `ncli relay context` (see
  `skills/ncli-relay-ops/SKILL.md`) or a `ncli.yaml`/`relay.yaml` in the
  working directory -- any of those can supply `nip11.privkey` without
  `--config` being passed explicitly.
- Both `ncli id` and `ncli id delegate` had zero README usage examples as of
  writing — this skill is effectively their first real documentation.
- `ncli id sign`'s `-e/--events` accepts `.json`/`.jsonp`/`.yaml`/`.yml`
  (like `mine`'s `-e`, not `miner check`'s JSON-only `-e`). An empty array
  (`[]`) is rejected (`code: "invalid_input"`) rather than silently
  succeeding with a zero-length output file.
