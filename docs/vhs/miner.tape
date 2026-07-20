# Records `ncli miner mine` finding a NIP-13 proof-of-work nonce. Run via
# `just vhs`.
#
# --workers and --progress-interval are both left at their real defaults
# (0 = every available CPU core; 5s progress narration -- client/miner.go,
# cli/ncli/miner.go) rather than tuned for the recording, so the command
# stays short and shows genuine default UX. Difficulty is picked high
# enough (26) that mining takes long enough for the 5s progress line to
# actually fire at least once before it finishes -- PoW search time is
# inherently random (expected ~2^26 hashes, not a fixed duration), so the
# generous Sleep below is a buffer, not a guarantee; an unlucky run may
# still finish early or spill past it.

Output docs/vhs/miner.gif

Set Shell "bash"
Set FontSize 15
Set Width 1200
Set Height 500
Set Padding 20
Set Theme "Dracula"
Set TypingSpeed 40ms

Hide
Type "mkdir -p /tmp/ncli-demo && cp examples/event.yaml /tmp/ncli-demo/ && cd /tmp/ncli-demo && clear"
Enter
Show

Type "ncli miner mine -e event.yaml -o mined.json -d 26"
Sleep 500ms
Enter
Sleep 20s
