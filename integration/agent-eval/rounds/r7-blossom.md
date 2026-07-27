# Round R7 -- Blossom media

A Blossom server is reachable at `http://blossom:3000` (plain reads need
no auth; uploads/deletes are authorized per-request by your Nostr identity
via BUD-11). Use your `eval-agent` identity.

1. `ncli blossom servers add http://blossom:3000 --identity eval-agent`,
   then `ncli blossom servers list` to confirm it saved.
2. Create a small throwaway text file yourself and upload it with
   `ncli blossom upload`.
3. `ncli blossom list --identity eval-agent` and confirm your upload
   shows up (by hash).
4. Download it back to a *different* filename and confirm the bytes are
   identical to what you uploaded (compare checksums, don't eyeball it).
5. `ncli blossom mirror` the same blob's own URL on that server (yes,
   mirroring it back to itself is fine for this check) and note the
   report shape you get back.
6. `ncli blossom rm` it with `--yes`, then confirm it no longer shows up
   in `list`.

Write your self-report to `/report/r7-blossom.self-report.json`,
including the blob's sha256.
