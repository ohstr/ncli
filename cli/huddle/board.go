package huddle

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/ncli/huddleclient"
	"github.com/ohstr/nmilat/nip19"
)

// refreshInterval is how often the roster is re-read and redrawn.
//
// The speaking indicator has to be cleared on a timer rather than in response
// to an event: a peer who stops talking sends nothing, so no frame ever arrives
// to say "they went quiet". Without this tick the last speaker would stay lit
// forever. It is well under SpeakingHold so the indicator clears promptly, and
// redrawing a table of at most 25 rows ten times a second is cheap.
const refreshInterval = 100 * time.Millisecond

// Client is the part of *huddleclient.Client the board consumes, as an
// interface so the board can be driven by a scripted fake in tests with no
// relay and no network.
type Client interface {
	Self() huddleclient.Peer
	Roster() []huddleclient.Peer
	Frames() <-chan huddleclient.Frame
	Err() error
	Close() error
}

// Compile-time proof the real client still satisfies what the board asks of it,
// so a signature change in huddleclient fails here rather than at a call site.
var _ Client = (*huddleclient.Client)(nil)

// Board is ncli's huddle view: who is in the room, who is talking, and how
// loudly. It implements tui.ChildProvider, tui.FooterHintsProvider and
// tui.CtrlCHandler, and is handed to tui.App.Load.
//
// It is listen-and-watch only. Mute and raise-hand are deliberately absent
// rather than present and inert: muting means gating a microphone this build
// does not have, and raising a hand means publishing a NIP-53 kind 10312 with
// a `hand` tag, which needs a signer and a relay connection the board is not
// given. Both arrive with the pieces they depend on.
type Board struct {
	*tview.Flex

	app    *tui.App
	client Client
	roster *roster

	panel  *tview.Flex
	table  *tview.Table
	status *tview.TextView

	room string

	closeOnce sync.Once
}

// NewBoard builds the huddle board for client. room is shown in the panel
// title; app may be nil in tests, in which case draws run synchronously on the
// caller's goroutine instead of going through tview's event loop.
func NewBoard(app *tui.App, client Client, room string) *Board {
	b := &Board{
		app:    app,
		client: client,
		room:   room,
		roster: newRoster(client.Self().Pubkey, SpeakingHold,
			huddleclient.DefaultSpeakingThreshold, time.Now),
	}

	b.table = tview.NewTable()
	b.table.SetBorders(false).
		SetSelectable(true, false).
		SetFixed(1, 0).
		SetSelectedStyle(tcell.StyleDefault.
			Background(tui.ColorPrimary).
			Foreground(tui.ColorText))
	b.table.SetInputCapture(b.handleKey)

	b.panel = tview.NewFlex().AddItem(b.table, 0, 1, true)
	b.panel.SetBorder(true).
		SetTitle(b.panelTitle(0, 0)).
		SetTitleColor(tui.ColorPrimary)
	tui.WireFocusBorder(b.table, b.panel.Box)

	b.status = tview.NewTextView()
	b.status.SetDynamicColors(true).SetTextAlign(tview.AlignLeft)

	b.Flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(b.panel, 0, 1, true).
		AddItem(b.status, 1, 0, false)

	// Seeded before the first paint, so the board opens showing the room as it
	// already is rather than flashing an empty roster until Run's first tick.
	b.roster.sync(client.Roster())
	b.render(b.roster.participants())
	return b
}

// Participants is the roster as the board currently sees it: who is present,
// who is speaking, and at what level. Exported so the state the board renders
// can be asserted without a terminal.
func (b *Board) Participants() []Participant {
	return b.roster.participants()
}

// Childs gives the board's focusable panels to tui.App's Tab cycling. Only the
// table is focusable -- the status line is a readout.
func (b *Board) Childs() []tview.Primitive {
	return []tview.Primitive{b.table}
}

// FooterHints reports the keys this board actually honors. Leaving is offered
// under both <q> and Ctrl+C because Ctrl+C is what a terminal user reaches for
// to get out, and HandleCtrlC turns it into the same confirmed leave.
func (b *Board) FooterHints(tview.Primitive) string {
	return fmt.Sprintf("[%s:-:b]<Tab> [%s:-:-]Panel   [%s:-:b]<q> [%s:-:-]Leave   [%s:-:b]<Ctrl+C> [%s:-:-]Leave",
		tui.ColorAccent, tui.ColorMuted,
		tui.ColorAccent, tui.ColorMuted,
		tui.ColorAccent, tui.ColorMuted)
}

// HandleCtrlC asks before hanging up. tview's default would stop the
// application the instant nothing else consumes the keystroke, which here means
// dropping out of a live call by a single accidental keypress.
func (b *Board) HandleCtrlC() bool {
	if b.app == nil {
		return false
	}
	b.confirmLeave()
	return true
}

func (b *Board) handleKey(event *tcell.EventKey) *tcell.EventKey {
	switch event.Rune() {
	case 'q', 'Q':
		if b.app == nil {
			return event
		}
		b.confirmLeave()
		return nil
	}
	return event
}

func (b *Board) confirmLeave() {
	// Esc maps to the last func, so "Stay" is deliberately last: dismissing the
	// dialog by accident must not hang up.
	b.app.ShowDialog("LEAVE HUDDLE", "Leave the call?", tui.ColorWarning,
		[]string{"Leave", "Stay"},
		b.Leave,
		func() {},
	)
}

// Leave closes the client and stops the TUI. Safe to call more than once: the
// confirm dialog and a Run-loop shutdown can both reach it.
func (b *Board) Leave() {
	b.closeOnce.Do(func() {
		_ = b.client.Close()
	})
	if b.app != nil {
		b.app.Stop()
	}
}

// Run pumps the client's frames into the roster and redraws until ctx is done
// or the client's frame channel closes. It does not return an error: a dropped
// connection surfaces through the status line via Client.Err, because the call
// ending is something to show the operator, not a value to hand a caller.
func (b *Board) Run(ctx context.Context) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	frames := b.client.Frames()
	for {
		select {
		case <-ctx.Done():
			return

		case frame, ok := <-frames:
			if !ok {
				// The reader goroutine is gone, so the call is over. Draw once
				// more so the status line shows why rather than leaving the
				// last live roster on screen looking healthy.
				b.queueDraw(func() { b.render(b.roster.participants()) })
				return
			}
			b.roster.heardFrame(frame)

		case <-ticker.C:
			b.roster.sync(b.client.Roster())
			rows := b.roster.participants()
			b.queueDraw(func() { b.render(rows) })
		}
	}
}

// queueDraw hands fn to tview's event loop, the only goroutine allowed to touch
// primitives. With no app (tests) it runs synchronously on the caller's.
func (b *Board) queueDraw(fn func()) {
	if b.app == nil {
		fn()
		return
	}
	b.app.QueueUpdateDraw(fn)
}

var headers = []string{"", "PARTICIPANT", "LEVEL", "STATE"}

// render repaints the table from rows. It touches tview state, so it must only
// be called from tview's event loop -- go through queueDraw.
func (b *Board) render(rows []Participant) {
	b.table.Clear()

	for col, h := range headers {
		b.table.SetCell(0, col, tview.NewTableCell(h).
			SetTextColor(tui.ColorMuted).
			SetSelectable(false).
			SetExpansion(boolToInt(col == 1)))
	}

	speaking := 0
	for i, p := range rows {
		row := i + 1

		glyph, glyphColor := "·", tui.ColorMuted
		if p.Speaking {
			speaking++
			glyph, glyphColor = "●", tui.ColorSuccess
		}
		b.table.SetCell(row, 0, tview.NewTableCell(" "+glyph+" ").
			SetTextColor(glyphColor))

		label := shortNpub(p.Pubkey)
		labelColor := tui.ColorText
		if p.Self {
			label += " (you)"
			labelColor = tui.ColorPrimary
		}
		b.table.SetCell(row, 1, tview.NewTableCell(label).
			SetTextColor(labelColor).
			SetExpansion(1))

		b.table.SetCell(row, 2, tview.NewTableCell(meter(p.Level)).
			SetTextColor(levelColor(p)))

		b.table.SetCell(row, 3, tview.NewTableCell(stateText(p)).
			SetTextColor(tui.ColorMuted))
	}

	b.panel.SetTitle(b.panelTitle(len(rows), speaking))
	b.status.SetText(b.statusLine(len(rows)))
}

func (b *Board) panelTitle(participants, speaking int) string {
	title := fmt.Sprintf(" HUDDLE %s [%d] ", b.room, participants)
	if speaking > 0 {
		title = fmt.Sprintf(" HUDDLE %s [%d, %d talking] ", b.room, participants, speaking)
	}
	return title
}

func (b *Board) statusLine(participants int) string {
	if err := b.client.Err(); err != nil {
		return fmt.Sprintf(" [%s:-:b]disconnected[%s:-:-] %s", tui.ColorDanger, tui.ColorMuted, err)
	}
	if participants <= 1 {
		// Worth saying outright: an empty-looking roster is the normal state
		// for the first person in, not a sign the connection is broken.
		return fmt.Sprintf(" [%s:-:-]connected, waiting for others to join", tui.ColorMuted)
	}
	return fmt.Sprintf(" [%s:-:-]connected", tui.ColorMuted)
}

// stateText describes a row in words, for the cases the meter alone cannot
// carry.
//
// A remote peer is never reported as "muted": this wire protocol has no mute
// signal -- muting is implemented by not sending -- so a muted peer and a
// silent one are genuinely indistinguishable from here. Claiming to know which
// would be inventing information.
func stateText(p Participant) string {
	switch {
	case p.Speaking:
		return "speaking"
	case !p.Heard:
		return "no audio yet"
	default:
		return "silent"
	}
}

func levelColor(p Participant) tcell.Color {
	if p.Speaking {
		return tui.ColorSuccess
	}
	return tui.ColorMuted
}

// meterBars is the level meter's width in characters.
const meterBars = 10

// meterFloor is the dBov level the meter treats as empty. The scale starts here
// rather than at wire.LevelSilenceFloor (-127) because speech sits roughly
// between -40 and -3 dBov: spreading the full 127 dB range over ten characters
// would squeeze every normal speaking voice into the last two.
const meterFloor = -60

// meter renders a level in dBov as a bar. Levels are client-authored and
// untrusted, so this clamps rather than trusting the range.
func meter(level int8) string {
	filled := 0
	if int(level) > meterFloor {
		scaled := float64(int(level)-meterFloor) / float64(-meterFloor) * meterBars
		filled = int(scaled + 0.5)
	}
	if filled > meterBars {
		filled = meterBars
	}
	return strings.Repeat("█", filled) + strings.Repeat("·", meterBars-filled)
}

// shortNpub renders pubKeyHex as a truncated bech32 npub, matching the shape
// cli/bunker's own board uses. Falls back to truncated hex if it does not even
// encode (it always should).
func shortNpub(pubKeyHex string) string {
	npub, err := nip19.EncodePublicKey(pubKeyHex)
	if err != nil {
		if len(pubKeyHex) <= 20 {
			return pubKeyHex
		}
		return pubKeyHex[:14] + "..." + pubKeyHex[len(pubKeyHex)-4:]
	}
	if len(npub) <= 20 {
		return npub
	}
	return npub[:14] + "..." + npub[len(npub)-4:]
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
