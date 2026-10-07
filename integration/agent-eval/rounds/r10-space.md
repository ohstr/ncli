# Round R10 -- Meeting spaces, calls and chat (NIP-53)

The relay at `ws://localhost:5500` hosts NIP-53 meeting spaces and their
voice calls ("huddles"). Use your `eval-agent` identity throughout. Read
the space skill before starting.

1. Create a space with id `r10-standup` and a short summary. Write its
   full `30312:<pubkey>:r10-standup` coordinate, and nothing else, to
   `/report/r10-space.txt` -- someone else is waiting to join it.
2. List the relay's spaces and show yours. Also show `eval-agent`'s
   profile card.
3. Join the call yourself, without a terminal, for 90 seconds, saving
   everything it reports to `/report/r10-join.ndjson`. Run it in the
   background so you can keep working while it's open.
4. While you're in the call:
   - list the relay's active call rooms and confirm `r10-standup` shows
     more than one person;
   - send the message `hello from eval-agent` to the space's chat.
5. When the call ends, read the space's chat. Someone else posted in it;
   reply to their message (as a threaded reply, not a new message) with
   `got it`.
6. From `/report/r10-join.ndjson`, say who joined and left the call while
   you were in it, and how the stream ended.

Write your self-report to `/report/r10-space.self-report.json`.
