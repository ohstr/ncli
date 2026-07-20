# Records `ncli find` resolving a nip-05 address, then fetching that
# author's recent notes. Run via `just vhs`.

Output docs/vhs/find.gif

Set Shell "bash"
Set FontSize 15
Set Width 1200
Set Height 700
Set Padding 20
Set Theme "Dracula"
Set TypingSpeed 40ms

Type "ncli find jack@primal.net -k 1 -l 2 -s relay.damus.io"
Sleep 500ms
Enter
Sleep 5s
