# Records `ncli ping` probing relay connectivity. Run via `just vhs ping`.
#
# ping narrates as plain log lines by default -- --tui opts into the live
# board instead (see the --json/--quiet veto note in `ncli ping --help`).
# Like `ncli apply`'s TUI, the board keeps running after both relays report
# reachable (it waits on ctx.Done(), not self-exit -- see client/ping.go),
# so Ctrl+C is needed to close it.
#
# ping's board is built on the same client/tui package as apply, so it
# needs the same ANSI-16 theme apply-stream.tape uses (not a generic vhs
# theme like Dracula) -- see that file's comments for why tcell's named
# colors specifically need this palette to render right. Width/Height stay
# ping's own (its board has no wide SOURCES/DESTINATIONS-style table to
# fit, unlike stream/sync).

Output docs/vhs/ping.gif

Set Shell "bash"
Set FontSize 15
Set Width 1200
Set Height 400
Set Padding 20
Set Theme {"name": "vscode-dark-modern", "black": "#000000", "red": "#cd3131", "green": "#0dbc79", "yellow": "#e5e510", "blue": "#2472c8", "magenta": "#bc3fbc", "cyan": "#11a8cd", "white": "#e5e5e5", "brightBlack": "#666666", "brightRed": "#f14c4c", "brightGreen": "#23d18b", "brightYellow": "#f5f543", "brightBlue": "#3b8eea", "brightMagenta": "#d670d6", "brightCyan": "#29b8db", "brightWhite": "#e5e5e5", "background": "#000000", "foreground": "#cccccc", "cursor": "#ffffff", "selection": "#264f78"}
Set TypingSpeed 40ms

Type "ncli ping --tui relay.ohstr.com relay.snort.social"
Sleep 500ms
Enter
Sleep 6s

Ctrl+C
Sleep 1s
