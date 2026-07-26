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
# The TUI (client/tui) colors its widgets via client/tui/colors.go's own
# truecolor hex constants (ColorPrimary, ColorAccent, ColorSuccess, ...),
# not tcell's named/indexed colors -- so those foreground colors render
# identically here no matter what this theme's own magenta/green/blue
# slots are set to. That wasn't always true: before colors.go existed,
# tcell rendered ColorPurple/ColorBlue/ColorGreen through whatever ANSI
# palette the terminal defined, and matching this theme's specific slot
# values to VS Code's default "Dark Modern" integrated-terminal palette
# (the reference recordings' own terminal) was the only way to get a
# recording that looked like a real terminal instead of vhs's own default
# theme. What still matters here: `background` is pure black to match the
# padding to every tview.Box's own black widget background
# (tview.Styles.PrimitiveBackgroundColor -- still a genuinely indexed
# ANSI color this app doesn't control) -- no seam. The rest of this
# theme is now just a reasonable-looking shell theme for the typed
# command line itself, not something the TUI's own rendering depends on.
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
