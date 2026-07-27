package bunker

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip19"
	"github.com/ohstr/nmilat/nip46"
	"github.com/rivo/tview"
)

// renderInterval matches client/tui's own Table/EventTable ticker cadence
// -- rendering decoupled from ingestion, not redrawn on every single
// approve/reject click.
const renderInterval = 2 * time.Second

// deferFollowUpDialog defers fn (which itself opens a dialog/overlay, e.g.
// app.Error/Alert/ShowDialog/ShowOverlay) until after the *currently
// executing* ShowDialog/ConfirmDelete button callback's own switch-back-
// to-main has completed. Required any time such a callback needs to show
// a follow-up dialog of its own: opening one directly, synchronously,
// would have that same switch-back immediately hide it again in the same
// tick (indistinguishable from nothing happening) -- and simply wrapping
// the call in app.QueueUpdateDraw without a goroutine would deadlock the
// whole app instead of just misbehaving, since
// tview.Application.QueueUpdate blocks until Run()'s own event loop --
// the very goroutine currently executing this callback -- services it.
// Spawning a goroutine first lets *this* callback return immediately (so
// Run() can loop around and drain the queue), while fn still executes on
// the main goroutine as tview requires.
func deferFollowUpDialog(app *tui.App, fn func()) {
	go app.QueueUpdateDraw(fn)
}

// PendingTable is the bunker board's primary, most-interacted-with pane:
// every currently-pending approval request. Follows client/tui/eventtable.go's
// exact conventions (purple border, SetSelectable, ticker-driven
// QueueUpdateDraw render loop) for visual consistency with apply's own
// boards. Wraps table (the actual selectable grid) in a bordered Flex,
// the same shape client/tui.Logger uses for its own Autoscroll/Wrap
// toggle bar, so this table's own Auto-Prompt toggle state has a
// dedicated row here -- the key hints themselves live in BunkerBoard's
// own (focus-aware) FooterHints, same as every other panel's.
type PendingTable struct {
	*tview.Flex
	table     *tview.Table
	actions   *tview.TextView
	app       *tui.App
	client    BunkerClient
	canDetach bool
	ctx       context.Context // stashed at Init so dialog-opening methods (showBunkerURI's pairing watcher) can spawn a goroutine tied to the board's own lifetime, not just the overlay's

	mu       sync.Mutex
	rendered []Pending

	// sessionLabels caches pubkey -> labelFor(session), refreshed each
	// Update tick (the same ListSessions fetch SessionsTable.Update
	// already does for its own App column) -- appLabel's own backing
	// store. Cached here, rather than fetched fresh, because
	// openApprovalDialog isn't only ever reached right after an Update:
	// tryPromptNext and the manual-Enter path (SetSelectedFunc) both call
	// it directly, and a synchronous ListSessions round-trip on every
	// dialog open is worse than a tick-stale label.
	sessionLabels map[string]string

	// autoPrompt, when true, automatically opens openApprovalDialog for
	// the oldest pending request the moment nothing else is already being
	// shown -- chaining straight through connect -> get_public_key ->
	// sign_event -> ... without the operator having to hunt down and
	// select each row by hand. Mirrors client/tui.Logger's own Autoscroll
	// toggle: on by default, one key ('p') to flip it off and fall back
	// to fully manual (Enter/'a'/'r') triage. promptedID is the request
	// currently shown (auto- or manually-opened alike -- see
	// openApprovalDialog), guarding against a background poll tick
	// stacking a second dialog on top of one the operator is already
	// looking at; "" means nothing is currently shown.
	autoPrompt bool
	promptedID string
}

func NewPendingTable(app *tui.App, client BunkerClient, canDetach bool) *PendingTable {
	return &PendingTable{
		Flex:       tview.NewFlex(),
		table:      tview.NewTable(),
		actions:    tview.NewTextView(),
		app:        app,
		client:     client,
		canDetach:  canDetach,
		autoPrompt: true,
	}
}

func (t *PendingTable) Init(ctx context.Context) *PendingTable {
	t.ctx = ctx
	t.SetBorder(true).
		SetBorderPadding(0, 1, 1, 1)

	t.actions.SetDynamicColors(true).SetTextAlign(tview.AlignCenter)

	t.table.SetFixed(1, 0).
		SetSelectable(true, false)

	tui.WireFocusBorder(t.table, t.Flex.Box)

	t.table.SetSelectedStyle(tcell.Style{}.
		Background(tui.ColorPrimary).
		Foreground(tui.ColorText),
	)

	t.Flex.SetDirection(tview.FlexRow).
		AddItem(t.actions, 1, 0, false).
		AddItem(t.table, 0, 1, true)

	t.updateTitle(0)
	t.updateActionsBar()
	t.drawHeader()

	t.table.SetSelectedFunc(func(row, _ int) {
		if p, ok := t.pendingAt(row); ok {
			t.openApprovalDialog(p)
		}
	})

	// 'a'/'r' are the one-key path for the common no-frills decision --
	// Approve Once / Reject Once on whatever row is selected -- without
	// opening openApprovalDialog first. Enter still opens that dialog,
	// now mainly for its "Always ..." grant flow (still also has its own
	// Approve/Reject Once buttons for parity/discoverability), so a
	// decision that doesn't need remembering is a single keypress instead
	// of select-then-Enter-then-pick-a-button.
	//
	// 'b' (background/stop the daemon) and 'c' (connect a new app) are
	// deliberately *not* bound here -- see BunkerBoard's own
	// SetInputCapture for why they're board-wide keys instead of scoped
	// to whichever panel happens to be focused. Pairing a new app is just
	// as useful to reach from Trusted Apps/Request History/the activity
	// log as from Pending itself, so 'c' moved there rather than only
	// working while this table happened to hold focus.
	//
	// Set on the outer Flex (not t.table) so it fires regardless of which
	// child actually holds real keyboard focus -- see client/tui.Logger's
	// FocusTarget doc comment for why a Flex's own SetInputCapture still
	// runs even though Tab-cycling focuses t.table (via this type's own
	// FocusTarget below), not this wrapper.
	t.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() != tcell.KeyRune {
			return event
		}
		switch event.Rune() {
		case 'a':
			t.quickResolve(Allow)
			return nil
		case 'r':
			t.quickResolve(Deny)
			return nil
		case 'p':
			t.toggleAutoPrompt()
			return nil
		}
		return event
	})

	t.Update()
	go t.render(ctx)
	return t
}

// FocusTarget returns the actual selectable primitive within this
// PendingTable -- its inner *tview.Table, not the outer Flex wrapper --
// for the same reason client/tui.Logger.FocusTarget exists: Tab-cycling
// (BunkerBoard.Childs) needs the real focusable leaf, not a Box with no
// row-selection state of its own.
func (t *PendingTable) FocusTarget() tview.Primitive {
	return t.table
}

// quickResolve is the one-key path behind 'a'/'r': resolve whatever row is
// currently selected as a one-off Approve/Reject, same as the dialog's own
// "Approve Once"/"Reject Once" buttons -- never remembers a grant (nil),
// that's still what Enter's dialog is for. A no-op if nothing's selected
// (e.g. the table is empty).
func (t *PendingTable) quickResolve(verdict Decision) {
	row, _ := t.table.GetSelection()
	if p, ok := t.pendingAt(row); ok {
		t.resolve(p.ID, verdict, nil)
	}
}

// toggleAutoPrompt flips the Auto-Prompt toggle (see the PendingTable doc
// comment) and immediately tries to show a dialog if it just turned on
// and something's already waiting -- so flipping it back on doesn't sit
// idle for up to renderInterval before catching up.
func (t *PendingTable) toggleAutoPrompt() {
	t.mu.Lock()
	t.autoPrompt = !t.autoPrompt
	auto := t.autoPrompt
	t.mu.Unlock()

	t.updateActionsBar()
	if auto {
		t.tryPromptNext()
	}
}

// openConnectDialog is the TUI's entry point for pairing a new app --
// otherwise only reachable via a separate `ncli bunker connect` in
// another terminal. Offers both connection directions: this signer
// generating a bunker:// URI to hand to a client (client speaks first),
// or pasting in a nostrconnect:// URI a client already generated (this
// signer speaks first).
func (t *PendingTable) openConnectDialog() {
	t.app.ShowDialog(
		"Connect a new app",
		"Pair with a client that speaks first (bunker://), or one that\nalready generated its own nostrconnect:// URI.",
		tcell.ColorDefault,
		[]string{"Show bunker:// URI", "Enter nostrconnect:// URI", "Cancel"},
		// Both branches open another dialog/overlay of their own -- see
		// deferFollowUpDialog's doc comment for why that needs deferring
		// through a goroutine, not a direct call.
		func() { deferFollowUpDialog(t.app, t.showBunkerURI) },
		func() { deferFollowUpDialog(t.app, t.openNostrconnectInput) },
		func() {},
	)
}

// showBunkerURI generates a fresh single-use pairing secret and displays
// the resulting bunker:// URI for the connecting client. This never
// blocks on network I/O, unlike the nostrconnect direction below, so it's
// safe to call synchronously. Uses ShowOverlay (not ShowDialog) for two
// reasons: a "Copy" button needs somewhere to show its own confirmation
// without opening yet another nested dialog, and the URI itself is shown
// as its own dedicated, unwrapped, border-free line -- unlike text inside
// a Modal, this is cleanly selectable by dragging in the user's terminal
// even when the automatic copy (copyToClipboard: OSC 52 + a native
// clipboard tool) doesn't apply to their terminal/environment.
func (t *PendingTable) showBunkerURI() {
	uri, err := t.client.Connect("", nil)
	if err != nil {
		t.app.Error(fmt.Sprintf("failed to generate a pairing URI: %s", err))
		return
	}
	// Connect("") just armed the secret Handler-side for PairingSecretTTL
	// (handler.go's SetPendingSecret) -- computed independently here rather
	// than threaded back through BunkerClient.Connect's return value
	// (shared with the unrelated nostrconnect direction, which returns
	// "paired" instead of a URI) since the two clocks are the same call
	// chain apart, local or over the IPC socket alike.
	deadline := time.Now().Add(PairingSecretTTL)

	const overlayKey = "bunkerShowURI"

	uriView := tview.NewTextView().
		SetText(uri).
		SetTextColor(tui.ColorText)

	// waiting is this dialog's whole reason for existing beyond a plain
	// static URI display: pairing otherwise gives no feedback at all --
	// the human has to tab away, guess whether the client's "connect" ever
	// arrived or the secret just expired unused, and hunt for it in
	// Pending Requests. Polling for it here instead answers "did that just
	// work, or do I need a new link?" without leaving the dialog.
	waiting := tview.NewTextView().SetDynamicColors(true)
	fmt.Fprint(waiting, formatWaitingStatus(deadline))

	status := tview.NewTextView().SetDynamicColors(true)

	stopWaiting := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopWaiting) }) }

	dismiss := func() {
		stop()
		t.app.DismissOverlay(overlayKey)
	}

	form := tview.NewForm().
		SetButtonsAlign(tview.AlignCenter).
		SetButtonBackgroundColor(tcell.ColorDefault).
		// SetButtonBackgroundColor alone also (mis)sets the *focused*
		// button's own text color to the same ColorDefault it just gave
		// the unfocused background (tview's Form.SetButtonBackgroundColor
		// mutates buttonActivatedStyle's foreground too, on the
		// assumption it's always paired with a SetButtonTextColor call) --
		// left uncorrected, a focused button renders as barely-visible
		// default-colored text on tview's own stock white activated
		// background, not this app's black/purple/white palette. Explicit
		// SetButtonActivatedStyle overrides both, matching every table's
		// own SetSelectedStyle "this is selected" convention.
		SetButtonActivatedStyle(tcell.Style{}.Background(tui.ColorPrimary).Foreground(tui.ColorText))
	form.AddButton("Copy", func() {
		copyToClipboard(uri)
		status.SetText(fmt.Sprintf("[%s:-:-]Attempted to copy to your clipboard -- if that didn't work, select the line above manually.", tui.ColorSuccess))
	})
	form.AddButton("Close", dismiss)
	form.SetCancelFunc(dismiss)
	form.SetBackgroundColor(tcell.ColorDefault)

	view := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(tview.NewTextView().SetText("Share this with the connecting app:"), 1, 0, false).
		AddItem(uriView, 3, 0, false).
		AddItem(waiting, 1, 0, false).
		AddItem(status, 2, 0, false).
		AddItem(form, 3, 0, true)
	// No SetBorderPadding here -- see overlaySpacer's own doc comment:
	// a Box's own border-padding is the same kind of gap the nil-Flex-
	// margin bug was, and was independently found to bleed the exact
	// same way under this board's real concurrent redraw traffic once a
	// dialog like this one (a bordered Box containing the focused Form)
	// is open long enough for other panels' tickers to redraw around it.
	view.SetBorder(true).
		SetBorderColor(tui.ColorPrimary).
		SetTitle(" bunker:// pairing URI ").
		SetBackgroundColor(tcell.ColorDefault)

	// A real modal, not a full-screen page: the board (Trusted Apps,
	// Request History, ...) stays visible and live around this small
	// dialog, the same as a real GUI modal only dims/covers part of the
	// screen -- see App.ShowPositionedOverlay's own doc comment. Sized to
	// this dialog's actual content: 1+3+1+2+3 = 10 fixed content rows
	// above, plus 2 border rows (top+bottom) = 12 -- not a screen
	// percentage (would leave a large dead gap below on any reasonably
	// tall terminal), and deliberately not a row more than that exact sum
	// either (see the 14-vs-12 bug this replaced, below).
	//
	// Full width (100), not a percentage with margin either side: a
	// narrower dialog left just enough margin on a wide terminal for a
	// table column or two to peek through unreadably (a stray "d625821c"/
	// "APP" fragment sitting right next to the dialog's own border,
	// reported as "we see the request history behind [it]") without
	// actually being legible or useful -- board-visible-around-it is the
	// intent for the *rows* above/below this fixed-height dialog (those
	// stay whole, clean rows), not for a sliver of *columns* beside it.
	// Zero margin removes the sliver outright rather than just narrowing
	// it, and needs no SetBorderPadding to do it -- padding was already
	// independently found to bleed the exact same way (see the comment
	// above view.SetBorder), so it's not an option here either.
	//
	// heightRows used to be 14, two more than the 12 the Flex's own
	// AddItem calls above actually claim (1+3+1+2+3 content + 2 border).
	// Those 2 unclaimed rows were live, reproduced proof that
	// view.Box.DrawForSubclass painting its own background does *not*
	// reliably cover a Flex's full assigned rect when its children's own
	// fixed sizes sum to less than that -- Request History showed straight
	// through exactly those 2 rows and nowhere else, every time. Same
	// family of bug as the nil-Flex-spacer margin bleed overlaySpacer
	// exists for, different trigger (unclaimed slack height here, not a
	// nil item there); same fix in spirit -- don't leave the gap for
	// something else to show through, whether that's a bare nil item or
	// slack no child claims.
	x, y, w, h := positionedOverlayRectFixedHeight(t.app, 100, 12)
	view.SetRect(x, y, w, h)
	t.app.ShowPositionedOverlay(overlayKey, view, form)

	go t.watchForPairing(t.ctx, stopWaiting, waiting, deadline, dismiss)
}

// watchForPairing polls ListPending (the same mechanism PendingTable's own
// render loop already uses) once a second for a not-yet-decided "connect"
// request -- the signal that a client used the bunker:// URI this dialog
// is showing. handler.go's Handle sends this through the normal approval
// queue the instant the secret checks out, so it shows up here well
// within a poll tick; there's no other way a "connect"-method Pending
// entry gets created. The moment that happens, this closes the URI dialog
// itself (dismiss) and hands straight off to the approval flow
// (tryPromptNext) instead of just saying "a client connected, go find it
// in Pending Requests" and leaving the operator to do that by hand --
// mirroring PendingTable's own doc comment on why the connect flow
// shouldn't dead-end on a static confirmation. Every tick otherwise
// refreshes the countdown against deadline (handler.go enforces the same
// PairingSecretTTL Handler-side, so this is a truthful readout, not just
// cosmetic) -- once past it with no connect ever having landed, the
// secret is truly dead (Handle would reject it even if a client showed up
// right now), so this stops for good rather than ticking a countdown that
// no longer means anything. Also stops as soon as a connect is found
// (single-use secret, nothing further to watch for), when stop fires (the
// dialog was closed), or when ctx is cancelled (the board itself is
// tearing down).
func (t *PendingTable) watchForPairing(ctx context.Context, stop <-chan struct{}, waiting *tview.TextView, deadline time.Time, dismiss func()) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			pending, err := t.client.ListPending()
			if err == nil && hasPendingConnect(pending) {
				t.app.QueueUpdateDraw(func() {
					dismiss()
					t.tryPromptNext()
				})
				return
			}

			status := formatWaitingStatus(deadline)
			t.app.QueueUpdateDraw(func() {
				waiting.Clear()
				fmt.Fprint(waiting, status)
			})
			if time.Now().After(deadline) {
				return
			}
		case <-stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

// formatWaitingStatus renders showBunkerURI's live status line: a
// countdown to deadline while the secret's still armed, or an explicit
// expired state once it's passed with nobody having connected -- directly
// answering "is this still good, or do I need a new one" instead of
// leaving that to be guessed at.
func formatWaitingStatus(deadline time.Time) string {
	if time.Now().After(deadline) {
		return fmt.Sprintf("[%s]This link expired with no client connecting -- press 'c' again for a new one.[-:-:-]", tui.ColorDanger)
	}
	return fmt.Sprintf("[%s]Waiting for a client to connect... (expires in %s)[-:-:-]", tui.ColorMuted, formatCountdown(deadline))
}

// hasPendingConnect reports whether pending contains a not-yet-decided
// "connect" request -- watchForPairing's whole signal.
func hasPendingConnect(pending []Pending) bool {
	for _, p := range pending {
		if p.Method == nip46.MethodConnect {
			return true
		}
	}
	return false
}

// openNostrconnectInput opens a text-input overlay (ShowOverlay, since
// ShowDialog's fixed N-button Modal can't hold an InputField) for pasting
// a nostrconnect:// URI, mirroring client/tui/eventdialog.go's own
// Form-based overlay pattern. Submitting runs Connect in its own
// goroutine: unlike the bunker:// direction, this blocks on the network
// waiting for the client to confirm (up to ~60s) -- calling it directly
// from the input handler would freeze the whole single-threaded tview
// event loop for that long.
func (t *PendingTable) openNostrconnectInput() {
	const overlayKey = "bunkerConnectInput"

	input := tview.NewInputField().
		SetLabel("nostrconnect:// URI: ").
		SetFieldWidth(0)

	dismiss := func() { t.app.DismissOverlay(overlayKey) }

	submit := func() {
		uri := strings.TrimSpace(input.GetText())
		dismiss()
		if uri == "" {
			return
		}
		t.showConnecting(uri)
	}

	// Form re-applies its own label/field colors to every item on each
	// Draw (see form.go's SetFormAttributes call), so recoloring has to go
	// through the Form's own setters -- setting them on the InputField
	// directly gets silently overwritten. tview's defaults here are
	// Styles.ContrastBackgroundColor (a plain blue field) and
	// Styles.SecondaryTextColor (a yellow label), neither of which appears
	// anywhere else in ncli's purple/black/white palette; recoloring to
	// the same purple+white pairing table.go/eventtable.go already use for
	// a selected row keeps this, the TUI's only InputField, on-theme.
	form := tview.NewForm().
		SetButtonsAlign(tview.AlignCenter).
		SetButtonBackgroundColor(tcell.ColorDefault).
		// See showBunkerURI's own comment on why SetButtonActivatedStyle
		// has to be explicit here too -- SetButtonBackgroundColor alone
		// leaves a focused button's text near-invisible.
		SetButtonActivatedStyle(tcell.Style{}.Background(tui.ColorPrimary).Foreground(tui.ColorText)).
		SetLabelColor(tui.ColorText).
		SetFieldBackgroundColor(tui.ColorPrimary).
		SetFieldTextColor(tui.ColorText)
	form.AddFormItem(input)
	form.AddButton("Connect", submit)
	form.AddButton("Cancel", dismiss)
	form.SetCancelFunc(dismiss)
	form.SetBackgroundColor(tcell.ColorDefault)
	wireButtonArrowNav(form, input)

	view := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true)
	view.SetBorder(true).
		SetBorderColor(tui.ColorPrimary).
		SetTitle(" Paste nostrconnect:// URI ").
		SetBackgroundColor(tcell.ColorDefault)

	// A real modal, not a full-screen page -- see showBunkerURI's own
	// comment on ShowPositionedOverlay for why. Full width for the same
	// reason showBunkerURI itself now is: a narrower dialog left a margin
	// on each side just wide enough for an unreadable fragment of
	// whatever board table sits underneath to peek through.
	x, y, w, h := positionedOverlayRect(t.app, 100, 20)
	view.SetRect(x, y, w, h)
	t.app.ShowPositionedOverlay(overlayKey, view, form)
}

// showConnecting is openNostrconnectInput's submit follow-up: a status
// overlay for the up-to-a-minute network wait while the client confirms.
// Deliberately its own ShowPositionedOverlay page, not the shared "dialog"
// page Alert/Error/ShowDialog all funnel through (App.showDialog's single
// a.dialog field) -- Auto-Prompt can legitimately pop an unrelated approval
// dialog onto that shared page while this wait is still in flight, and this
// call used to hand off through App.Alert + App.DismissDialog
// (a.pages.SwitchToPage("main")), which closes whatever currently occupies
// "dialog" with no regard for what that is. If Auto-Prompt's dialog had
// taken it over in the meantime, that silently discarded an operator's
// in-progress approve/reject decision on some unrelated request -- no
// error, no trace, and PendingTable.promptedID left stuck pointing at it,
// wedging Auto-Prompt for good (nothing else ever prompts again, with no
// visible sign why). A dedicated overlay key sidesteps the collision
// entirely: DismissOverlay only ever touches its own page, and (per its own
// doc comment) no longer steals focus back to the board if something else
// raced onto "dialog" in the meantime either.
func (t *PendingTable) showConnecting(uri string) {
	const overlayKey = "bunkerConnecting"
	dismiss := func() { t.app.DismissOverlay(overlayKey) }

	status := tview.NewTextView().
		SetText("Connecting... this can take up to a minute while the client confirms.").
		SetTextColor(tui.ColorText).
		SetWordWrap(true)

	form := tview.NewForm().
		SetButtonsAlign(tview.AlignCenter).
		SetButtonBackgroundColor(tcell.ColorDefault).
		// See showBunkerURI's own comment on why this is needed.
		SetButtonActivatedStyle(tcell.Style{}.Background(tui.ColorPrimary).Foreground(tui.ColorText))
	form.AddButton("OK", dismiss)
	form.SetCancelFunc(dismiss)
	form.SetBackgroundColor(tcell.ColorDefault)

	view := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(status, 2, 0, false).
		AddItem(form, 3, 0, true)
	view.SetBorder(true).
		SetBorderColor(tui.ColorPrimary).
		SetTitle(" Connecting ").
		SetBackgroundColor(tcell.ColorDefault)

	x, y, w, h := positionedOverlayRectFixedHeight(t.app, 100, 7)
	view.SetRect(x, y, w, h)
	t.app.ShowPositionedOverlay(overlayKey, view, form)

	go func() {
		_, err := t.client.Connect(uri, nil)
		t.app.QueueUpdateDraw(func() {
			dismiss()
			if err != nil {
				t.app.Error(fmt.Sprintf("pairing failed: %s", err))
				return
			}
			// Success: don't dead-end on a static "Paired successfully" OK
			// box the operator has to click through for no reason -- hand
			// straight off to the approval flow, same as showBunkerURI's
			// own watchForPairing does for the other pairing direction.
			// tryPromptNext no-ops harmlessly if the client hasn't sent its
			// first real request yet, or if something else already claimed
			// the prompt slot while this was in flight.
			t.tryPromptNext()
		})
	}()
}

// overlaySpacer is a blank margin cell for centerOverlay/
// centerOverlayFixedHeight -- a real Box, not a nil Flex item. A nil item
// is never itself drawn at all (tview.Flex skips it outright), leaving
// that margin's fill entirely up to the ancestor Flex's own one-time
// background fill from *its* Draw call. That's fine for a single,
// isolated Draw, but this board redraws constantly (every panel runs its
// own render() ticker independently, all calling QueueUpdateDraw), and
// under that real concurrent redraw traffic the margin was observed to
// keep showing stale content from whatever's underneath instead of
// staying blank -- board.go's own overlays are the first ones in this
// codebase to sit on top of a screen that's constantly, independently
// repainting itself, which is exactly the condition that exposes this.
// A real Box gets its own Draw call (and so its own background fill)
// every single redraw, the same as every other primitive on screen,
// removing the dependency on an ancestor's fill alone.
func overlaySpacer() *tview.Box {
	return tview.NewBox().SetBackgroundColor(tcell.ColorDefault)
}

// centerOverlay centers box within blank margin spacers (see
// overlaySpacer) sized to leave widthPercent/heightPercent of the screen
// for it -- the same technique client/tui/eventdialog.go's own
// (unexported) centerBox uses, duplicated here rather than exported
// across the package boundary for a handful of lines.
func centerOverlay(box tview.Primitive, widthPercent, heightPercent int) tview.Primitive {
	widthRest := (100 - widthPercent) / 2
	heightRest := (100 - heightPercent) / 2

	row := tview.NewFlex().
		AddItem(overlaySpacer(), 0, widthRest, false).
		AddItem(box, 0, widthPercent, true).
		AddItem(overlaySpacer(), 0, widthRest, false)

	return tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(overlaySpacer(), 0, heightRest, false).
		AddItem(row, 0, heightPercent, true).
		AddItem(overlaySpacer(), 0, heightRest, false)
}

// centerOverlayFixedHeight centers box the same way centerOverlay does
// horizontally (widthPercent of the screen), but vertically at an exact
// row count instead of a percentage of the screen. A short, fixed-content
// dialog sized by screen percentage inherits a dead gap below its content
// on any reasonably tall terminal; a fixed row count hugs the content
// while the two equal-proportion blank spacers around it (see
// overlaySpacer) still keep it vertically centered.
func centerOverlayFixedHeight(box tview.Primitive, widthPercent, height int) tview.Primitive {
	widthRest := (100 - widthPercent) / 2
	row := tview.NewFlex().
		AddItem(overlaySpacer(), 0, widthRest, false).
		AddItem(box, 0, widthPercent, true).
		AddItem(overlaySpacer(), 0, widthRest, false)

	return tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(overlaySpacer(), 0, 1, false).
		AddItem(row, height, 0, true).
		AddItem(overlaySpacer(), 0, 1, false)
}

// screenSize returns the terminal's current dimensions. tview.Application
// has no direct exported getter for this -- ResizeToFullScreen(p) is the
// only public way to read it, so this asks it to size a throwaway Box to
// the full screen and reads the result back, rather than reaching past
// App into its unexported screen field.
func screenSize(app *tui.App) (width, height int) {
	probe := tview.NewBox()
	app.ResizeToFullScreen(probe)
	_, _, width, height = probe.GetRect()
	return
}

// positionedOverlayRect computes a centered rect sized to widthPercent/
// heightPercent of the current screen -- for use with
// App.ShowPositionedOverlay (a real, board-stays-visible modal), not
// centerOverlay (a full-screen page whose own margins have to actively
// stay filled, which is what centerOverlay's own overlaySpacer exists
// for -- unnecessary here, since a directly-positioned, un-resized page
// only ever touches its own small rect, leaving everything else exactly
// as "main" already drew it).
func positionedOverlayRect(app *tui.App, widthPercent, heightPercent int) (x, y, w, h int) {
	screenW, screenH := screenSize(app)
	w = screenW * widthPercent / 100
	h = screenH * heightPercent / 100
	x = (screenW - w) / 2
	y = (screenH - h) / 2
	return
}

// positionedOverlayRectFixedHeight is positionedOverlayRect's fixed-row-
// count counterpart, matching centerOverlayFixedHeight's own reasoning:
// a short, fixed-content dialog sized by screen percentage inherits a
// dead gap below its content on any reasonably tall terminal.
func positionedOverlayRectFixedHeight(app *tui.App, widthPercent, heightRows int) (x, y, w, h int) {
	screenW, screenH := screenSize(app)
	w = screenW * widthPercent / 100
	h = heightRows
	x = (screenW - w) / 2
	y = (screenH - h) / 2
	return
}

// wireScrollCapture makes Up/Down/PageUp/PageDown/Home/End scroll text
// regardless of which button form currently has selected -- form's own
// Tab/Backtab button navigation, Enter-to-activate, and Escape-to-cancel
// are untouched (tview.Form has no left/right button navigation of its
// own to begin with -- see wireButtonArrowNav below). Duplicates
// client/tui/eventdialog.go's own (unexported)
// scroll-capture logic from ShowEvent, the same event-JSON-viewer pattern
// this package's own overlays already lean on (see ColorizeEventJSON's own
// doc comment) -- shared locally, not exported, since two overlays in this
// package need it: openSignEventApprovalDialog below and HistoryTable's
// showEventDetail.
func wireScrollCapture(form *tview.Form, text *tview.TextView) {
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		row, col := text.GetScrollOffset()
		switch event.Key() {
		case tcell.KeyUp:
			text.ScrollTo(row-1, col)
		case tcell.KeyDown:
			text.ScrollTo(row+1, col)
		case tcell.KeyPgUp:
			text.ScrollTo(row-10, col)
		case tcell.KeyPgDn:
			text.ScrollTo(row+10, col)
		case tcell.KeyHome:
			text.ScrollToBeginning()
		case tcell.KeyEnd:
			text.ScrollToEnd()
		default:
			return event
		}
		return nil
	})
}

// wireButtonArrowNav lets Left/Right move between form's buttons the same
// way ShowDialog's tview.Modal-based dialogs already do -- Modal remaps
// arrow keys to Tab/Backtab itself (see rivo/tview's modal.go), but a
// plain tview.Form (what every ShowOverlay dialog in this file uses,
// since Modal can't hold an InputField) has no such mapping: Button.
// InputHandler only ever reacts to Enter/Tab/Backtab/Escape, so Left/
// Right silently do nothing on a focused button. skip is the form's own
// InputField -- while it has focus, Left/Right must keep moving the text
// cursor, not jump to a button, so the remap only applies once focus has
// moved past it.
func wireButtonArrowNav(form *tview.Form, skip tview.Primitive) {
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if skip.HasFocus() {
			return event
		}
		switch event.Key() {
		case tcell.KeyLeft:
			return tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone)
		case tcell.KeyRight:
			return tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
		default:
			return event
		}
	})
}

func (t *PendingTable) pendingAt(row int) (Pending, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	idx := row - 1
	if idx < 0 || idx >= len(t.rendered) {
		return Pending{}, false
	}
	return t.rendered[idx], true
}

var pendingTableHeaders = []string{"Time", "App", "Method", "Kind", "Expires"}

func (t *PendingTable) drawHeader() {
	for c, name := range pendingTableHeaders {
		t.table.SetCell(0, c, tview.NewTableCell(fmt.Sprintf("[-:-:b]%s", strings.ToUpper(name))).
			SetExpansion(1).
			SetTextColor(tui.ColorMuted).
			SetAlign(tview.AlignLeft).
			SetSelectable(false))
	}
}

func (t *PendingTable) updateTitle(count int) {
	t.SetTitle(fmt.Sprintf(" [::b][%s]PENDING REQUESTS [[%s]%d[%s]] ",
		tui.ColorPrimary, tui.ColorBadge, count, tui.ColorPrimary))
}

// updateActionsBar redraws the hint row above the table with the
// Auto-Prompt toggle's current On/Off state -- styled the same purple-
// label/colored-value way client/tui.Logger's own Autoscroll/Wrap bar
// uses (StatusText). Key hints (including 'p', the key that flips this)
// live in BunkerBoard's own FooterHints instead, the same state-here/
// keys-in-the-footer split Logger's own bar uses. Called at Init and
// whenever the toggle itself changes -- not on every render tick, since
// the state it displays doesn't change on its own.
func (t *PendingTable) updateActionsBar() {
	t.mu.Lock()
	auto := t.autoPrompt
	t.mu.Unlock()

	autoColor, autoText := tui.StatusText(auto)
	t.actions.Clear()
	fmt.Fprintf(t.actions, "[%s::b]Auto-Prompt:[%s]%s", tui.ColorPrimary, autoColor, autoText)
}

// appLabel resolves pubkey to labelFor(session) against the cache
// Update's own ListSessions fetch last populated, falling back to
// shortHex(pubkey) on a cache miss (a pubkey Update hasn't seen a
// Session for yet, or a nil sessionLabels before the first Update ever
// ran).
func (t *PendingTable) appLabel(pubkey string) string {
	t.mu.Lock()
	label, ok := t.sessionLabels[pubkey]
	t.mu.Unlock()
	if !ok {
		return shortHex(pubkey)
	}
	return label
}

func (t *PendingTable) Update() {
	pending, err := t.client.ListPending()
	if err != nil {
		return // transient IPC hiccup -- next tick retries; keep the last good snapshot on screen
	}

	// Best-effort, same "transient IPC hiccup shouldn't blank the whole
	// render" reasoning SessionsTable.Update already applies to its own
	// History() fetch: a failure here just leaves sessionLabels at
	// whatever it was (or nil, appLabel's own shortHex fallback covers
	// that), not the pending table itself.
	sessions, _ := t.client.ListSessions()
	sessionLabels := make(map[string]string, len(sessions))
	for _, s := range sessions {
		sessionLabels[s.Pubkey] = labelFor(s)
	}

	t.mu.Lock()
	t.rendered = pending
	t.sessionLabels = sessionLabels
	t.mu.Unlock()

	t.table.Clear()
	t.drawHeader()
	t.updateTitle(len(pending))

	for i, p := range pending {
		kindCol := "-"
		if p.Method == nip46.MethodSignEvent {
			kindCol = strconv.Itoa(p.Kind)
		}
		expiresCol := len(pendingTableHeaders) - 1
		cells := []string{
			formatClockTime(p.CreatedAt),
			t.appLabel(p.ClientKey),
			p.Method,
			kindCol,
			formatCountdown(p.ExpiresAt),
		}
		for c, val := range cells {
			cell := tview.NewTableCell(val).
				SetExpansion(1).
				SetAlign(tview.AlignLeft)
			// Urgency color on Expires only -- a glance-at cue for "decide
			// this one now" (the queue's own sweep auto-rejects it once it
			// hits zero) without having to read every row's countdown text.
			if c == expiresCol {
				if color, urgent := urgencyColor(p.ExpiresAt); urgent {
					cell.SetTextColor(color)
				}
			}
			t.table.SetCell(i+1, c, cell)
		}
	}

	t.maybeAutoPrompt()
}

func (t *PendingTable) render(ctx context.Context) {
	ticker := time.NewTicker(renderInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.app.QueueUpdateDraw(func() { t.Update() })
		case <-ctx.Done():
			return
		}
	}
}

// maybeAutoPrompt is Update's own tail call, driving the Auto-Prompt
// toggle's steady-state behavior: every poll tick (see the PendingTable
// doc comment), if nothing's currently shown and there's at least one
// request waiting, open the oldest one. This is the fallback path for
// requests that arrive with nobody having just resolved anything (e.g. a
// fresh request lands while the operator was on a different panel, or the
// toggle only just got flipped back on) -- the immediate-response path
// after an actual resolve is tryPromptNext below, which doesn't wait for
// the next tick.
//
// promptedID is claimed synchronously, right here, but the actual dialog
// open is deferred (deferFollowUpDialog) -- even though every *other*
// maybeAutoPrompt trigger (the render() ticker) already runs safely
// inside its own QueueUpdateDraw callback and wouldn't need this. Update
// (and so this) is also called synchronously from Init, which -- for a
// request already pending the instant the TUI attaches -- runs *inside*
// NewBunkerBoard's own constructor, before the caller ever gets a chance
// to call app.Load. Opening a dialog synchronously at that point adds a
// page to the App's Pages before "main" is even one of them; Load's own
// subsequent bookkeeping (SwitchToPage("main"), which hides every *other*
// page) then silently steals the dialog's focus back to the board once
// Load finally runs, with no visible sign anything went wrong. Deferring
// the open sidesteps this: the goroutine blocks on QueueUpdateDraw until
// Run() actually starts draining its update queue, which can only happen
// after the caller's Load call has already returned. Claiming
// promptedID before deferring (rather than leaving it to
// openApprovalDialog, which still also sets it, for every other caller)
// keeps the existing "Init synchronously shows the only pending request"
// contract intact for anything that only checks promptedID, not real
// focus.
func (t *PendingTable) maybeAutoPrompt() {
	t.mu.Lock()
	ready := t.autoPrompt && t.promptedID == "" && len(t.rendered) > 0
	var p Pending
	if ready {
		p = t.rendered[0]
		t.promptedID = p.ID
	}
	t.mu.Unlock()

	if ready {
		deferFollowUpDialog(t.app, func() { t.openApprovalDialog(p) })
	}
}

// tryPromptNext is maybeAutoPrompt's immediate-response counterpart:
// called right after a request is resolved (see resolve), or when the
// Auto-Prompt toggle is flipped back on (toggleAutoPrompt), or once a
// pairing completes (watchForPairing, openNostrconnectInput's submit) --
// anywhere the next dialog should appear right away rather than waiting
// up to renderInterval for the next poll tick. Re-fetches ListPending
// itself instead of trusting t.rendered, which may still be stale from
// before whatever just happened here. A no-op if Auto-Prompt is off,
// something's already shown, or nothing's waiting.
func (t *PendingTable) tryPromptNext() {
	t.mu.Lock()
	auto := t.autoPrompt
	open := t.promptedID != ""
	t.mu.Unlock()
	if !auto || open {
		return
	}

	pending, err := t.client.ListPending()
	if err != nil || len(pending) == 0 {
		return
	}

	t.mu.Lock()
	// Re-check: something else (a manual Enter, another tryPromptNext
	// racing in from a different trigger) may have claimed the slot while
	// ListPending was in flight.
	if t.promptedID != "" {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()

	t.openApprovalDialog(pending[0])
}

// openApprovalDialog is the "don't ask every time" UX: a two-step dialog
// covering all three grant axes (scope, then duration/budget) without a
// cluttered single screen. Step 1's scope choices adapt to the request's
// method: sign_event gets an exact-kind option (and, unless the kind is
// always-sensitive, a broader any-kind option too -- see policy.go's
// sensitiveKinds); every other method just gets a single "Always". Marks
// p as the one currently being shown (t.promptedID) regardless of how
// this got called -- SetSelectedFunc's manual Enter, or the Auto-Prompt
// chain (maybeAutoPrompt/tryPromptNext) -- so the two never race and pop
// two dialogs on top of each other; resolve and the "Decide Later" button
// below both clear it again once this one's actually done with.
//
// "Decide Later" is deliberately the *last* button: Dialog's own Esc
// convention (client/tui/dialog.go) invokes whichever func is last, so
// before this it was "Reject Always" -- pressing Esc to back out of a
// dialog you didn't mean to open yet silently denied the app forever.
// That's especially dangerous now that this dialog can pop up
// unattended (Auto-Prompt): the safe, reversible action has to be the
// one an instinctive Esc lands on.
func (t *PendingTable) openApprovalDialog(p Pending) {
	t.mu.Lock()
	t.promptedID = p.ID
	t.mu.Unlock()

	kindLabel := ""
	if p.Method == nip46.MethodSignEvent {
		kindLabel = fmt.Sprintf(" (kind %d)", p.Kind)
	}
	title := "Approve request?"
	text := fmt.Sprintf("App %s\nwants to: %s%s", t.appLabel(p.ClientKey), p.Method, kindLabel)

	buttons := []string{"Approve Once"}
	funcs := []func(){
		func() { t.resolve(p.ID, Allow, nil) },
	}

	if p.Method == nip46.MethodSignEvent {
		kind := p.Kind
		buttons = append(buttons, fmt.Sprintf("Always: kind %d", kind))
		funcs = append(funcs, func() { deferFollowUpDialog(t.app, func() { t.openDurationDialog(p, &kind) }) })

		if !sensitiveKinds[p.Kind] {
			buttons = append(buttons, "Always: any kind")
			funcs = append(funcs, func() { deferFollowUpDialog(t.app, func() { t.openDurationDialog(p, nil) }) })
		}
	} else {
		buttons = append(buttons, "Always")
		funcs = append(funcs, func() { deferFollowUpDialog(t.app, func() { t.openDurationDialog(p, nil) }) })
	}

	buttons = append(buttons, "Reject Once", "Reject Always", "Decide Later")
	funcs = append(funcs,
		func() { t.resolve(p.ID, Deny, nil) },
		func() {
			grant := DenyAlways(p.Method, time.Now())
			t.resolve(p.ID, Deny, &grant)
		},
		func() { t.deferDecision(p.ID) },
	)

	// sign_event gets the event's own unsigned JSON displayed above this
	// same button set, so the operator can see exactly what they're about
	// to authorize instead of approving blind on method+kind alone --
	// ShowDialog's Modal has no way to embed that, so this is a custom
	// overlay instead (see openSignEventApprovalDialog). p.Event is nil
	// only if this Pending somehow predates that field being wired
	// (Handler.Handle has set it unconditionally for sign_event since
	// Event was added to Pending) -- falls back to the plain dialog rather
	// than showing an empty JSON view in that unreachable-in-practice case.
	if p.Method == nip46.MethodSignEvent && p.Event != nil {
		t.openSignEventApprovalDialog(p, title, text, buttons, funcs)
		return
	}

	t.app.ShowDialog(title, text, tcell.ColorDefault, buttons, funcs...)
}

// openSignEventApprovalDialog is openApprovalDialog's sign_event variant:
// same title/text/buttons/funcs computed there, laid out as a custom
// ShowOverlay (Form-based, mirroring showBunkerURI/openNostrconnectInput)
// with the unsigned event's JSON displayed above the button row instead of
// ShowDialog's fixed Modal, which has no room for arbitrary content.
// Buttons dismiss this overlay before running their func, not after (unlike
// ShowDialog's own wrapper) -- so any of them (e.g. openDurationDialog) can
// safely open a follow-up dialog/overlay of its own synchronously, the same
// "Form's own button handler, no wrapper" case deferFollowUpDialog's own
// doc comment carves out.
func (t *PendingTable) openSignEventApprovalDialog(p Pending, title, text string, buttons []string, funcs []func()) {
	const overlayKey = "signEventApproval"
	dismiss := func() { t.app.DismissOverlay(overlayKey) }

	jsonView := tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true).
		SetScrollable(true)
	jsonView.SetBorderPadding(0, 0, 0, 0)
	fmt.Fprint(jsonView, tui.ColorizeEventJSON(p.Event))
	jsonView.ScrollToBeginning()

	form := tview.NewForm().
		SetButtonsAlign(tview.AlignCenter).
		SetButtonBackgroundColor(tcell.ColorDefault).
		// See showBunkerURI's own comment on why this is needed -- this is
		// the approve/reject overlay itself, so it's the one most worth
		// getting right.
		SetButtonActivatedStyle(tcell.Style{}.Background(tui.ColorPrimary).Foreground(tui.ColorText))
	for i, label := range buttons {
		fn := funcs[i]
		form.AddButton(label, func() {
			dismiss()
			fn()
		})
	}
	// Esc invokes the last button (Decide Later) -- same convention
	// Dialog's own Esc handling uses, and for the same reason
	// openApprovalDialog's own doc comment gives: this can appear
	// unattended via Auto-Prompt, so an instinctive Esc must land on the
	// safe, reversible choice, not silently reject the request forever.
	last := funcs[len(funcs)-1]
	form.SetCancelFunc(func() {
		dismiss()
		last()
	})
	form.SetBackgroundColor(tcell.ColorDefault)
	wireScrollCapture(form, jsonView)

	header := tview.NewTextView().SetDynamicColors(true)
	fmt.Fprintf(header, "[::b]%s[-:-:-]\n%s", tview.Escape(title), tview.Escape(text))

	view := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 2, 0, false).
		AddItem(jsonView, 0, 1, false).
		AddItem(form, 3, 0, true)
	// No SetBorderPadding here -- see overlaySpacer's own doc comment: a
	// Box's own border-padding is the same kind of gap the nil-Flex-margin
	// bug was, and independently bleeds the exact same way under this
	// board's real concurrent redraw traffic (form is the focused child
	// here, which makes view itself part of tview.Flex's own
	// focused-item-deferred-draw path -- the padding gap, unlike the
	// content area proper, was observed to not reliably get its own fill
	// applied while that's happening).
	view.SetBorder(true).
		SetBorderColor(tui.ColorPrimary).
		SetTitle(" Approve request? ").
		SetBackgroundColor(tcell.ColorDefault)

	// Full screen (100/100), not the usual centered-with-a-margin look
	// (see e.g. openNostrconnectInput's 80/20) -- this is a large,
	// content-heavy view (the full unsigned event JSON), and even with
	// overlaySpacer's fix for the margin itself never actually being
	// blank (see its own doc comment), a margin here still meant part of
	// the board underneath stayed glimpsable around the edges. No margin
	// at all removes that entirely, not just papers over it.
	t.app.ShowOverlay(overlayKey, centerOverlay(view, 100, 100), form)
}

// deferDecision backs "Decide Later": leave p undecided (still pending,
// nothing sent to the daemon) and switch off Auto-Prompt rather than
// leaving it on -- with the chain left running, the very next poll tick
// would just see the same still-undecided request back at the front of
// the queue and pop it right back up, turning "not right now" into an
// unclosable loop. Switching to fully manual control is what the operator
// actually wants out of "later": handle it themselves, in their own time,
// via Enter/'a'/'r' on whichever row they choose.
func (t *PendingTable) deferDecision(id string) {
	t.mu.Lock()
	if t.promptedID == id {
		t.promptedID = ""
	}
	t.autoPrompt = false
	t.mu.Unlock()

	t.updateActionsBar()
}

// openDurationDialog is the approval dialog's step 2: how long an
// "Always" grant lasts. kind is nil for an any-kind grant, or the exact
// kind for a kind-scoped one (both established by step 1). Cancel backs
// out to step 1 for the same p rather than clearing t.promptedID and
// leaving nothing shown -- p is still exactly as undecided as it was
// before "Always: ..." was clicked, so the natural move is straight back
// to openApprovalDialog, not treating this as a resolved/dismissed slot
// the Auto-Prompt chain should now move on from.
func (t *PendingTable) openDurationDialog(p Pending, kind *int) {
	now := time.Now()
	buttons := []string{"1 hour", "24 hours", "7 days", "Until revoked", "Next 10 uses", "Cancel"}
	funcs := []func(){
		func() { g := GrantForDuration(p.Method, kind, time.Hour, now); t.resolve(p.ID, Allow, &g) },
		func() { g := GrantForDuration(p.Method, kind, 24*time.Hour, now); t.resolve(p.ID, Allow, &g) },
		func() { g := GrantForDuration(p.Method, kind, 7*24*time.Hour, now); t.resolve(p.ID, Allow, &g) },
		func() { g := GrantForever(p.Method, kind, now); t.resolve(p.ID, Allow, &g) },
		func() { g := GrantForUses(p.Method, kind, 10, now); t.resolve(p.ID, Allow, &g) },
		func() { deferFollowUpDialog(t.app, func() { t.openApprovalDialog(p) }) },
	}
	t.app.ShowDialog("How long should this be allowed?", "", tcell.ColorDefault, buttons, funcs...)
}

// resolve sends id's verdict to the daemon, clears t.promptedID once it
// was the one shown for id, and -- on success -- hands off to
// tryPromptNext so the next request (if Auto-Prompt is on and one's
// waiting) appears right away rather than up to renderInterval later.
// deferFollowUpDialog is required here since resolve always runs inside
// a ShowDialog button callback (openApprovalDialog's or
// openDurationDialog's own funcs): opening tryPromptNext's dialog
// synchronously would have ShowDialog's own switch-back-to-main (which
// runs right after this callback returns) immediately hide it again.
func (t *PendingTable) resolve(id string, verdict Decision, remember *Grant) {
	var err error
	if verdict == Allow {
		err = t.client.Approve(id, remember)
	} else {
		err = t.client.Reject(id, remember)
	}

	t.mu.Lock()
	if t.promptedID == id {
		t.promptedID = ""
	}
	t.mu.Unlock()

	if err != nil {
		deferFollowUpDialog(t.app, func() { t.app.Error(fmt.Sprintf("failed to resolve request: %s", err)) })
		return
	}
	deferFollowUpDialog(t.app, t.tryPromptNext)
}

// SessionsTable is the bunker board's glance-at pane: every app with a
// remembered grant. Enter opens openGrantsOverlay for the selected row --
// per-grant detail and (there) surgical revoke/extend; 'r' opens the same
// confirm-delete pattern client/tui's own Table uses for its 'd' key, but
// for every grant the app holds at once. Both are shown as key hints in
// BunkerBoard's own (focus-aware) FooterHints while this panel is
// focused, not in a dedicated row of its own the way PendingTable's
// Auto-Prompt toggle state is (there's no state to show here, just the
// keys).
type SessionsTable struct {
	*tview.Table
	app    *tui.App
	client BunkerClient

	mu       sync.Mutex
	rendered []Session

	// onCountChange, if set, is called at the end of every Update with the
	// freshly-fetched session count -- NewBunkerBoard's hook back to the
	// containing Flex so this panel's own height (see sessionsHeightFor)
	// stays correct as sessions are added/revoked over the life of a long-
	// running board, not just whatever it happened to be at construction.
	onCountChange func(int)
}

func NewSessionsTable(app *tui.App, client BunkerClient) *SessionsTable {
	return &SessionsTable{Table: tview.NewTable(), app: app, client: client}
}

func (t *SessionsTable) Init(ctx context.Context) *SessionsTable {
	t.SetFixed(1, 0).
		SetSelectable(true, false).
		SetBorder(true).
		SetBorderPadding(0, 1, 1, 1)

	tui.WireFocusBorder(t.Table, t.Table.Box)

	t.SetSelectedStyle(tcell.Style{}.
		Background(tui.ColorPrimary).
		Foreground(tui.ColorText),
	)

	t.updateTitle(0)
	t.drawHeader()

	// Enter drills into the selected app's own grants, the same "detail
	// view for the focused row" convention PendingTable's Enter (the
	// approval dialog) and HistoryTable's Enter (event JSON) already use
	// -- see openGrantsOverlay's own doc comment.
	t.SetSelectedFunc(func(row, _ int) {
		if s, ok := t.sessionAt(row); ok {
			t.openGrantsOverlay(s)
		}
	})

	t.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyRune && event.Rune() == 'r' {
			row, _ := t.GetSelection()
			if s, ok := t.sessionAt(row); ok {
				t.app.ConfirmDelete(fmt.Sprintf("Revoke all permissions for %s?", labelFor(s)), func() {
					if _, err := t.client.Revoke(s.Pubkey); err != nil {
						deferFollowUpDialog(t.app, func() { t.app.Error(fmt.Sprintf("failed to revoke: %s", err)) })
					}
				})
			}
			return nil
		}
		if event.Key() == tcell.KeyRune && event.Rune() == 'n' {
			row, _ := t.GetSelection()
			if s, ok := t.sessionAt(row); ok {
				t.openRenameInput(s)
			}
			return nil
		}
		return event
	})

	// Fetched synchronously (matching IdentityBar's own Init) rather than
	// leaving t.rendered at its zero value until the first render tick, up
	// to renderInterval later: onCountChange (set by NewBunkerBoard before
	// this Init call) needs a real count right away so the panel's height
	// is correct from the first frame, not just eventually.
	t.Update()
	go t.render(ctx)
	return t
}

func (t *SessionsTable) sessionAt(row int) (Session, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	idx := row - 1
	if idx < 0 || idx >= len(t.rendered) {
		return Session{}, false
	}
	return t.rendered[idx], true
}

// openRenameInput opens a text-input overlay (same ShowPositionedOverlay/
// Form/InputField construction as PendingTable's own
// openNostrconnectInput) for setting s's Nickname -- the "n" shortcut's
// target. Pre-fills with the current Nickname if one's already set, but
// deliberately not with s.AppName otherwise: saving that back unedited
// would silently turn summarizeApp's own "Name (url)" parenthetical into
// the user's own nickname instead of leaving the field blank for them to
// type a fresh one. No forced immediate re-render on submit -- like the
// "r" (revoke) handler above, the next render tick picks up the change.
func (t *SessionsTable) openRenameInput(s Session) {
	const overlayKey = "sessionRenameInput"

	input := tview.NewInputField().
		SetLabel("Name: ").
		SetFieldWidth(0)
	if s.Nickname != "" {
		input.SetText(s.Nickname)
	}

	dismiss := func() { t.app.DismissOverlay(overlayKey) }

	submit := func() {
		name := strings.TrimSpace(input.GetText())
		dismiss()
		if _, err := t.client.SetName(s.Pubkey, name); err != nil {
			deferFollowUpDialog(t.app, func() { t.app.Error(fmt.Sprintf("failed to update name: %s", err)) })
		}
	}

	form := tview.NewForm().
		SetButtonsAlign(tview.AlignCenter).
		SetButtonBackgroundColor(tcell.ColorDefault).
		// See showBunkerURI's own comment on why this is needed.
		SetButtonActivatedStyle(tcell.Style{}.Background(tui.ColorPrimary).Foreground(tui.ColorText)).
		SetLabelColor(tui.ColorText).
		SetFieldBackgroundColor(tui.ColorPrimary).
		SetFieldTextColor(tui.ColorText)
	form.AddFormItem(input)
	form.AddButton("Save", submit)
	form.AddButton("Cancel", dismiss)
	form.SetCancelFunc(dismiss)
	form.SetBackgroundColor(tcell.ColorDefault)
	wireButtonArrowNav(form, input)

	view := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true)
	view.SetBorder(true).
		SetBorderColor(tui.ColorPrimary).
		SetTitle(" Set App Name ").
		SetBackgroundColor(tcell.ColorDefault)

	x, y, w, h := positionedOverlayRect(t.app, 100, 20)
	view.SetRect(x, y, w, h)
	t.app.ShowPositionedOverlay(overlayKey, view, form)
}

// openGrantsOverlay is Enter's target on a Trusted App row: a per-grant
// detail view, one row per Grant (plain-language scope, allow/deny
// status, expiry), so a session with several grants can be edited
// surgically instead of only through 'r' (SessionsTable's own bulk
// revoke-everything). 'x' revokes just the selected grant; 'e' reopens
// the same duration/budget picker openDurationDialog's own "Always ..."
// step 2 uses, to re-scope an existing grant on demand. Sized to its own
// row count (centerOverlayFixedHeight), not a fixed screen percentage --
// a session with one grant shouldn't get the same tall box as one with
// ten.
func (t *SessionsTable) openGrantsOverlay(s Session) {
	const overlayKey = "sessionGrants"
	dismiss := func() { t.app.DismissOverlay(overlayKey) }

	grants := s.Grants
	table := tview.NewTable().SetSelectable(true, false)
	table.SetSelectedStyle(tcell.Style{}.
		Background(tui.ColorPrimary).
		Foreground(tui.ColorText),
	)
	table.SetFixed(1, 0)

	render := func() {
		table.Clear()
		for c, h := range []string{"Permission", "Status", "Expires"} {
			table.SetCell(0, c, tview.NewTableCell(h).
				SetTextColor(tui.ColorPrimary).
				SetSelectable(false).
				SetAttributes(tcell.AttrBold))
		}
		if len(grants) == 0 {
			table.SetCell(1, 0, tview.NewTableCell("(no remembered grants)").
				SetTextColor(tui.ColorMuted).
				SetSelectable(false))
			return
		}
		for i, g := range grants {
			statusText, statusColor := grantStatusLabel(g)
			table.SetCell(i+1, 0, tview.NewTableCell(grantScopeLabel(g)))
			table.SetCell(i+1, 1, tview.NewTableCell(statusText).SetTextColor(statusColor))
			table.SetCell(i+1, 2, tview.NewTableCell(grantDurationLabel(g)))
		}
	}
	render()

	selectedGrant := func() (Grant, bool) {
		row, _ := table.GetSelection()
		idx := row - 1
		if idx < 0 || idx >= len(grants) {
			return Grant{}, false
		}
		return grants[idx], true
	}

	hint := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	fmt.Fprint(hint, strings.Join([]string{
		hintTag("<x>", "Revoke"),
		hintTag("<e>", "Extend"),
		hintTag("<Esc>", "Close"),
	}, "   \t"))

	table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			dismiss()
			return nil
		}
		if event.Key() != tcell.KeyRune {
			return event
		}
		switch event.Rune() {
		case 'x':
			g, ok := selectedGrant()
			if !ok {
				return nil
			}
			// dismiss() first, then a plain (non-ConfirmDelete) two-button
			// ShowDialog: App.ConfirmDelete's own OK/Cancel both hardcode
			// SwitchToPage("main") with no way to plug in "go back to the
			// grants overlay instead" -- fine for the existing bulk-revoke
			// 'r' key (already on "main," nothing to return to), wrong
			// here, where losing the overlay on every single revoke would
			// defeat the point of a view meant for pruning several grants
			// in one visit. Both button funcs run inside ShowDialog's own
			// wrapped callback (which switches back to "main" immediately
			// after they return), so reopening the overlay from either one
			// needs deferFollowUpDialog -- same reasoning as
			// openDurationDialog's own "Cancel" button.
			dismiss()
			t.app.ShowDialog(
				"Revoke permission?",
				fmt.Sprintf("Revoke %s for %s?", grantScopeLabel(g), labelFor(s)),
				tcell.ColorDefault,
				[]string{"Revoke", "Cancel"},
				func() {
					if _, err := t.client.RevokeGrant(s.Pubkey, g.Method, g.Kind); err != nil {
						deferFollowUpDialog(t.app, func() { t.app.Error(fmt.Sprintf("failed to revoke: %s", err)) })
						return
					}
					deferFollowUpDialog(t.app, func() { t.reopenGrantsOverlay(s.Pubkey) })
				},
				func() { deferFollowUpDialog(t.app, func() { t.openGrantsOverlay(s) }) },
			)
			return nil
		case 'e':
			g, ok := selectedGrant()
			if !ok {
				return nil
			}
			dismiss()
			if g.Verdict == Deny {
				// Same "dismiss first" reasoning as the non-Deny branch
				// below -- Alert's own OK button also hardcodes
				// SwitchToPage("main"), so there's no overlay left to
				// steal focus from by the time it's shown.
				t.app.Alert("Blocked permissions don't expire -- revoke it instead to let this app ask again.")
				return nil
			}
			t.openExtendGrantDialog(s, g)
			return nil
		}
		return event
	})

	view := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(table, 0, 1, true).
		AddItem(hint, 1, 0, false)
	view.SetBorder(true).
		SetBorderColor(tui.ColorPrimary).
		SetTitle(fmt.Sprintf(" Grants for %s ", labelFor(s))).
		SetBackgroundColor(tcell.ColorDefault)

	height := max(len(grants)+5, 6) // border(2) + header row(1) + hint(1) + at least one content row
	t.app.ShowOverlay(overlayKey, centerOverlayFixedHeight(view, 70, height), table)
}

// reopenGrantsOverlay refetches pubkey's own Session from the daemon and
// reopens openGrantsOverlay against that fresh copy -- used after a
// mutating action (revoke, extend) so the operator lands back on an
// up-to-date view instead of the stale snapshot the overlay was first
// opened with. A no-op if pubkey is no longer a known session at all
// (e.g. its last grant's revoke also happened to be racing a full
// Revoke from elsewhere).
func (t *SessionsTable) reopenGrantsOverlay(pubkey string) {
	sessions, err := t.client.ListSessions()
	if err != nil {
		return
	}
	for _, sess := range sessions {
		if sess.Pubkey == pubkey {
			t.openGrantsOverlay(sess)
			return
		}
	}
}

// openExtendGrantDialog re-scopes an already-remembered grant to a new
// duration/budget -- the same five choices openDurationDialog's own step 2
// offers a still-pending request, just re-targeted at an existing grant
// via BunkerClient.SetGrant instead of an Approve call. Store.Remember
// replaces any existing grant with the same (method, kind) scope (see its
// own doc comment), so this needs no separate revoke-then-recreate step.
// Only ever called for an Allow-verdict grant -- openGrantsOverlay's own
// 'e' handler turns away a Deny one before reaching here, since a blocked
// permission has no duration/budget concept in this codebase.
func (t *SessionsTable) openExtendGrantDialog(s Session, g Grant) {
	now := time.Now()
	set := func(grant Grant) {
		if err := t.client.SetGrant(s.Pubkey, grant); err != nil {
			deferFollowUpDialog(t.app, func() { t.app.Error(fmt.Sprintf("failed to update grant: %s", err)) })
			return
		}
		deferFollowUpDialog(t.app, func() { t.reopenGrantsOverlay(s.Pubkey) })
	}

	buttons := []string{"1 hour", "24 hours", "7 days", "Until revoked", "Next 10 uses", "Cancel"}
	funcs := []func(){
		func() { set(GrantForDuration(g.Method, g.Kind, time.Hour, now)) },
		func() { set(GrantForDuration(g.Method, g.Kind, 24*time.Hour, now)) },
		func() { set(GrantForDuration(g.Method, g.Kind, 7*24*time.Hour, now)) },
		func() { set(GrantForever(g.Method, g.Kind, now)) },
		func() { set(GrantForUses(g.Method, g.Kind, 10, now)) },
		func() { deferFollowUpDialog(t.app, func() { t.openGrantsOverlay(s) }) },
	}
	t.app.ShowDialog(fmt.Sprintf("Extend %q?", grantScopeLabel(g)), "", tcell.ColorDefault, buttons, funcs...)
}

// sessionsTableHeaders -- Name/Trusted Since/Last Request/Kinds surface,
// as their own scannable columns, information that used to live only
// inside Grants' own dense freeform summary (or, for Name, not at all):
// App always stays the app's raw shortened pubkey -- a stable identifier
// that never changes out from under you, unlike Name -- which is instead
// where nameColumn's own Nickname-over-self-reported-name-over-"-"
// preference shows up (see the "n" shortcut/openRenameInput), how long
// it's actually been trusted (formatElapsed(s.PairedAt)), how recently
// it last actually asked for anything (formatElapsed on the newest
// History entry for that pubkey -- see Update's lastRequestAt), and
// which sign_event kinds it can currently sign without asking
// (summarizeKinds) -- Grants itself stays, still the one column with the
// full verdict/duration/budget detail for every method, not just
// sign_event's kinds.
var sessionsTableHeaders = []string{"App", "Name", "Trusted Since", "Last Request", "Kinds", "Grants"}

func (t *SessionsTable) drawHeader() {
	for c, name := range sessionsTableHeaders {
		t.SetCell(0, c, tview.NewTableCell(fmt.Sprintf("[-:-:b]%s", strings.ToUpper(name))).
			SetExpansion(1).
			SetTextColor(tui.ColorMuted).
			SetAlign(tview.AlignLeft).
			SetSelectable(false))
	}
}

func (t *SessionsTable) updateTitle(count int) {
	t.SetTitle(fmt.Sprintf(" [::b][%s]TRUSTED APPS [[%s]%d[%s]] ",
		tui.ColorPrimary, tui.ColorBadge, count, tui.ColorPrimary))
}

func (t *SessionsTable) Update() {
	sessions, err := t.client.ListSessions()
	if err != nil {
		return
	}

	// History, best-effort: a transient IPC hiccup here shouldn't blank
	// out the whole render, just leave Last Request as "-" for every row
	// this tick -- unlike ListSessions' own error above, History isn't
	// the data this table exists to show, so it doesn't get an early
	// return of its own.
	history, _ := t.client.History()
	lastRequestAt := make(map[string]time.Time, len(history))
	for _, h := range history {
		// history is most-recent-first (see Daemon.History's own doc
		// comment), so the first entry seen per pubkey is already its
		// newest -- skip any later, older entries for the same app.
		if _, ok := lastRequestAt[h.ClientKey]; !ok {
			lastRequestAt[h.ClientKey] = h.CreatedAt
		}
	}

	// Most-recently-used apps first, so a glance at the top of the panel
	// answers "what's active" rather than requiring a scan of the whole
	// list -- ListSessions itself has no ordering guarantee worth relying
	// on. An app with no entry in lastRequestAt (never made a request, or
	// its last one has aged out of the 200-entry history tail -- see
	// Update's own "Last Request" cell) sorts to the bottom rather than
	// the top: a zero time.Time is already "smallest," so the plain After
	// comparison below puts it last with no special-casing needed. Stable
	// so two apps that tie (both zero, most commonly) keep ListSessions'
	// own relative order instead of shuffling every tick.
	sort.SliceStable(sessions, func(i, j int) bool {
		return lastRequestAt[sessions[i].Pubkey].After(lastRequestAt[sessions[j].Pubkey])
	})

	t.mu.Lock()
	t.rendered = sessions
	t.mu.Unlock()

	t.Clear()
	t.drawHeader()
	t.updateTitle(len(sessions))

	for i, s := range sessions {
		cells := []string{
			shortHex(s.Pubkey),
			nameColumn(s),
			formatElapsed(s.PairedAt),
			formatElapsed(lastRequestAt[s.Pubkey]),
			summarizeKinds(s.Grants),
			summarizeGrants(s.Grants),
		}
		for c, val := range cells {
			t.SetCell(i+1, c, tview.NewTableCell(val).
				SetExpansion(1).
				SetAlign(tview.AlignLeft))
		}
	}

	if t.onCountChange != nil {
		t.onCountChange(len(sessions))
	}
}

func (t *SessionsTable) render(ctx context.Context) {
	ticker := time.NewTicker(renderInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.app.QueueUpdateDraw(func() { t.Update() })
		case <-ctx.Done():
			return
		}
	}
}

// HistoryTable is the bunker board's other glance-at pane: the most
// recent resolved requests (approved/rejected/expired), complementing
// PendingTable -- which only ever shows requests still awaiting a
// decision -- with what actually happened to the ones that aren't
// pending anymore. Read-only: a past decision can't be undone from here
// (revoke a Trusted App's whole standing permission instead, via
// SessionsTable's own 'r', if that's what's actually wanted), so unlike
// PendingTable/SessionsTable this has no SetInputCapture of its own, and
// (nothing to look a selected row up for) no rendered-snapshot field
// either. Shares a row with PendingTable (see NewBunkerBoard's
// historyPendingRow) rather than sizing itself to its own content the
// way SessionsTable does, so it has no onCountChange/height hook either.
type HistoryTable struct {
	*tview.Table
	app    *tui.App
	client BunkerClient

	// mu/rendered exist for exactly one reason: letting Enter on a row look
	// up that row's full HistoryEntry (historyAt) to show its event JSON
	// -- see showEventDetail. Mirrors PendingTable's own rendered field/
	// pendingAt, including the row-index-to-slice-index offset (row 0 is
	// the header).
	mu       sync.Mutex
	rendered []HistoryEntry
}

func NewHistoryTable(app *tui.App, client BunkerClient) *HistoryTable {
	return &HistoryTable{Table: tview.NewTable(), app: app, client: client}
}

func (t *HistoryTable) Init(ctx context.Context) *HistoryTable {
	t.SetFixed(1, 0).
		SetSelectable(true, false).
		SetBorder(true).
		SetBorderPadding(0, 1, 1, 1)

	tui.WireFocusBorder(t.Table, t.Table.Box)

	t.SetSelectedStyle(tcell.Style{}.
		Background(tui.ColorPrimary).
		Foreground(tui.ColorText),
	)

	t.updateTitle(0)
	t.drawHeader()

	// Enter shows the selected row's event JSON, if it has one (only
	// sign_event does -- see HistoryEntry.Event's own doc comment;
	// signed if approved, unsigned otherwise) -- a no-op for every other
	// method, same "nothing to act on" fallback PendingTable's
	// quickResolve uses for an empty table.
	t.SetSelectedFunc(func(row, _ int) {
		if h, ok := t.historyAt(row); ok {
			t.showEventDetail(h)
		}
	})

	// The footer's own <Enter> hint (FooterHints' own b.history case) is
	// conditional on the *selected row* actually having something to
	// show, not just on this table holding focus -- Up/Down moving the
	// selection within an already-focused table doesn't go through
	// App.Focus (Tab/Shift+Tab only), so nothing else would ever tell the
	// footer to recompute after a plain arrow-key move. RefreshFooterHints
	// is the one hook that exists for exactly this: a hint that can change
	// without any focus change at all.
	t.SetSelectionChangedFunc(func(row, _ int) {
		t.app.RefreshFooterHints()
	})

	// Fetched synchronously (matching SessionsTable/IdentityBar's own
	// Init) rather than leaving this at zero (and the title's own count)
	// until the first render tick, up to renderInterval later.
	t.Update()
	go t.render(ctx)
	return t
}

// historyAt maps a table row to its HistoryEntry, the same row-1 offset
// (row 0 is the header) PendingTable.pendingAt uses.
func (t *HistoryTable) historyAt(row int) (HistoryEntry, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	idx := row - 1
	if idx < 0 || idx >= len(t.rendered) {
		return HistoryEntry{}, false
	}
	return t.rendered[idx], true
}

// historyTableHeaders mirrors pendingTableHeaders' own Method/Kind split
// (see PendingTable.Update) rather than folding them into one field --
// consistent with Pending's own table, and Kind on its own is directly
// scannable/sortable-by-eye rather than buried inside a combined string.
// Expires has no place here (a resolved request no longer has one);
// Status takes its place instead.
var historyTableHeaders = []string{"Time", "App", "Method", "Kind", "Status"}

func (t *HistoryTable) drawHeader() {
	for c, name := range historyTableHeaders {
		t.SetCell(0, c, tview.NewTableCell(fmt.Sprintf("[-:-:b]%s", strings.ToUpper(name))).
			SetExpansion(1).
			SetTextColor(tui.ColorMuted).
			SetAlign(tview.AlignLeft).
			SetSelectable(false))
	}
}

func (t *HistoryTable) updateTitle(count int) {
	t.SetTitle(fmt.Sprintf(" [::b][%s]REQUEST HISTORY [[%s]%d[%s]] ",
		tui.ColorPrimary, tui.ColorBadge, count, tui.ColorPrimary))
}

func (t *HistoryTable) Update() {
	history, err := t.client.History()
	if err != nil {
		return // transient IPC hiccup -- next tick retries; keep the last good snapshot on screen
	}

	// Best-effort, same reasoning as PendingTable.Update's own
	// ListSessions fetch: a failure here just falls back to shortHex
	// below for every row, not an early return of the whole render.
	// Local rather than cached on t (unlike PendingTable's
	// sessionLabels) -- nothing outside this loop needs an app label for
	// a History row (showEventDetail never prints one).
	sessions, _ := t.client.ListSessions()
	sessionLabels := make(map[string]string, len(sessions))
	for _, s := range sessions {
		sessionLabels[s.Pubkey] = labelFor(s)
	}

	t.mu.Lock()
	t.rendered = history
	t.mu.Unlock()

	t.Clear()
	t.drawHeader()
	t.updateTitle(len(history))

	statusCol := len(historyTableHeaders) - 1
	for i, h := range history {
		kindCol := "-"
		if h.Method == nip46.MethodSignEvent {
			kindCol = strconv.Itoa(h.Kind)
		}
		statusText, statusColor := historyStatus(h)
		label, ok := sessionLabels[h.ClientKey]
		if !ok {
			label = shortHex(h.ClientKey)
		}
		cells := []string{
			formatClockTime(h.ResolvedAt),
			label,
			h.Method,
			kindCol,
			statusText,
		}
		for c, val := range cells {
			cell := tview.NewTableCell(val).
				SetExpansion(1).
				SetAlign(tview.AlignLeft)
			if c == statusCol {
				cell.SetTextColor(statusColor)
			}
			t.SetCell(i+1, c, cell)
		}
	}
}

func (t *HistoryTable) render(ctx context.Context) {
	ticker := time.NewTicker(renderInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.app.QueueUpdateDraw(func() { t.Update() })
		case <-ctx.Done():
			return
		}
	}
}

// showEventDetail displays h.Event's full JSON (colorized the same way
// client/tui.ShowEvent renders an event, via the exported
// ColorizeEventJSON -- see that function's own doc comment on why this
// package reuses it instead of duplicating the colorizer) with a Copy
// button (copyToClipboard, the same OSC52-plus-native-fallback mechanism
// showBunkerURI's own "Copy" button already uses) suggested alongside
// Close. h.Event holds the unsigned sign_event target from the moment a
// request is resolved, whatever the verdict (see HistoryEntry.Event's own
// doc comment) -- signed is only true once it's actually been approved
// and Handler.execute really signed it, so a rejected or expired
// sign_event still opens this, showing what was being asked for even
// though nothing was ever signed; the header line and Copy's own
// confirmation text both say which case this is. A no-op if h has no
// event at all (every method but sign_event) -- historyAt's own
// SetSelectedFunc caller already guards this, so h.Event is never nil
// here in practice, but the check stays cheap insurance against a future
// caller forgetting that guard.
func (t *HistoryTable) showEventDetail(h HistoryEntry) {
	if h.Event == nil {
		return
	}
	signed := h.Event.Sig != ""

	const overlayKey = "historyEventDetail"
	dismiss := func() { t.app.DismissOverlay(overlayKey) }

	statusText, statusColor := historyStatus(h)

	status := tview.NewTextView().SetDynamicColors(true)

	jsonView := tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true).
		SetScrollable(true)
	jsonView.SetBorderPadding(0, 0, 0, 0)
	fmt.Fprint(jsonView, tui.ColorizeEventJSON(h.Event))
	jsonView.ScrollToBeginning()

	form := tview.NewForm().
		SetButtonsAlign(tview.AlignCenter).
		SetButtonBackgroundColor(tcell.ColorDefault).
		// See showBunkerURI's own comment on why this is needed.
		SetButtonActivatedStyle(tcell.Style{}.Background(tui.ColorPrimary).Foreground(tui.ColorText))
	form.AddButton("Copy", func() {
		raw, err := json.Marshal(h.Event)
		if err != nil {
			status.SetText(fmt.Sprintf("[%s:-:-]failed to encode: %s", tui.ColorDanger, err))
			return
		}
		copyToClipboard(string(raw))
		if signed {
			status.SetText(fmt.Sprintf("[%s:-:-]Attempted to copy the signed event JSON to your clipboard.", tui.ColorSuccess))
		} else {
			status.SetText(fmt.Sprintf("[%s:-:-]Attempted to copy the unsigned event JSON to your clipboard.", tui.ColorSuccess))
		}
	})
	form.AddButton("Close", dismiss)
	form.SetFocus(1) // "Close" -- default selected, so a stray Enter can't copy
	form.SetCancelFunc(dismiss)
	form.SetBackgroundColor(tcell.ColorDefault)
	wireScrollCapture(form, jsonView)

	view := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(jsonView, 0, 1, false).
		AddItem(status, 1, 0, false).
		AddItem(form, 3, 0, true)
	// No SetBorderPadding here -- see openSignEventApprovalDialog's own
	// comment on the same choice for the same reason.
	view.SetBorder(true).
		SetBorderColor(tui.ColorPrimary)
	// Status (Approved/Rejected/Expired, optionally "(always)") lives in
	// the title now, not as its own body line -- title rendering already
	// supports the same color tags (see e.g. HistoryTable.updateTitle),
	// so nothing is lost by moving it there.
	kind := "Signed Event"
	if !signed {
		kind = "Unsigned Event"
	}
	view.SetTitle(fmt.Sprintf(" [%s]%s[-:-:-] -- %s ", statusColor, statusText, kind))
	view.SetBackgroundColor(tcell.ColorDefault)

	// Full screen (100/100) -- see openSignEventApprovalDialog's own
	// comment on the same choice for the same reason (a large JSON view,
	// where any margin leaves part of the board underneath glimpsable
	// around the edges).
	t.app.ShowOverlay(overlayKey, centerOverlay(view, 100, 100), form)
}

// historyStatus renders h's outcome as a short label plus a color from
// the same green/yellow/red vocabulary already used elsewhere on this
// board (formatRelayStatuses' connected/connecting/down,
// PendingTable's urgencyColor) -- green for approved, red for rejected,
// yellow for expired unanswered (neither a yes nor a no), with
// "(always)" distinguishing a remembered-grant decision from a one-off
// Approve/Reject Once. "Auto-approved" is its own case, ahead of all the
// others: a request an existing grant already covered never went through
// a human decision (or Queue.Add) at all, so neither "(always)" (nothing
// new was remembered) nor a one-off "Approved" (nobody actually chose
// anything, this time) quite fits.
func historyStatus(h HistoryEntry) (text string, color tcell.Color) {
	switch {
	case h.AutoApproved:
		return "Auto-approved", tui.ColorSuccess
	case h.Expired:
		return "Expired", tui.ColorWarning
	case h.Verdict == Allow && h.Remembered:
		return "Approved (always)", tui.ColorSuccess
	case h.Verdict == Allow:
		return "Approved", tui.ColorSuccess
	case h.Remembered:
		return "Rejected (always)", tui.ColorDanger
	default:
		return "Rejected", tui.ColorDanger
	}
}

// summarizeApp renders a session's self-reported app identity (see
// Session.AppName/AppURL's own doc comment for why this is only ever
// populated for a nostrconnect:// pairing) as "Name (url)", "Name" alone
// if there's no URL, or "-" if the app never supplied a name at all
// (every bunker:// pairing, and any nostrconnect:// one with a blank
// metadata.name).
func summarizeApp(s Session) string {
	if s.AppName == "" {
		return "-"
	}
	if s.AppURL == "" {
		return s.AppName
	}
	return fmt.Sprintf("%s (%s)", s.AppName, s.AppURL)
}

// nameColumn is SessionsTable's own Name-column formatter: the user's
// own Nickname if they've set one, else the app's self-reported name
// (summarizeApp), else "-". Deliberately doesn't fall all the way back
// to shortHex(pubkey) the way labelFor's own chain does elsewhere --
// App, this table's neighboring column, always shows that already, so
// repeating it here would just be noise.
func nameColumn(s Session) string {
	if s.Nickname != "" {
		return s.Nickname
	}
	return summarizeApp(s)
}

// formatElapsed renders how long ago t was, coarse enough to be
// glanceable in a table cell -- SessionsTable's own "Trusted Since"
// column, where the exact second a pairing happened rarely matters but
// the rough age does (days into a long-running signer's life, not
// timestamps). "-" for a zero t: a session whose sessions.yaml predates
// Session.PairedAt existing at all, not a real "just now."
func formatElapsed(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// summarizeKinds renders which sign_event kinds grants currently covers
// without asking -- "any" if there's an any-kind grant (sensitiveKinds
// are never included in one regardless -- see policy.go -- so "any"
// here is already accurate, not an overclaim), a sorted comma-joined
// list of exact kinds otherwise, "-" if there's no sign_event grant at
// all. Deliberately doesn't distinguish allow from deny or show
// duration/budget -- summarizeGrants' own column already has the full
// detail for every method, not just sign_event; this is purely "which
// kinds," for a glance.
func summarizeKinds(grants []Grant) string {
	any := false
	seen := map[int]bool{}
	var kinds []int
	for _, g := range grants {
		if g.Method != nip46.MethodSignEvent {
			continue
		}
		if g.Kind == nil {
			any = true
			continue
		}
		if !seen[*g.Kind] {
			seen[*g.Kind] = true
			kinds = append(kinds, *g.Kind)
		}
	}
	if any {
		return "any"
	}
	if len(kinds) == 0 {
		return "-"
	}
	sort.Ints(kinds)
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = strconv.Itoa(k)
	}
	return strings.Join(parts, ", ")
}

// summarizeGrants renders a session's grants as a compact, comma-joined
// summary -- e.g. "sign_event(kind 1, until revoked), ping(forever)".
func summarizeGrants(grants []Grant) string {
	if len(grants) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(grants))
	for _, g := range grants {
		scope := "any kind"
		if g.Kind != nil {
			scope = fmt.Sprintf("kind %d", *g.Kind)
		}
		verdict := "allow"
		if g.Verdict == Deny {
			verdict = "deny"
		}
		duration := "forever"
		switch {
		case g.ExpiresAt != nil:
			duration = "until " + g.ExpiresAt.Format("Jan 2 15:04")
		case g.RemainingUses != nil:
			duration = fmt.Sprintf("%d uses left", *g.RemainingUses)
		}
		if g.Method == nip46.MethodSignEvent {
			parts = append(parts, fmt.Sprintf("%s %s(%s, %s)", verdict, g.Method, scope, duration))
		} else {
			parts = append(parts, fmt.Sprintf("%s %s(%s)", verdict, g.Method, duration))
		}
	}
	return strings.Join(parts, ", ")
}

// methodLabels gives each NIP-46 method a short, plain-language phrase --
// the Grants management overlay (openGrantsOverlay, below) is meant to be
// readable by a general user who's never heard of "nip44_encrypt," not
// just an operator who already knows the wire protocol's method names.
var methodLabels = map[string]string{
	nip46.MethodConnect:      "Reconnect",
	nip46.MethodPing:         "Check connection (ping)",
	nip46.MethodGetPublicKey: "Read your public key",
	nip46.MethodGetRelays:    "Read your relay list",
	nip46.MethodNIP04Encrypt: "Encrypt messages (NIP-04)",
	nip46.MethodNIP04Decrypt: "Decrypt messages (NIP-04)",
	nip46.MethodNIP44Encrypt: "Encrypt messages (NIP-44)",
	nip46.MethodNIP44Decrypt: "Decrypt messages (NIP-44)",
}

// kindLabels names the handful of event kinds a general user is likely to
// recognize offhand -- anything else still shows as "kind N" (still
// exact, just not translated); this is a readability aid, not a registry.
var kindLabels = map[int]string{
	0:     "profile updates",
	1:     "notes",
	3:     "contact list changes",
	4:     "encrypted DMs (legacy)",
	5:     "deletions",
	6:     "reposts",
	7:     "reactions",
	9734:  "zap requests",
	9735:  "zap receipts",
	30023: "long-form posts",
}

// grantScopeLabel renders what g actually covers in plain language --
// methodLabels/kindLabels' whole reason to exist. Sensitive kinds (see
// policy.go's sensitiveKinds) never show up under an any-kind grant, so
// that phrasing is a guarantee, not an approximation.
func grantScopeLabel(g Grant) string {
	if g.Method != nip46.MethodSignEvent {
		if label, ok := methodLabels[g.Method]; ok {
			return label
		}
		return g.Method
	}
	if g.Kind == nil {
		return "Sign any event (except profile/contacts/deletions)"
	}
	if label, ok := kindLabels[*g.Kind]; ok {
		return fmt.Sprintf("Sign %s (kind %d)", label, *g.Kind)
	}
	return fmt.Sprintf("Sign kind %d events", *g.Kind)
}

// grantStatusLabel renders g's verdict as a short label plus a color,
// matching historyStatus' own green-for-allow/red-for-deny vocabulary --
// the Grants overlay's own "Status" column.
func grantStatusLabel(g Grant) (text string, color tcell.Color) {
	if g.Verdict == Deny {
		return "Blocked", tui.ColorDanger
	}
	return "Allowed", tui.ColorSuccess
}

// grantDurationLabel renders how long g lasts, in the same plain-language
// spirit as grantScopeLabel -- the Grants overlay's own "Expires" column.
func grantDurationLabel(g Grant) string {
	switch {
	case g.ExpiresAt != nil:
		return "until " + g.ExpiresAt.Format("Jan 2 15:04")
	case g.RemainingUses != nil:
		return fmt.Sprintf("%d use(s) left", *g.RemainingUses)
	default:
		return "until revoked"
	}
}

func formatClockTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("15:04:05")
}

// formatCountdown renders a future timestamp as "Xm Ys" until it arrives,
// or "expired" past it -- how long a human has left to decide before the
// queue's own sweep auto-rejects this request.
func formatCountdown(t time.Time) string {
	remaining := time.Until(t)
	if remaining <= 0 {
		return "expired"
	}
	m := int(remaining.Minutes())
	s := int(remaining.Seconds()) % 60
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// urgencyColor flags how soon expiresAt will trip the queue's own
// auto-reject sweep. ok is false once remaining is comfortable enough
// that the cell's default color already reads fine -- callers should
// leave the cell alone rather than force a color in that case.
func urgencyColor(expiresAt time.Time) (color tcell.Color, ok bool) {
	switch remaining := time.Until(expiresAt); {
	case remaining <= 30*time.Second:
		return tui.ColorDanger, true
	case remaining <= 2*time.Minute:
		return tui.ColorWarning, true
	default:
		return 0, false
	}
}

// IdentityBar is a two-line, borderless strip showing which identity this
// bunker daemon is signing for, and whether it's actually reachable --
// the plan called for the identity line and it was never wired up
// (StatusInfo's own doc comment already promised "the TUI header show[s]"
// it); the relay line answers "is this working or not" directly, rather
// than leaving a fully-disconnected signer to be discovered as "nothing
// ever happens." The identity half shows a shortened npub immediately
// (Status() never blocks), then upgrades to a resolved display name/nip05
// once the daemon's own best-effort kind:0 lookup (Daemon.fetchProfile)
// resolves -- on the same render ticker every other bunker panel already
// polls on, so no extra plumbing is needed for that upgrade to appear.
type IdentityBar struct {
	*tview.TextView
	app    *tui.App
	client BunkerClient

	// onDaemonLost, if set, is called the first time Status() fails --
	// see the doc comment above Update for why that's treated as
	// permanent rather than the "transient hiccup, next tick retries"
	// every other panel's Update assumes.
	onDaemonLost func()
	lostOnce     sync.Once
}

func NewIdentityBar(app *tui.App, client BunkerClient) *IdentityBar {
	return &IdentityBar{TextView: tview.NewTextView(), app: app, client: client}
}

func (b *IdentityBar) Init(ctx context.Context) *IdentityBar {
	b.SetDynamicColors(true)
	b.Update()
	go b.render(ctx)
	return b
}

// Update's Status() failure path is the one place in this file that
// doesn't treat an error as a transient hiccup to silently retry past:
// ipcClient (the Linux/macOS attach path) holds one connection for the
// TUI's whole lifetime and never reconnects, and its bufio.Scanner is
// permanently done after any read error (its own documented behavior) --
// so once this fails, every future call on this same client fails
// identically, forever. There is no "retry succeeds later" for this
// specific error, so onDaemonLost fires (once) instead of leaving the
// board frozen on a stale last-good snapshot forever with nothing on
// screen ever indicating why nothing's moving anymore.
func (b *IdentityBar) Update() {
	st, err := b.client.Status()
	if err != nil {
		if b.onDaemonLost != nil {
			b.lostOnce.Do(b.onDaemonLost)
		}
		return
	}
	b.Clear()
	fmt.Fprintf(b, " [%s::b]Signing as:[-:-:-] %s\n", tui.ColorPrimary, formatIdentity(st))
	fmt.Fprintf(b, " [%s::b]Relays:[-:-:-] %s", tui.ColorPrimary, formatRelayStatuses(st.RelayStatuses))
}

func (b *IdentityBar) render(ctx context.Context) {
	ticker := time.NewTicker(renderInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			b.app.QueueUpdateDraw(func() { b.Update() })
		case <-ctx.Done():
			return
		}
	}
}

// formatIdentity prefers a resolved display name plus nip05, then either
// alone, and only falls back to a shortened npub (see shortNpub) if
// Daemon.fetchProfile hasn't resolved a kind:0 for this identity -- which
// may be its permanent state, for a signer with no published profile.
// formatIdentity always includes the shortened npub (see shortNpub) --
// even once a display name/nip05 has resolved -- since that's the one
// stable, always-correct identifier on the line: a display name is
// self-reported profile data (kind:0), not proof of who's actually
// signing, and the npub is what a human would cross-check against
// elsewhere. Only the npub-only fallback (nothing resolved yet) skips
// repeating it a second time.
func formatIdentity(st StatusInfo) string {
	name := tview.Escape(st.IdentityName)
	nip05 := tview.Escape(st.IdentityNip05)
	npub := shortNpub(st.IdentityPub)
	vault := formatVaultSuffix(st.VaultLabel)
	switch {
	case name != "" && nip05 != "":
		return fmt.Sprintf("[%s]%s[-:-:-] [%s](%s)[-:-:-] [%s](%s)[-:-:-]%s", tui.ColorText, name, tui.ColorMuted, nip05, tui.ColorMuted, npub, vault)
	case name != "":
		return fmt.Sprintf("[%s]%s[-:-:-] [%s](%s)[-:-:-]%s", tui.ColorText, name, tui.ColorMuted, npub, vault)
	case nip05 != "":
		return fmt.Sprintf("[%s]%s[-:-:-] [%s](%s)[-:-:-]%s", tui.ColorText, nip05, tui.ColorMuted, npub, vault)
	default:
		return fmt.Sprintf("[%s]%s[-:-:-]%s", tui.ColorText, npub, vault)
	}
}

// formatVaultSuffix renders st.VaultLabel (see DaemonConfig.VaultLabel's
// own doc comment for when it's set) as a trailing " (vault: label)"
// tview color-tag annotation -- empty (no suffix at all) when there's no
// vault entry to name, the common case for a raw nsec that was never
// saved.
func formatVaultSuffix(label string) string {
	if label == "" {
		return ""
	}
	return fmt.Sprintf(" [%s](vault: %s)[-:-:-]", tui.ColorMuted, tview.Escape(label))
}

// formatRelayStatuses renders each configured relay as a colored bullet
// (green "connected", yellow "still trying its first connection" --
// RelayStatus.Connecting, red "down" once that first attempt has actually
// failed) plus its URL -- the "is this actually working" signal requested
// alongside the identity itself: a signer with zero relays actually
// connected can't receive anything, silently, and this is what surfaces
// that instead of leaving it to be discovered as "nothing ever happens."
// The yellow state exists so the first instant or two after startup --
// before any relay has even had a chance to dial yet -- reads as "in
// progress," not "broken."
func formatRelayStatuses(statuses []RelayStatus) string {
	if len(statuses) == 0 {
		return fmt.Sprintf("[%s]none configured[-:-:-]", tui.ColorMuted)
	}
	parts := make([]string, 0, len(statuses))
	for _, s := range statuses {
		bullet, color := "○", tui.ColorDanger
		switch {
		case s.Connected:
			bullet, color = "●", tui.ColorSuccess
		case s.Connecting:
			bullet, color = "◐", tui.ColorWarning
		}
		parts = append(parts, fmt.Sprintf("[%s]%s[-:-:-] %s", color, bullet, tview.Escape(s.URL)))
	}
	return strings.Join(parts, "   ")
}

// AlertBar is a conditional, height-toggling strip: invisible (0 rows)
// while the signer's healthy, a single hard-to-miss red line the moment
// it's not. Currently watches for just one failure mode -- no relay
// connected -- since that's the one that silently breaks bunker entirely:
// a client's request just never arrives, with nothing else on screen
// pointing at why. IdentityBar's own per-relay bullets already carry this
// same signal, but only as a small colored dot easy to miss while actually
// watching Pending Requests below; this surfaces the identical underlying
// state (client.Status().RelayStatuses) as its own dedicated row instead,
// sized by severity rather than sharing a fixed slot with routine status.
type AlertBar struct {
	*tview.TextView
	app    *tui.App
	client BunkerClient

	// onAlert, if set, is called at the end of every Update with whether
	// there's currently something to show -- NewBunkerBoard's hook back to
	// the containing Flex so this strip's height (0 healthy, 1 not) tracks
	// live, the same mechanism SessionsTable.onCountChange uses for its own
	// height.
	onAlert func(active bool)
}

func NewAlertBar(app *tui.App, client BunkerClient) *AlertBar {
	return &AlertBar{TextView: tview.NewTextView(), app: app, client: client}
}

func (b *AlertBar) Init(ctx context.Context) *AlertBar {
	b.SetDynamicColors(true)
	b.Update()
	go b.render(ctx)
	return b
}

func (b *AlertBar) Update() {
	st, err := b.client.Status()
	if err != nil {
		return // transient IPC hiccup -- next tick retries; keep the last good snapshot on screen
	}

	// anyRelayConnecting guards the startup race: AlertBar.Init runs its
	// own first Update synchronously, at the same instant Daemon.Run has
	// only just spawned its runRelay goroutines -- none of them have had
	// a chance to even attempt a dial yet, let alone resolve one, so
	// every relay reads as "not connected" for that first instant
	// regardless of whether anything's actually wrong. Without this,
	// that always flashed the alert on a perfectly healthy startup.
	// Suppressed only until every relay's first attempt has resolved one
	// way or the other -- a real, confirmed failure (RelayStatus.
	// Connecting false) still raises this normally, same as before.
	active := !anyRelayConnected(st.RelayStatuses) && !anyRelayConnecting(st.RelayStatuses)
	b.Clear()
	if active {
		fmt.Fprintf(b, "[%s::b] ⚠ No relay connected -- signer can't receive requests. Check your network or relay config.[-:-:-]", tui.ColorDanger)
	}
	if b.onAlert != nil {
		b.onAlert(active)
	}
}

func (b *AlertBar) render(ctx context.Context) {
	ticker := time.NewTicker(renderInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			b.app.QueueUpdateDraw(func() { b.Update() })
		case <-ctx.Done():
			return
		}
	}
}

// anyRelayConnected reports whether at least one configured relay is
// currently live -- statuses empty (nothing configured) is treated the
// same as all-disconnected, since either way nothing can reach the signer.
func anyRelayConnected(statuses []RelayStatus) bool {
	for _, s := range statuses {
		if s.Connected {
			return true
		}
	}
	return false
}

// anyRelayConnecting reports whether at least one configured relay is
// still waiting on its very first dial attempt to resolve -- see
// RelayStatus.Connecting's own doc comment. AlertBar.Update uses this to
// withhold judgment on a fresh startup rather than reporting a confirmed
// failure prematurely.
func anyRelayConnecting(statuses []RelayStatus) bool {
	for _, s := range statuses {
		if s.Connecting {
			return true
		}
	}
	return false
}

// shortNpub renders pubKeyHex as its bech32 npub, truncated to
// "npub1xxxxxxxxx...xxxx" -- the same shape every other nostr client uses
// to show an identity without a resolved profile. Falls back to shortHex's
// hex truncation if the pubkey doesn't even encode (it always should).
func shortNpub(pubKeyHex string) string {
	npub, err := nip19.EncodePublicKey(pubKeyHex)
	if err != nil {
		return shortHex(pubKeyHex)
	}
	if len(npub) <= 20 {
		return npub
	}
	return npub[:14] + "..." + npub[len(npub)-4:]
}

// fullNpub renders pubKeyHex as its complete bech32 npub -- unlike
// shortNpub's truncated "npub1xxx...xxxx" (built for a fixed-width TUI
// chrome line), used where completeness matters more than staying
// compact, e.g. `ncli bunker status`'s own output (command.go's
// printIdentityAndRelays), meant to be read carefully or copied whole.
// Falls back to the raw hex if it doesn't even encode (it always should).
func fullNpub(pubKeyHex string) string {
	npub, err := nip19.EncodePublicKey(pubKeyHex)
	if err != nil {
		return pubKeyHex
	}
	return npub
}

// DaemonLogWatcher bridges Daemon.RecentLogs (an in-process accessor) into
// flowLogger, the TUI's own Logger panel -- the piece that was missing
// entirely before: a spawned background daemon (the normal Linux/macOS
// path) runs in a separate process from the attached TUI, so nothing
// wired the daemon's own activity (relay connects, every request's
// method/from/id, a rejected/mismatched pairing attempt, ...) through to
// what looked like a live activity feed; it only ever reached daemon.log
// on disk. Polls on the same cadence every other bunker panel already
// uses, tracking Total (not a raw slice index) across polls so it never
// re-shows or skips lines as RecentLogs' own bounded tail rotates.
type DaemonLogWatcher struct {
	app        *tui.App
	client     BunkerClient
	flowLogger *tui.FlowLogger
	shown      int
}

func NewDaemonLogWatcher(app *tui.App, client BunkerClient, flowLogger *tui.FlowLogger) *DaemonLogWatcher {
	return &DaemonLogWatcher{app: app, client: client, flowLogger: flowLogger}
}

func (w *DaemonLogWatcher) Init(ctx context.Context) *DaemonLogWatcher {
	w.Update()
	go w.render(ctx)
	return w
}

func (w *DaemonLogWatcher) Update() {
	snap, err := w.client.Logs()
	if err != nil || snap.Total <= w.shown {
		return
	}
	missing := snap.Total - w.shown
	newLines := snap.Lines
	if missing < len(snap.Lines) {
		newLines = snap.Lines[len(snap.Lines)-missing:]
	}
	// If missing > len(snap.Lines), some lines rotated out of the tail
	// between polls -- shown just catches back up to Total below,
	// silently accepting the small gap (see maxLogTail's own doc comment
	// on why that's not a realistic concern at this poll cadence) rather
	// than re-showing stale duplicates.
	for _, line := range newLines {
		w.flowLogger.Info(line, tui.FlowAttr{})
	}
	w.shown = snap.Total
}

func (w *DaemonLogWatcher) render(ctx context.Context) {
	ticker := time.NewTicker(renderInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.app.QueueUpdateDraw(func() { w.Update() })
		case <-ctx.Done():
			return
		}
	}
}

// BunkerBoard composes the bunker daemon's live view. Every panel's height
// is a deliberate choice, not a shared default -- each row below is sized
// to what that specific card actually needs:
//
//   - identity: fixed, 2 rows. Pure chrome (who's signing, is it reachable)
//     -- always relevant, never has more or less content to show, so it
//     never grows or shrinks.
//   - alert: conditional, 0 or 1 row (AlertBar.onAlert). Needs zero space
//     the vast majority of the time; the one time it matters (no relay
//     connected -- bunker is silently unreachable) it deserves to be
//     impossible to miss, not squeezed into the same footprint as routine
//     status -- and placed right under identity, above every table, so
//     it's the first thing seen rather than something that has to be
//     scrolled down past Trusted Apps and the whole activity log to
//     even notice.
//   - logger (activity log): fixed, loggerHeight, right under alert --
//     ahead of Trusted Apps, since the raw event-by-event feed (every
//     request's method/from/id, relay connects, ...) is usually the
//     first thing worth a glance right after identity/health, before the
//     two request-tracking tables below it. A continuous feed with no
//     natural "done" size the way a list has a row count -- more is
//     always somewhat useful, so this gets a deliberately generous fixed
//     strip instead of borrowing sessions' count-driven height (which is
//     answering a completely different question).
//   - sessions (Trusted Apps): content-bound, sessionsHeightFor. A
//     reference list that's usually short and occasionally isn't -- sized
//     to its real row count within [minSessionsHeight, maxSessionsHeight]
//     so an empty list doesn't waste a fixed slot and a long one doesn't
//     starve Pending, and kept live (SessionsTable.onCountChange) so it
//     stays correct as apps are trusted/revoked mid-session, not just
//     whatever it was at startup. Now that Store.Pair means every paired
//     app shows up here (not only ones with a remembered grant), this
//     panel is populated far more often than it used to be -- both bounds
//     were raised accordingly so it isn't immediately capped and
//     scrolling right after the very first pairing.
//   - history and pending: flex remainder, split 1:1 into a side-by-side
//     row (historyPendingRow) rather than each getting its own full-width
//     row -- Pending is "awaiting a decision," History is "already
//     decided," and an operator's eye needs to move between the two
//     constantly, so they share a row instead of one pushing the other
//     down the screen. Pending keeps initial focus: it's the only panel
//     actually acted on, the same rationale InspectBoard's own doc
//     comment gives for putting Events there.
type BunkerBoard struct {
	*tview.Flex
	identity *IdentityBar
	sessions *SessionsTable
	logger   *tui.Logger
	alert    *AlertBar
	history  *HistoryTable
	pending  *PendingTable

	// daemonLost is closed exactly once, the moment IdentityBar.Update
	// first sees the daemon connection permanently fail -- see DaemonLost.
	daemonLost chan struct{}
}

const (
	minSessionsHeight = 8
	maxSessionsHeight = 20

	// loggerHeight -- see the BunkerBoard doc comment above for why this
	// is fixed and independent rather than content-driven like Sessions.
	loggerHeight = 16

	// alertBarHeight is AlertBar's one active-state row; see
	// AlertBar.onAlert for the 0-row inactive state.
	alertBarHeight = 1
)

// sessionsHeightFor bounds SessionsTable's height to its actual row count
// (+4 for border, header, and a little breathing room), the same shape
// client/tui.NewInspectBoard uses for Targets -- Table scrolls internally
// past maxSessionsHeight rather than growing unbounded.
func sessionsHeightFor(count int) int {
	return min(max(count+4, minSessionsHeight), maxSessionsHeight)
}

// NewBunkerBoard builds the board. flowLogger backs the same FlowLogger/
// Logger widget apply's own boards use (identical styling and wrap/
// autoscroll toggles for free), fed by DaemonLogWatcher polling the
// daemon's own RecentLogs -- not by the daemon's OnLog hook directly,
// which only reaches a spawned background daemon's on-disk log file (see
// DaemonLogWatcher's own doc comment). canDetach controls the pending
// table's quit-key dialog: true (a real background daemon exists to
// detach from) offers "Detach (keep running)"; false (the Windows
// in-process fallback, where closing the TUI is the only way to stop the
// daemon) only offers stopping outright.
func NewBunkerBoard(app *tui.App, ctx context.Context, client BunkerClient, flowLogger *tui.FlowLogger, canDetach bool) *BunkerBoard {
	b := &BunkerBoard{
		identity:   NewIdentityBar(app, client),
		sessions:   NewSessionsTable(app, client),
		logger:     tui.NewLogger(app, flowLogger).Init(ctx),
		alert:      NewAlertBar(app, client),
		history:    NewHistoryTable(app, client),
		pending:    NewPendingTable(app, client, canDetach).Init(ctx),
		daemonLost: make(chan struct{}),
	}
	NewDaemonLogWatcher(app, client, flowLogger).Init(ctx)

	// History and Pending sit side by side in one row, each getting half
	// the width, rather than stacked -- they're the two panels an
	// operator's eye actually needs to move between constantly (what's
	// still waiting vs. what was just decided), so putting them side by
	// side keeps both on screen together instead of one pushing the
	// other down. Equal 1:1 proportion, no fixed width bias toward
	// either.
	historyPendingRow := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(b.history, 0, 1, false).
		AddItem(b.pending, 0, 1, true)

	b.Flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(b.identity, 2, 0, false).
		// fixedSize=0, proportion=0 is how a Flex item is collapsed to
		// nothing: vendored tview's own Draw sizes an item at
		// distSize*Proportion/proportionSum whenever FixedSize<=0, and a
		// Proportion of 0 makes that numerator -- and so the item's whole
		// size -- 0 regardless of what else is on the row (historyPendingRow's
		// own proportion=1 keeps proportionSum itself away from zero, so
		// this never risks a division by zero either). alert.onAlert below
		// raises it to alertBarHeight the moment there's something to show.
		// Placed right after identity, before every table -- see the
		// BunkerBoard doc comment above for why.
		AddItem(b.alert, 0, 0, false).
		AddItem(b.logger, loggerHeight, 0, false).
		AddItem(b.sessions, sessionsHeightFor(0), 0, false).
		AddItem(historyPendingRow, 0, 1, true)

	// 'b' (background/stop the daemon) and 'c' (connect a new app) are
	// bound here, on the board's own outer Flex, rather than on whichever
	// panel happens to be focused -- both need to be reachable no matter
	// which of Sessions/Logger/History/Pending the operator is currently
	// looking at, not just Pending (where 'c' used to live, and where
	// pairing was only reachable from before this). ('q' used to do the
	// same thing as 'b' as a second key for the same action; dropped as a
	// redundant way to do the one thing 'b' already does.) Firing
	// regardless of the real focus target several levels down
	// (b.pending.table, b.logger's inner TextView, ...) relies on the
	// same top-down capture-before-delegate behavior PendingTable's own
	// SetInputCapture doc comment describes -- every Box between the root
	// and the focused leaf gets a chance to intercept first, not just the
	// leaf itself.
	b.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() != tcell.KeyRune {
			return event
		}
		switch event.Rune() {
		case 'b':
			b.showQuitDialog()
			return nil
		case 'c':
			b.pending.openConnectDialog()
			return nil
		}
		return event
	})

	// Both callbacks are wired before their widget's own Init (which starts
	// its render loop) so no tick can land before the callback is in
	// place -- otherwise the very first tick's Update could fire with
	// onCountChange/onAlert still nil and get silently dropped, leaving
	// the panel's height wrong until the *second* tick corrects it.
	//
	// Sessions' own height would otherwise be pinned forever at
	// sessionsHeightFor(0): SessionsTable.Init never fetches synchronously
	// (only the ticker does, up to renderInterval later), so a height
	// computed once here from b.sessions.rendered -- still nil at
	// construction time -- could never reflect the real session count, at
	// startup or afterward, no matter how many apps end up trusted over a
	// long-running board.
	b.sessions.onCountChange = func(n int) {
		b.Flex.ResizeItem(b.sessions, sessionsHeightFor(n), 0)
	}
	b.sessions.Init(ctx)

	// History has no onCountChange/height wiring of its own, unlike
	// Sessions above -- it shares historyPendingRow with Pending instead
	// of sizing to its own content, so its height already tracks
	// correctly (matches Pending's) with no callback needed.
	b.history.Init(ctx)

	b.alert.onAlert = func(active bool) {
		height := 0
		if active {
			height = alertBarHeight
		}
		b.Flex.ResizeItem(b.alert, height, 0)
	}
	b.alert.Init(ctx)

	// See IdentityBar.Update's own doc comment for why a Status() failure
	// means the daemon connection is gone for good, not a blip -- closing
	// daemonLost and stopping the app immediately turns that into a clean
	// exit instead of a TUI that silently sits there, frozen on a stale
	// snapshot, forever.
	b.identity.onDaemonLost = func() {
		close(b.daemonLost)
		app.Stop()
	}
	b.identity.Init(ctx)

	return b
}

// DaemonLost reports whether this board's BunkerClient permanently failed
// (see IdentityBar.Update). runTUI checks this right after app.Run()
// returns, to tell a normal quit apart from the daemon disappearing out
// from under an attached TUI -- and print a clear reason for the exit
// instead of just silently dropping back to the shell.
func (b *BunkerBoard) DaemonLost() bool {
	select {
	case <-b.daemonLost:
		return true
	default:
		return false
	}
}

// showQuitDialog offers a way to close the signer without silently
// killing a live daemon out from under other apps depending on it --
// "detach, keep running" is the default action whenever there's a real
// background daemon to detach from (canDetach); otherwise (Windows
// in-process fallback) it only offers stopping outright, since there's
// nothing else to detach from. A BunkerBoard method (bound to the global
// 'b' key in NewBunkerBoard), not PendingTable's, even though it reads
// b.pending's own app/client/canDetach -- closing the whole signer isn't
// really one panel's concern any more than Tab-cycling is.
func (b *BunkerBoard) showQuitDialog() {
	app := b.pending.app

	if b.pending.canDetach {
		app.ShowDialog(
			"Detach or stop?",
			"ncli bunker keeps running in the background.\nReattach any time with `ncli bunker attach`.",
			tcell.ColorDefault,
			[]string{"Detach (keep running)", "Stop bunker entirely", "Cancel"},
			func() { app.Stop() },
			b.stopDaemon,
			func() {},
		)
		return
	}

	app.ShowDialog(
		"Stop bunker?",
		"This closes the signer -- connected apps will no longer\nbe able to request signatures.",
		tcell.ColorDefault,
		[]string{"Stop", "Cancel"},
		b.stopDaemon,
		func() {},
	)
}

// stopDaemon issues Stop() to the daemon and, on success, exits the TUI
// -- shared by showQuitDialog's own "Stop bunker entirely"/"Stop" button
// and by HandleCtrlC below.
func (b *BunkerBoard) stopDaemon() {
	app, client := b.pending.app, b.pending.client
	if err := client.Stop(); err != nil {
		deferFollowUpDialog(app, func() { app.Error(fmt.Sprintf("failed to stop: %s", err)) })
		return
	}
	app.Stop()
}

// HandleCtrlC implements client/tui.CtrlCHandler. tview's own built-in
// default for Ctrl+C is an unconfirmed Application.Stop() -- fine for
// most boards (the TUI process exiting IS the whole app ending), wrong
// here: on the Linux/macOS attach path this TUI is just a client dialed
// into an already-running background daemon (see canDetach), so
// Application.Stop() alone would silently leave that daemon running
// forever, the exact opposite of what hitting Ctrl+C on a live signer
// means to press. Unlike 'b' (showQuitDialog), which asks -- a
// reasonable key to press just to look around -- Ctrl+C is deliberately
// answered without a confirmation dialog: it's a conventional,
// unambiguous "kill this now" gesture across terminal tools, and
// treating it as anything softer than an immediate stop would be its
// own surprise.
func (b *BunkerBoard) HandleCtrlC() bool {
	b.stopDaemon()
	return true
}

// Childs implements client/tui.ChildProvider, wiring Tab/Shift+Tab
// cycling across this board's four panels the same way every other ncli
// TUI board does -- in the same top-to-bottom order they're actually
// laid out in (see NewBunkerBoard), so Tab always moves to whichever
// panel is visually next rather than jumping around the screen.
// b.pending.FocusTarget(), not b.pending itself, for the same reason
// b.logger.FocusTarget() already is: PendingTable is a bordered Flex
// wrapping its own actions bar + table (see its own doc comment), and
// Tab-cycling needs the real selectable leaf underneath -- b.sessions/
// b.history have no such wrapper (see their own doc comments), so
// they're used directly.
func (b *BunkerBoard) Childs() []tview.Primitive {
	return []tview.Primitive{b.logger.FocusTarget(), b.sessions, b.history, b.pending.FocusTarget()}
}

// hintTag renders one "<key> label" hint-bar entry using this board's
// shared accent-key/muted-label color pair -- the repeated building block
// behind every hint string in this file (FooterHints' own four variants,
// plus the Grants overlay's own hint line).
func hintTag(key, label string) string {
	return fmt.Sprintf("[%s:-:b]%s [%s:-:-]%s", tui.ColorAccent, key, tui.ColorMuted, label)
}

// FooterHints implements client/tui.FooterHintsProvider, replacing the
// default Tab/Shift+Tab/Wrap/AutoScroll hint bar (apply-specific -- Wrap/
// AutoScroll are Logger-only toggles, not central here) with bunker's
// own, contextual set: whichever of Sessions/Logger/History/Pending
// focused currently is tells us which one of them holds the real focus,
// and only that panel's own keys actually do anything right now --
// showing all of them at once regardless of focus (the previous, fixed
// version of this) meant most of the hint bar was inert most of the time
// (e.g. 'n' Set Name shown while Pending, not Sessions, was focused).
// Switch Panel, Background, and Connect are the board-wide exceptions
// (Tab/Shift+Tab always works; 'b'/'c' are bound board-wide -- see
// NewBunkerBoard) -- global always leads, in the same fixed order, in
// every variant; the panel-specific (dynamic) keys always follow it,
// never interleaved with it, so the board-wide keys are always found in
// the same place regardless of which panel happens to be focused.
func (b *BunkerBoard) FooterHints(focused tview.Primitive) string {
	global := strings.Join([]string{
		hintTag("<Tab>/<Shift+Tab>", "Switch Panel"),
		hintTag("<b>", "Background"),
		hintTag("<c>", "Connect"),
	}, "   \t") + "   \t"

	switch focused {
	case b.sessions:
		return global + strings.Join([]string{
			hintTag("<Enter>", "Manage Grants"),
			hintTag("<r>", "Revoke All"),
			hintTag("<n>", "Set Name"),
		}, "   \t")

	case b.logger.FocusTarget():
		return global + strings.Join([]string{
			hintTag("<w>", "Toggle Wrap"),
			hintTag("<s>", "Toggle AutoScroll"),
		}, "   \t")

	case b.history:
		// Read-only for deciding anything (see HistoryTable's own doc
		// comment) -- but Enter on a sign_event row opens its event JSON
		// (showEventDetail). Shown only when the *currently selected*
		// row actually has one (see HistoryEntry.Event's own doc comment
		// on which rows do), not just because History itself is
		// focused -- a hint for a key that would silently do nothing on
		// most rows (every non-sign_event method) is worse than no hint
		// at all. HistoryTable's own SetSelectionChangedFunc keeps this
		// current as the selection moves within the still-focused table.
		row, _ := b.history.GetSelection()
		if h, ok := b.history.historyAt(row); ok && h.Event != nil {
			return global + hintTag("<Enter>", "View Event")
		}
		return global

	default: // b.pending.FocusTarget(), and the fallback before real focus is resolved
		// <a>/<r>/<Enter> merged into one hint (three keys, one label,
		// same slash-separated shape as the keys themselves) rather than
		// two separate footer entries -- a/r: quick Approve/Reject Once;
		// Enter: the full dialog, for an "Always ..." grant or Reject
		// Always (see openApprovalDialog's own doc comment). 'r' overloads
		// SessionsTable's own Revoke key, but the two never fire at once --
		// each panel's SetInputCapture only runs while it's the one
		// actually focused (see PendingTable.Init's own doc comment), and
		// the hint bar itself always matches whichever key really does.
		return global + strings.Join([]string{
			hintTag("<a>/<r>/<Enter>", "Approve/Reject/More"),
			hintTag("<p>", "Toggle Auto-Prompt"),
		}, "   \t")
	}
}
