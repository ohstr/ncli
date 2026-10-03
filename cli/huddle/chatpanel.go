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
)

// chatBodyIndent is the margin a message body sits at under its own header
// line, on top of whatever its thread depth adds.
const chatBodyIndent = "  "

// chatFallbackWidth wraps the body before tview has laid the panel out and a
// real width exists. The next draw rewraps at the true width.
const chatFallbackWidth = 60

// chatPanel is the conversation half of the huddle board: a scrollable,
// threaded transcript plus a composer.
//
// Messages are a tview.Table rather than a TextView because replying needs a
// selected message, and a TextView has no notion of one. The cost is that
// wrapping is this type's job -- a table cell does not wrap -- so a long
// message becomes several rows that all map back to it.
type chatPanel struct {
	*tview.Flex

	app    *tui.App
	log    *chatLog
	sender chatSender

	table *tview.Table
	input *tview.InputField
	hint  *tview.TextView

	mu sync.Mutex
	// rowMessage maps a table row to the message it belongs to, so a
	// selection landing on a wrapped continuation row still resolves to the
	// right message.
	rowMessage []chatMessage
	replyTo    chatMessage
	replying   bool
	quotes     []chatMessage
	// drawnRev and drawnWidth are what the table currently shows, so a tick
	// can skip repainting when neither the conversation nor the width moved.
	drawnRev   uint64
	drawnWidth int
	// following keeps new messages in view until someone scrolls up, at which
	// point their position is left alone.
	following bool
}

func newChatPanel(app *tui.App, log *chatLog, sender chatSender) *chatPanel {
	p := &chatPanel{app: app, log: log, sender: sender, following: true}

	p.table = tview.NewTable()
	p.table.SetBorders(false).
		SetSelectable(true, false).
		SetSelectedStyle(tcell.StyleDefault.
			Background(tui.ColorPrimary).
			Foreground(tui.ColorText))
	p.table.SetInputCapture(p.handleTableKey)
	p.table.SetSelectionChangedFunc(func(row, _ int) {
		p.mu.Lock()
		defer p.mu.Unlock()
		// Sitting on the last row means "show me new messages as they
		// arrive"; anywhere else means someone is reading back.
		p.following = row >= len(p.rowMessage)-1
	})

	p.input = tview.NewInputField()
	p.input.SetLabel(" > ").
		SetLabelColor(tui.ColorAccent).
		SetFieldBackgroundColor(tcell.ColorDefault).
		SetPlaceholder("message").
		SetPlaceholderTextColor(tui.ColorMuted)
	p.input.SetInputCapture(p.handleInputKey)
	p.input.SetDoneFunc(p.handleDone)

	p.hint = tview.NewTextView()
	p.hint.SetDynamicColors(true)

	p.Flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(p.table, 0, 1, false).
		AddItem(p.hint, 1, 0, false).
		AddItem(p.input, 1, 0, false)
	p.SetBorder(true).
		SetTitle(" CHAT ").
		SetTitleColor(tui.ColorPrimary)
	tui.WireFocusBorder(p.table, p.Box)
	tui.WireFocusBorder(p.input, p.Box)

	p.render()
	return p
}

// childs are the panel's focusable primitives, in Tab order.
func (p *chatPanel) childs() []tview.Primitive {
	return []tview.Primitive{p.table, p.input}
}

// refresh repaints when the conversation or the available width has changed.
// Called from the board's tick, so it must be cheap when nothing moved.
func (p *chatPanel) refresh() {
	_, _, width, _ := p.table.GetInnerRect()

	p.mu.Lock()
	unchanged := p.log.rev() == p.drawnRev && width == p.drawnWidth
	p.mu.Unlock()
	if unchanged {
		return
	}
	p.render()
}

// bodyWidth is how wide a wrapped message body may be at the given thread
// depth.
//
// The author and timestamp sit on their own header line rather than in a left
// column: a chat pane beside the roster is narrow, and a meta column wide
// enough for "HH:MM <npub…> (you)" left too little for the message -- tview
// then ellipsized the author away, which is the one label that most needs
// reading. Stacking costs a row per message and gives the body the full width.
func (p *chatPanel) bodyWidth(depth int) int {
	_, _, width, _ := p.table.GetInnerRect()
	if width <= 0 {
		width = chatFallbackWidth
	}
	body := width - len(chatBodyIndent) - 2*depth - 1
	if body < 16 {
		// Below this the wrapping is unreadable whatever we do, and a floor
		// keeps wrapText from being handed a useless width.
		body = 16
	}
	return body
}

// render repaints the transcript. It touches tview state, so it runs only on
// tview's event loop -- the board's queueDraw is the way in.
func (p *chatPanel) render() {
	lines := p.log.lines()
	_, _, innerWidth, _ := p.table.GetInnerRect()

	p.table.Clear()

	rows := make([]chatMessage, 0, len(lines))
	self := p.sender.self()

	row := 0
	addRow := func(text string, color tcell.Color, msg chatMessage) {
		p.table.SetCell(row, 0, tview.NewTableCell(text).
			SetTextColor(color).
			SetExpansion(1))
		rows = append(rows, msg)
		row++
	}

	for _, line := range lines {
		indent := strings.Repeat("  ", line.Depth)
		marker := ""
		if line.Depth > 0 || line.Orphan {
			// A reply whose parent never arrived would otherwise read as the
			// start of a thread it is not the start of.
			marker = "↳ "
		}

		label := shortNpub(line.Author)
		labelColor := tui.ColorText
		if line.Author == self {
			label += " (you)"
			labelColor = tui.ColorPrimary
		}

		header := fmt.Sprintf("%s%s%s %s", indent, marker,
			time.Unix(int64(line.CreatedAt), 0).Format("15:04"), label)
		if line.Orphan && line.Parent != "" {
			header += " (replying to a message not here)"
		}
		addRow(" "+header, labelColor, line.chatMessage)

		body := line.Content
		for _, q := range line.Quotes {
			body += fmt.Sprintf(" [quoting %s]", shortID(q))
		}
		for _, segment := range wrapText(body, p.bodyWidth(line.Depth)) {
			addRow(" "+indent+chatBodyIndent+segment, tui.ColorText, line.chatMessage)
		}
	}

	if row == 0 {
		p.table.SetCell(0, 0, tview.NewTableCell(" (no messages yet)").
			SetTextColor(tui.ColorMuted).
			SetSelectable(false))
	}

	p.mu.Lock()
	p.rowMessage = rows
	p.drawnRev = p.log.rev()
	p.drawnWidth = innerWidth
	follow := p.following
	p.mu.Unlock()

	if follow && len(rows) > 0 {
		p.table.Select(len(rows)-1, 0)
		p.table.ScrollToEnd()
	}
	p.renderHint()
}

// renderHint shows what pressing send would do: a plain message, a reply, or a
// quote. Without it a reply looks identical to a new message until it lands.
func (p *chatPanel) renderHint() {
	p.mu.Lock()
	replying, reply := p.replying, p.replyTo
	quotes := append([]chatMessage(nil), p.quotes...)
	p.mu.Unlock()

	var parts []string
	if replying {
		parts = append(parts, fmt.Sprintf("[%s:-:b]replying to[%s:-:-] %s: %s",
			tui.ColorAccent, tui.ColorMuted, shortNpub(reply.Author), firstLine(reply.Content, 40)))
	}
	if len(quotes) > 0 {
		ids := make([]string, 0, len(quotes))
		for _, q := range quotes {
			ids = append(ids, shortID(q.ID))
		}
		parts = append(parts, fmt.Sprintf("[%s:-:b]quoting[%s:-:-] %s",
			tui.ColorAccent, tui.ColorMuted, strings.Join(ids, ", ")))
	}
	if len(parts) == 0 {
		p.hint.SetText(fmt.Sprintf(" [%s:-:-]<r> reply   <y> quote   <Esc> clear   <Enter> send", tui.ColorMuted))
		return
	}
	p.hint.SetText(" " + strings.Join(parts, "   ") + fmt.Sprintf("   [%s:-:-]<Esc> clear", tui.ColorMuted))
}

// selected is the message under the cursor, if any.
func (p *chatPanel) selected() (chatMessage, bool) {
	row, _ := p.table.GetSelection()
	p.mu.Lock()
	defer p.mu.Unlock()
	if row < 0 || row >= len(p.rowMessage) {
		return chatMessage{}, false
	}
	return p.rowMessage[row], true
}

func (p *chatPanel) handleTableKey(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyEscape:
		p.clearComposing()
		return nil
	case tcell.KeyEnter:
		p.focusInput()
		return nil
	}

	switch event.Rune() {
	case 'r', 'R':
		if msg, ok := p.selected(); ok {
			p.mu.Lock()
			p.replyTo, p.replying = msg, true
			p.mu.Unlock()
			p.renderHint()
			p.focusInput()
		}
		return nil
	case 'y', 'Y':
		if msg, ok := p.selected(); ok {
			p.addQuote(msg)
			p.renderHint()
			p.focusInput()
		}
		return nil
	case 'i':
		p.focusInput()
		return nil
	// q is the board's "leave", and must keep working from here rather than
	// being eaten as a chat key.
	case 'q', 'Q':
		return event
	}
	return event
}

func (p *chatPanel) handleInputKey(event *tcell.EventKey) *tcell.EventKey {
	if event.Key() == tcell.KeyEscape {
		// Esc clears the reply/quote rather than leaving the call: the
		// composer is the one place a stray Esc must not hang up.
		p.clearComposing()
		if p.app != nil {
			p.app.SetFocus(p.table)
		}
		return nil
	}
	return event
}

// handleDone fires on Enter (and on Esc, which handleInputKey already ate).
func (p *chatPanel) handleDone(key tcell.Key) {
	if key != tcell.KeyEnter {
		return
	}
	content := strings.TrimSpace(p.input.GetText())
	if content == "" {
		return
	}

	p.mu.Lock()
	parent := ""
	if p.replying {
		parent = p.replyTo.ID
	}
	quotes := make([]string, 0, len(p.quotes))
	for _, q := range p.quotes {
		quotes = append(quotes, q.ID)
	}
	p.mu.Unlock()

	p.input.SetText("")
	p.clearComposing()

	// Sending waits on the relay's OK, which must not happen on tview's event
	// loop -- the whole UI would freeze until the relay answered.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), chatSendTimeout+time.Second)
		defer cancel()

		if err := p.sender.send(ctx, content, parent, quotes); err != nil {
			p.queue(func() {
				p.hint.SetText(fmt.Sprintf(" [%s:-:b]not sent:[%s:-:-] %s",
					tui.ColorDanger, tui.ColorMuted, err))
			})
			return
		}
		// The message itself arrives back through the subscription, so there
		// is nothing to add locally -- what the panel shows is what the relay
		// actually accepted.
		p.queue(p.renderHint)
	}()
}

func (p *chatPanel) addQuote(msg chatMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, existing := range p.quotes {
		if existing.ID == msg.ID {
			return
		}
	}
	p.quotes = append(p.quotes, msg)
}

func (p *chatPanel) clearComposing() {
	p.mu.Lock()
	p.replying, p.replyTo, p.quotes = false, chatMessage{}, nil
	p.mu.Unlock()
	p.renderHint()
}

func (p *chatPanel) focusInput() {
	if p.app != nil {
		p.app.SetFocus(p.input)
	}
}

func (p *chatPanel) queue(fn func()) {
	if p.app == nil {
		fn()
		return
	}
	p.app.QueueUpdateDraw(fn)
}

// shortID truncates an event id for display.
func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:8] + "…"
}

// firstLine is a one-line preview of content, for the reply indicator.
func firstLine(content string, max int) string {
	flat := strings.ReplaceAll(strings.ReplaceAll(content, "\n", " "), "\r", " ")
	flat = strings.TrimSpace(flat)
	runes := []rune(flat)
	if len(runes) <= max {
		return flat
	}
	return string(runes[:max]) + "…"
}
