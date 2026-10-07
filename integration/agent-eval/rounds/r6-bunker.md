# Round R6 -- Bunker (NIP-46 remote signing)

`ncli bunker` is the *signer* side of NIP-46. You run it yourself this
round, with no interactive terminal, and do from the command line
everything its TUI would let a person do. Something else will play the
"other app": it reads the pairing URI you produce, pairs, and then asks
your bunker to sign things.

1. Start the signer as identity `eval-agent` on `ws://localhost:5500`, in
   the background, and confirm it's running and which key it signs as.
2. Write a `--grants` spec pre-authorizing a pairing for at least
   `get_public_key` and `sign_event` for kind 1 only -- nothing else.
3. Generate a pairing URI with that spec and write the *exact* URI, and
   nothing else, to `/report/r6-bunker-uri.txt`.
4. Wait for the app to pair (check the remembered sessions every few
   seconds, up to about 20 times). If nothing pairs, say so and skip to
   step 8.
5. After pairing, the app asks you to sign two events your spec doesn't
   cover. They wait for a decision rather than being answered. Find them,
   look at what each one is, and:
   - **approve** the kind 7 one, just this once;
   - **reject** the kind 30023 one.
   They expire after about 5 minutes, so don't wait long between looking
   and deciding.
6. Show the app's remembered grants, and the history of decided requests
   (it should show both of your decisions).
7. Give the app's session the name `eval-app`, revoke its kind 1 grant,
   then confirm both changes, then revoke the whole session.
8. Stop the signer, and confirm it's no longer running.

Write your self-report to `/report/r6-bunker.self-report.json`. Include the
app's pubkey and the ids of the two requests you decided.
