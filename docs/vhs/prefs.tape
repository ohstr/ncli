# Records `ncli prefs relays add`, then `ncli find jack@primal.net` with no
# -s at all -- the point of prefs is that find (and ping/dump/miner
# check/publish) fall back to it automatically, so this is the direct
# payoff shot: add, then use it. Run via `just vhs prefs`.
#
# Deliberately doesn't end on `clear` (no list/remove/path steps either --
# just the two commands above), so unlike dump.tape/apply-sync.tape's
# throwaway dirs this one DOES leave a mark on the container's real
# prefs.yaml -- `ncli prefs relays clear` after recording (not part of the
# tape) resets it back to empty.

Output docs/vhs/prefs.gif

Set Shell "bash"
Set FontSize 15
Set Width 1200
Set Height 600
Set Padding 20
Set Theme "Dracula"
Set TypingSpeed 40ms

Type "ncli prefs relays add relay.damus.io"
Sleep 500ms
Enter
Sleep 1s

Type "ncli find jack@primal.net -k 1 -l 2"
Sleep 500ms
Enter
Sleep 3s
