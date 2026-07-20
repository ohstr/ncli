# Records `ncli dump` exporting events to JSON. Run via `just vhs`.
#
# dump resolves -o to an absolute path before logging it (cli/ncli/dump.go),
# so the hidden setup below cds into a throwaway /tmp dir first -- keeps the
# visible command a plain "-o notes.json" and keeps the logged path generic
# instead of leaking this machine's real repo path into the recording.

Output docs/vhs/dump.gif

Set Shell "bash"
Set FontSize 15
Set Width 1200
Set Height 400
Set Padding 20
Set Theme "Dracula"
Set TypingSpeed 40ms

Hide
Type "mkdir -p /tmp/ncli-demo && cd /tmp/ncli-demo && clear"
Enter
Show

Type "ncli dump -s relay.damus.io -k 1 --since 1h -o notes.json"
Sleep 500ms
Enter
Sleep 3s

Type "jq length notes.json"
Sleep 500ms
Enter
Sleep 3s
