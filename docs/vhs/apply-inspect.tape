# Records the `ncli apply` TUI dashboard running the repo's own
# examples/apply/inspect.yaml -- a read-only query across two remote
# relays (wss://relay.damus.io, wss://relay.snort.social) plus a local
# store (./data/db/notes.db, gitignored, created fresh). Run via
# `just vhs apply-inspect`.
#
# inspect uses a different layout than stream/sync (see README's "TUI"
# section): a compact Targets strip, the ambient log, then a full-width
# events table anchored at the bottom. NewInspectBoard (client/tui/body.go)
# wires the events table as the initially-focused panel (the `true` in its
# AddItem call) -- no Tab needed to reach it, and Tab-ing away from it (as
# an earlier version of this tape did) is exactly why Enter used to land on
# the wrong panel and never open anything. Enter below opens the detail
# view on whichever event Autoscroll has selected (the latest one), then
# Escape closes it back out.
#
# Same Width/FontSize/Theme as apply-stream.tape -- see that file's
# comments for why (SOURCES/DESTINATIONS column widths, tcell's named
# colors resolving through the ANSI palette rather than truecolor).

Output docs/vhs/apply-inspect.gif

Set Shell "bash"
Set FontSize 18
Set Width 1600
Set Height 800
Set Padding 20
Set Theme {"name": "vscode-dark-modern", "black": "#000000", "red": "#cd3131", "green": "#0dbc79", "yellow": "#e5e510", "blue": "#2472c8", "magenta": "#bc3fbc", "cyan": "#11a8cd", "white": "#e5e5e5", "brightBlack": "#666666", "brightRed": "#f14c4c", "brightGreen": "#23d18b", "brightYellow": "#f5f543", "brightBlue": "#3b8eea", "brightMagenta": "#d670d6", "brightCyan": "#29b8db", "brightWhite": "#e5e5e5", "background": "#000000", "foreground": "#cccccc", "cursor": "#ffffff", "selection": "#264f78"}
Set TypingSpeed 40ms

Type "ncli apply -f examples/apply/inspect.yaml"
Sleep 500ms
Enter

Sleep 6s
Enter
Sleep 4s
Escape
Sleep 2s

Ctrl+C
Sleep 1s
