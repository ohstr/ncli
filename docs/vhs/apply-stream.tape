# Records the `ncli apply` live TUI dashboard running the repo's own
# examples/apply/stream.yaml against real relays. Run via `just vhs`.
#
# Width/FontSize here are wider than the other tapes on purpose: at
# 1200x15px the SOURCES/DESTINATIONS tables don't have enough columns and
# the rightmost header (RETRIES) gets clipped -- this isn't a vhs capture
# bug, the terminal is sized correctly, the table just needs more columns.
#
# examples/apply/stream.yaml has one destination (wss://relay.nostr.band)
# that's slow/unreachable from some networks, so that destination may show
# as permanently retrying in the recording -- expected, not a recording bug.
#
# This also means running this tape writes real streamed events into the
# repo's tracked data/relay1 and data/relay2 local stores (the example's
# own destinations) -- `git checkout -- data/relay1 data/relay2` afterward
# if you don't want to commit that.

Output docs/vhs/apply-stream.gif

Set Shell "bash"
Set FontSize 18
Set Width 1600
Set Height 800
Set Padding 20
# The TUI (client/tui) colors its widgets with tcell's named colors
# (tcell.ColorPurple, ColorBlue, ColorGreen, ...), not raw hex -- tcell
# deliberately renders these as plain indexed ANSI codes rather than
# truecolor, specifically so they follow whatever palette the terminal
# defines. That makes this theme's 16 ANSI slots (not a generic vhs theme
# like Dracula) the actual knob for matching a real terminal. This is VS
# Code's default "Dark Modern" integrated-terminal palette, since that's
# what the reference recordings were taken in:
#   - magenta: ColorPurple (borders, titles, selection) -- bold, so it's
#     drawn in the *bright* variant (VS Code's drawBoldTextInBrightColors
#     default), which xterm.js replicates
#   - green: ColorGreen (source-flow flags)
#   - blue: ColorBlue -- resolves to the *bright* slot, not plain blue,
#     since 0x0000FF is nearer the bright-blue reference (destination flows)
# `background` is pure black to match the padding to the TUI's own black
# widget background (see client/tui) -- no seam. `black` itself no longer
# needs to dodge that background: the widget-black/text-black collision that
# made timestamps and a few labels invisible was a real bug in client/tui,
# fixed separately (formerly "[black:-:-]"/tcell.ColorBlack for timestamps,
# the footer's "Off" label, and the Info log level).
Set Theme {"name": "vscode-dark-modern", "black": "#000000", "red": "#cd3131", "green": "#0dbc79", "yellow": "#e5e510", "blue": "#2472c8", "magenta": "#bc3fbc", "cyan": "#11a8cd", "white": "#e5e5e5", "brightBlack": "#666666", "brightRed": "#f14c4c", "brightGreen": "#23d18b", "brightYellow": "#f5f543", "brightBlue": "#3b8eea", "brightMagenta": "#d670d6", "brightCyan": "#29b8db", "brightWhite": "#e5e5e5", "background": "#000000", "foreground": "#cccccc", "cursor": "#ffffff", "selection": "#264f78"}
Set TypingSpeed 40ms

Type "ncli apply -f examples/apply/stream.yaml"
Sleep 500ms
Enter

Sleep 15s
Tab
Sleep 2s
Tab
Sleep 3s

Ctrl+C
Sleep 1s
