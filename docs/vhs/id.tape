# Records `ncli id` generating a fresh keypair, then inspecting a
# nip-05 address (same jack@primal.net used in find.tape, for a
# consistent cast across the demo gifs). Run via `just vhs id`.
#
# The bare `ncli id` prompts "Save this identity to your vault? [y/N]"
# before exiting -- the trailing Enter below declines (default No), same
# as piping from /dev/null would, so this tape never touches the vault.

Output docs/vhs/id.gif

Set Shell "bash"
Set FontSize 15
Set Width 1200
Set Height 500
Set Padding 20
Set Theme "Dracula"
Set TypingSpeed 40ms

Type "ncli id"
Sleep 500ms
Enter
Sleep 1s
Enter
Sleep 500ms

Type "ncli id jack@primal.net"
Sleep 500ms
Enter
Sleep 3s
