# Round R6 -- Bunker (NIP-46 remote signing)

`ncli bunker` is the *signer* side of NIP-46 only -- it has no client side
of its own, and starting the signer daemon needs a real interactive
terminal (it's a TUI), which isn't available to you here. That part has
already been done for you, the same way the shared relay was already
started for you in an earlier round: a bunker daemon is running under
identity `eval-agent`, listening on `ws://localhost:5500`. Don't try to
run bare `ncli bunker` yourself -- it will fail with a clear "needs an
interactive terminal" error, and that's expected, not something to work
around.

Everything below this point *is* fully scriptable, and is your actual
job this round. Something else will play the "other app" pairing with
you; you don't need to know or care what it is, only that it will read
whatever pairing URI you produce.

1. `ncli bunker status --json` to confirm the daemon is up and see which
   identity/relay it's using.
2. Write a `--grants` spec yourself (check AGENTS.md / the bunker skill
   for its exact shape) pre-authorizing a pairing for at least
   `get_public_key` and `sign_event` for kind 1.
3. Generate a pairing URI with `ncli bunker connect --grants <your-spec>`
   and write the *exact* URI, and nothing else, to
   `/report/r6-bunker-uri.txt`.
4. Poll `ncli bunker sessions list --json` every few seconds, up to about
   10 times, waiting for a session to show up (something will attempt to
   pair against your URI shortly after you write it). If nothing has
   paired after ~10 tries, say so plainly in your report and move on --
   don't poll forever.
5. Once (or if) something pairs, show `ncli bunker sessions list --json`
   and `ncli bunker sessions grants <pubkey> --json` for it, and confirm
   the grants match what you specified in your spec.
6. `ncli bunker history --json` to see the resolved `connect` (and any
   other) request(s) tied to that pairing.

Do **not** run `ncli bunker stop` -- leave the daemon running for
whatever comes after this round.

Write your self-report to `/report/r6-bunker.self-report.json`.
