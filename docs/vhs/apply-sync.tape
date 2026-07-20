# Records the `ncli apply` TUI dashboard running the repo's own
# examples/apply/sync.yaml -- a one-shot NIP-77 negentropy reconciliation
# between a local store (./data/db/notes.db, gitignored, created fresh) and
# wss://relay.ohstr.com. Run via `just vhs apply-sync`.
#
# Unlike stream, sync's reconciliation finishes in well under a second
# against this relay -- the TUI stays open afterward showing the completed
# log/metrics until interrupted, same as stream, so the sleep below is
# mostly just holding that finished state on screen.
#
# Same Width/FontSize/Theme as apply-stream.tape -- see that file's
# comments for why (SOURCES/DESTINATIONS column widths, tcell's named
# colors resolving through the ANSI palette rather than truecolor).
#
# Unlike apply-stream/apply-inspect, sync logs the local store's path as an
# *absolute* path ("Opening local store: <path>") rather than just its
# basename -- if run straight from the repo root, that bakes this
# machine's real, full repo path into the recorded gif. So, same as
# dump.tape/miner.tape, the hidden setup below copies the spec into a
# throwaway /tmp dir and cds there first, so what actually gets logged (and
# recorded) is a generic /tmp/ncli-demo/... path instead.

Output docs/vhs/apply-sync.gif

Set Shell "bash"
Set FontSize 18
Set Width 1600
Set Height 800
Set Padding 20
Set Theme {"name": "vscode-dark-modern", "black": "#000000", "red": "#cd3131", "green": "#0dbc79", "yellow": "#e5e510", "blue": "#2472c8", "magenta": "#bc3fbc", "cyan": "#11a8cd", "white": "#e5e5e5", "brightBlack": "#666666", "brightRed": "#f14c4c", "brightGreen": "#23d18b", "brightYellow": "#f5f543", "brightBlue": "#3b8eea", "brightMagenta": "#d670d6", "brightCyan": "#29b8db", "brightWhite": "#e5e5e5", "background": "#000000", "foreground": "#cccccc", "cursor": "#ffffff", "selection": "#264f78"}
Set TypingSpeed 40ms

Hide
Type "mkdir -p /tmp/ncli-demo && cp examples/apply/sync.yaml /tmp/ncli-demo/ && cd /tmp/ncli-demo && clear"
Enter
Show

Type "ncli apply -f sync.yaml"
Sleep 500ms
Enter

Sleep 6s
Tab
Sleep 2s
Tab
Sleep 3s

Ctrl+C
Sleep 1s
