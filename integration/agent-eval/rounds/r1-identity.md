# Round R1 -- Identity

`ncli` should already be installed (an earlier round did this; if it
somehow isn't, install it the same way `https://ohstr.github.io/ncli/PROMPT.md`
describes before continuing). Use `ncli --help` / `ncli <cmd> --help`,
and re-fetch `https://raw.githubusercontent.com/ohstr/ncli/main/AGENTS.md`
if you need the command/flag contract again.

Use the vault label `eval-agent` for everything you save in this round --
later rounds will reuse it.

1. Generate a new identity in the vault under label `eval-agent`, printed
   as JSON.
2. Inspect it again two ways -- by its vault label, and by its npub --
   and confirm both resolve to the same pubkey.
3. `ncli decode` that npub and confirm the hex pubkey it prints matches.
4. Write a small unsigned kind:1 text-note event to a JSON file yourself,
   sign it with `ncli id sign` using `eval-agent`, and check the signed
   result's `pubkey`/`sig`/`id` fields look structurally right.
5. Mint a NIP-26 delegation token with `ncli id delegate`, from
   `eval-agent` to a second, freshly generated throwaway pubkey, scoped to
   kind 1 only, for a short validity window.

Write your self-report to `/report/r1-identity.self-report.json`.
