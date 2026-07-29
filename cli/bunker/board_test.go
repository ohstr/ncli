package bunker

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip46"
	"github.com/rivo/tview"
)

const testNpubPub = "c6565990f69f5e724e2c0584eb7ddedfa299bc9d55614e4efeb4e21cd99b0c64" // arbitrary valid hex pubkey

func TestFormatIdentity(t *testing.T) {
	npub := shortNpub(testNpubPub)
	tests := []struct {
		name string
		st   StatusInfo
		want string
	}{
		{
			name: "name and nip05 both resolved -- npub still shown",
			st:   StatusInfo{IdentityPub: testNpubPub, IdentityName: "Alice", IdentityNip05: "alice@example.com"},
			want: fmt.Sprintf("[%s]Alice[-:-:-] [%s](alice@example.com)[-:-:-] [%s](%s)[-:-:-]", tui.ColorText, tui.ColorMuted, tui.ColorMuted, npub),
		},
		{
			name: "name only -- npub still shown",
			st:   StatusInfo{IdentityPub: testNpubPub, IdentityName: "Alice"},
			want: fmt.Sprintf("[%s]Alice[-:-:-] [%s](%s)[-:-:-]", tui.ColorText, tui.ColorMuted, npub),
		},
		{
			name: "nip05 only -- npub still shown",
			st:   StatusInfo{IdentityPub: testNpubPub, IdentityNip05: "alice@example.com"},
			want: fmt.Sprintf("[%s]alice@example.com[-:-:-] [%s](%s)[-:-:-]", tui.ColorText, tui.ColorMuted, npub),
		},
		{
			name: "neither resolved falls back to just the npub, not repeated",
			st:   StatusInfo{IdentityPub: testNpubPub},
			want: fmt.Sprintf("[%s]%s[-:-:-]", tui.ColorText, npub),
		},
		{
			name: "vault label appended alongside name, nip05, and npub",
			st:   StatusInfo{IdentityPub: testNpubPub, IdentityName: "Alice", IdentityNip05: "alice@example.com", VaultLabel: "agent-key"},
			want: fmt.Sprintf("[%s]Alice[-:-:-] [%s](alice@example.com)[-:-:-] [%s](%s)[-:-:-] [%s](vault: agent-key)[-:-:-]", tui.ColorText, tui.ColorMuted, tui.ColorMuted, npub, tui.ColorMuted),
		},
		{
			name: "vault label appended to the npub fallback",
			st:   StatusInfo{IdentityPub: testNpubPub, VaultLabel: "agent-key"},
			want: fmt.Sprintf("[%s]%s[-:-:-] [%s](vault: agent-key)[-:-:-]", tui.ColorText, npub, tui.ColorMuted),
		},
		{
			name: "no vault label -- no suffix at all, not even an empty one",
			st:   StatusInfo{IdentityPub: testNpubPub, IdentityName: "Alice"},
			want: fmt.Sprintf("[%s]Alice[-:-:-] [%s](%s)[-:-:-]", tui.ColorText, tui.ColorMuted, npub),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatIdentity(tt.st); got != tt.want {
				t.Errorf("formatIdentity() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFullNpub(t *testing.T) {
	got := fullNpub(testNpubPub)
	if !strings.HasPrefix(got, "npub1") {
		t.Errorf("fullNpub() = %q, want it to start with npub1", got)
	}
	if strings.Contains(got, "...") {
		t.Errorf("fullNpub() = %q, want the complete npub, not shortNpub's truncated shape", got)
	}

	if got := fullNpub("not-a-valid-pubkey"); got != "not-a-valid-pubkey" {
		t.Errorf("fullNpub() on an unencodable key = %q, want the raw input back", got)
	}
}

func TestShortNpub(t *testing.T) {
	got := shortNpub(testNpubPub)
	if !strings.HasPrefix(got, "npub1") {
		t.Errorf("shortNpub() = %q, want it to start with npub1", got)
	}
	if !strings.Contains(got, "...") {
		t.Errorf("shortNpub() = %q, want a truncated %q shape", got, "npub1xxx...xxxx")
	}
	if len(got) >= len("npub1cet9ny8kna08yn3vqkzwklw7m73fn0ya24s5unh7kn3pekvmp3jqayth7d") {
		t.Errorf("shortNpub() = %q (len %d), want it shorter than the full npub", got, len(got))
	}

	if got := shortNpub("not-a-valid-pubkey"); got != shortHex("not-a-valid-pubkey") {
		t.Errorf("shortNpub() on an unencodable key = %q, want the shortHex fallback %q", got, shortHex("not-a-valid-pubkey"))
	}
}

func TestFormatRelayStatuses(t *testing.T) {
	tests := []struct {
		name     string
		statuses []RelayStatus
		want     string
	}{
		{
			name: "none configured",
			want: fmt.Sprintf("[%s]none configured[-:-:-]", tui.ColorMuted),
		},
		{
			name:     "one connected",
			statuses: []RelayStatus{{URL: "wss://relay.ohstr.com", Connected: true}},
			want:     fmt.Sprintf("[%s]●[-:-:-] wss://relay.ohstr.com", tui.ColorSuccess),
		},
		{
			name:     "one disconnected",
			statuses: []RelayStatus{{URL: "wss://relay.primal.net", Connected: false}},
			want:     fmt.Sprintf("[%s]○[-:-:-] wss://relay.primal.net", tui.ColorDanger),
		},
		{
			name: "mixed",
			statuses: []RelayStatus{
				{URL: "wss://a.example", Connected: true},
				{URL: "wss://b.example", Connected: false},
			},
			want: fmt.Sprintf("[%s]●[-:-:-] wss://a.example   [%s]○[-:-:-] wss://b.example", tui.ColorSuccess, tui.ColorDanger),
		},
		{
			name:     "still trying its first connection -- yellow, not red",
			statuses: []RelayStatus{{URL: "wss://relay.ohstr.com", Connecting: true}},
			want:     fmt.Sprintf("[%s]◐[-:-:-] wss://relay.ohstr.com", tui.ColorWarning),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatRelayStatuses(tt.statuses); got != tt.want {
				t.Errorf("formatRelayStatuses() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasPendingConnect(t *testing.T) {
	tests := []struct {
		name    string
		pending []Pending
		want    bool
	}{
		{name: "empty", pending: nil, want: false},
		{name: "no connect requests", pending: []Pending{{Method: nip46.MethodSignEvent}, {Method: nip46.MethodPing}}, want: false},
		{name: "has a connect request", pending: []Pending{{Method: nip46.MethodSignEvent}, {Method: nip46.MethodConnect}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasPendingConnect(tt.pending); got != tt.want {
				t.Errorf("hasPendingConnect() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatWaitingStatus(t *testing.T) {
	t.Run("well before the deadline shows a countdown", func(t *testing.T) {
		got := formatWaitingStatus(time.Now().Add(4*time.Minute + 30*time.Second))
		if !strings.Contains(got, "Waiting for a client to connect") {
			t.Errorf("formatWaitingStatus() = %q, want it to mention waiting", got)
		}
		if !strings.Contains(got, "4m") {
			t.Errorf("formatWaitingStatus() = %q, want a ~4m countdown", got)
		}
		if strings.Contains(got, "expired") {
			t.Errorf("formatWaitingStatus() = %q, want it not to claim expiry yet", got)
		}
	})

	t.Run("past the deadline shows expired, not a negative countdown", func(t *testing.T) {
		got := formatWaitingStatus(time.Now().Add(-time.Second))
		if !strings.Contains(got, "expired") {
			t.Errorf("formatWaitingStatus() = %q, want it to say expired", got)
		}
		if strings.Contains(got, "expires in") {
			t.Errorf("formatWaitingStatus() = %q, must not also show a (necessarily negative) countdown", got)
		}
	})
}

// logsOnlyClient is a minimal BunkerClient stub -- just enough for
// DaemonLogWatcher.Update, which only ever calls Logs().
type logsOnlyClient struct {
	BunkerClient
	snap LogSnapshot
	err  error
}

func (c *logsOnlyClient) Logs() (LogSnapshot, error) { return c.snap, c.err }

func TestDaemonLogWatcher_Update(t *testing.T) {
	// GetLastLogs() is itself a consuming read (drains everything logged
	// since the last call -- see model.go), so each check below only
	// covers what Update() fed in *since the previous GetLastLogs() call*,
	// not a running total.
	client := &logsOnlyClient{snap: LogSnapshot{Lines: []string{"a", "b"}, Total: 2}}
	fl := &tui.FlowLogger{}
	w := NewDaemonLogWatcher(nil, client, fl)

	w.Update()
	if got := len(fl.GetLastLogs()); got != 2 {
		t.Fatalf("after first Update, GetLastLogs() returned %d entries, want 2", got)
	}
	if w.shown != 2 {
		t.Errorf("shown = %d, want 2", w.shown)
	}

	// A second Update with nothing new must not re-log the same lines.
	w.Update()
	if got := len(fl.GetLastLogs()); got != 0 {
		t.Errorf("after a no-op Update, GetLastLogs() returned %d entries, want 0 (no duplicates)", got)
	}

	// New lines arrive appended to the tail; only the new one should log.
	client.snap = LogSnapshot{Lines: []string{"a", "b", "c"}, Total: 3}
	w.Update()
	if got := len(fl.GetLastLogs()); got != 1 {
		t.Errorf("after one new line arrived, GetLastLogs() returned %d entries, want 1", got)
	}

	// The tail rotated (old lines dropped) but Total kept counting past
	// what's still in Lines -- Update must not panic slicing past the end,
	// and must catch shown back up to Total rather than getting stuck.
	client.snap = LogSnapshot{Lines: []string{"y", "z"}, Total: 10}
	w.Update()
	fl.GetLastLogs() // drain, not asserted -- best-effort content once some lines are unrecoverably gone
	if w.shown != 10 {
		t.Errorf("shown = %d, want it to catch up to Total (10) even though some lines rotated out", w.shown)
	}
}

func TestSessionsHeightFor(t *testing.T) {
	tests := []struct {
		name  string
		count int
		want  int
	}{
		{name: "zero sessions clamps to the minimum", count: 0, want: minSessionsHeight},
		{name: "a couple sessions still clamps to the minimum", count: 1, want: minSessionsHeight},
		{name: "grows with content once past the minimum", count: 4, want: 8},
		{name: "many sessions clamps to the maximum", count: 50, want: maxSessionsHeight},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionsHeightFor(tt.count); got != tt.want {
				t.Errorf("sessionsHeightFor(%d) = %d, want %d", tt.count, got, tt.want)
			}
		})
	}
}

func TestUrgencyColor(t *testing.T) {
	tests := []struct {
		name      string
		remaining time.Duration
		wantColor tcell.Color
		wantOK    bool
	}{
		{name: "comfortable remaining time is left alone", remaining: 10 * time.Minute, wantOK: false},
		{name: "under 2 minutes turns yellow", remaining: time.Minute, wantColor: tui.ColorWarning, wantOK: true},
		{name: "under 30 seconds turns red", remaining: 10 * time.Second, wantColor: tui.ColorDanger, wantOK: true},
		{name: "already expired is still red, not a crash on a negative duration", remaining: -time.Second, wantColor: tui.ColorDanger, wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			color, ok := urgencyColor(time.Now().Add(tt.remaining))
			if ok != tt.wantOK {
				t.Fatalf("urgencyColor() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && color != tt.wantColor {
				t.Errorf("urgencyColor() color = %v, want %v", color, tt.wantColor)
			}
		})
	}
}

func TestHistoryStatus(t *testing.T) {
	tests := []struct {
		name      string
		entry     HistoryEntry
		wantText  string
		wantColor tcell.Color
	}{
		{name: "approved once", entry: HistoryEntry{Verdict: Allow}, wantText: "Approved", wantColor: tui.ColorSuccess},
		{name: "approved always", entry: HistoryEntry{Verdict: Allow, Remembered: true}, wantText: "Approved (always)", wantColor: tui.ColorSuccess},
		{name: "rejected once", entry: HistoryEntry{Verdict: Deny}, wantText: "Rejected", wantColor: tui.ColorDanger},
		{name: "rejected always", entry: HistoryEntry{Verdict: Deny, Remembered: true}, wantText: "Rejected (always)", wantColor: tui.ColorDanger},
		// Expired takes priority over Verdict/Remembered -- sweepExpired
		// always sets Verdict=Deny alongside Expired=true, but "expired,
		// nobody actually decided" is a materially different outcome from
		// "a human said no," and must never be labeled/colored as the same.
		{name: "expired overrides any verdict/remembered combination", entry: HistoryEntry{Verdict: Deny, Remembered: true, Expired: true}, wantText: "Expired", wantColor: tui.ColorWarning},
		// AutoApproved takes priority over everything else too, for the
		// same reason Expired does: a request an existing grant already
		// covered never went through a human decision (or Queue.Add) at
		// all, so it must read distinctly from both "Approved" (a one-off
		// decision, just made) and "Approved (always)" (a new grant, just
		// remembered) -- see followup issue in
		// integration/agent-eval/followup/issues.md.
		{name: "auto-approved overrides verdict/remembered", entry: HistoryEntry{Verdict: Allow, Remembered: true, AutoApproved: true}, wantText: "Auto-approved", wantColor: tui.ColorSuccess},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, color := historyStatus(tt.entry)
			if text != tt.wantText {
				t.Errorf("historyStatus() text = %q, want %q", text, tt.wantText)
			}
			if color != tt.wantColor {
				t.Errorf("historyStatus() color = %v, want %v", color, tt.wantColor)
			}
			if strings.Contains(text, "[") {
				t.Errorf("historyStatus() text = %q, must not contain tview color-tag markup (reused as-is for plain CLI output -- see newHistoryCommand)", text)
			}
		})
	}
}

// fakePendingClient is a minimal, directly-configurable BunkerClient stub
// for exercising PendingTable's own input-capture/resolve logic without a
// real Daemon or IPC socket -- records the last Approve/Reject call so
// tests can assert on it.
type fakePendingClient struct {
	BunkerClient
	pending  []Pending
	sessions []Session

	approvedID string
	rejectedID string
	lastGrant  *Grant
}

func (c *fakePendingClient) ListPending() ([]Pending, error)  { return c.pending, nil }
func (c *fakePendingClient) ListSessions() ([]Session, error) { return c.sessions, nil }

func (c *fakePendingClient) Approve(id string, remember *Grant) error {
	c.approvedID = id
	c.lastGrant = remember
	return nil
}

func (c *fakePendingClient) Reject(id string, remember *Grant) error {
	c.rejectedID = id
	c.lastGrant = remember
	return nil
}

func newTestPendingTable(t *testing.T, client *fakePendingClient) *PendingTable {
	t.Helper()
	app := tui.NewApp()
	pt := NewPendingTable(app, client, true)
	pt.Init(t.Context())
	pt.table.Select(1, 0) // the only data row once Init's synchronous Update runs
	return pt
}

// TestPendingTableQuickApproveKey guards the 'a' hotkey: the common
// no-frills "Approve Once" decision on the selected row, in a single
// keypress, without opening openApprovalDialog first.
func TestPendingTableQuickApproveKey(t *testing.T) {
	client := &fakePendingClient{pending: []Pending{{ID: "req-1", ClientKey: "abc", Method: nip46.MethodPing}}}
	pt := newTestPendingTable(t, client)

	capture := pt.GetInputCapture()
	if capture == nil {
		t.Fatal("expected Init to install an input capture on the table itself")
	}

	aKey := tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone)
	if got := capture(aKey); got != nil {
		t.Fatal("expected 'a' to be swallowed (capture returns nil)")
	}
	if client.approvedID != "req-1" {
		t.Errorf("Approve called with id %q, want %q", client.approvedID, "req-1")
	}
	if client.lastGrant != nil {
		t.Errorf("quick-approve must resolve once only (nil grant), got %+v", client.lastGrant)
	}
	if client.rejectedID != "" {
		t.Errorf("expected Reject not to be called, got id %q", client.rejectedID)
	}
}

// TestPendingTableQuickRejectKey mirrors the approve case for 'r'.
func TestPendingTableQuickRejectKey(t *testing.T) {
	client := &fakePendingClient{pending: []Pending{{ID: "req-1", ClientKey: "abc", Method: nip46.MethodPing}}}
	pt := newTestPendingTable(t, client)

	capture := pt.GetInputCapture()
	rKey := tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModNone)
	if got := capture(rKey); got != nil {
		t.Fatal("expected 'r' to be swallowed (capture returns nil)")
	}
	if client.rejectedID != "req-1" {
		t.Errorf("Reject called with id %q, want %q", client.rejectedID, "req-1")
	}
	if client.lastGrant != nil {
		t.Errorf("quick-reject must resolve once only (nil grant), got %+v", client.lastGrant)
	}
	if client.approvedID != "" {
		t.Errorf("expected Approve not to be called, got id %q", client.approvedID)
	}
}

// TestPendingTableQuickResolveNoSelectionIsNoop guards against a panic or
// a spurious Approve/Reject call when the table is empty (no pending
// requests, nothing selected) and the operator hits 'a'/'r' anyway.
func TestPendingTableQuickResolveNoSelectionIsNoop(t *testing.T) {
	client := &fakePendingClient{}
	app := tui.NewApp()
	pt := NewPendingTable(app, client, true)
	pt.Init(t.Context())

	capture := pt.GetInputCapture()
	capture(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone))
	capture(tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModNone))

	if client.approvedID != "" || client.rejectedID != "" {
		t.Errorf("expected no resolve on an empty table, got approvedID=%q rejectedID=%q", client.approvedID, client.rejectedID)
	}
}

// TestPendingTableInitFetchesSynchronously guards the cold-start fix:
// without a synchronous Update in Init, rendered (and so the table's
// title/rows) would stay empty until the first render tick, up to
// renderInterval later -- misleadingly showing "PENDING REQUESTS [0]"
// even when requests are already waiting the instant the TUI opens.
func TestPendingTableInitFetchesSynchronously(t *testing.T) {
	client := &fakePendingClient{pending: []Pending{{ID: "req-1", ClientKey: "abc", Method: nip46.MethodPing}}}
	app := tui.NewApp()
	pt := NewPendingTable(app, client, true)
	pt.Init(t.Context())

	if got := pt.table.GetCell(1, 2).Text; got != nip46.MethodPing {
		t.Errorf("after Init, row 1's Method cell = %q, want %q populated synchronously", got, nip46.MethodPing)
	}
}

// TestPendingTableAutoPromptDefaultOn guards the Auto-Prompt behavior
// requested for the connect flow: with a request already pending the
// instant the table exists (Init's own synchronous Update -> Update's
// maybeAutoPrompt tail call), the approval dialog should be marked shown
// for it without the operator having to select the row and press Enter
// first.
func TestPendingTableAutoPromptDefaultOn(t *testing.T) {
	client := &fakePendingClient{pending: []Pending{{ID: "req-1", ClientKey: "abc", Method: nip46.MethodPing}}}
	app := tui.NewApp()
	pt := NewPendingTable(app, client, true)
	pt.Init(t.Context())

	if !pt.autoPrompt {
		t.Error("autoPrompt = false, want true (on by default)")
	}
	if pt.promptedID != "req-1" {
		t.Errorf("promptedID = %q, want %q -- Init's synchronous Update should have auto-shown the only pending request", pt.promptedID, "req-1")
	}
}

// TestPendingTableAutoPromptToggleKey guards 'p': it must flip autoPrompt
// and refresh the actions bar's On/Off label.
func TestPendingTableAutoPromptToggleKey(t *testing.T) {
	client := &fakePendingClient{}
	app := tui.NewApp()
	pt := NewPendingTable(app, client, true)
	pt.Init(t.Context())

	if !strings.Contains(pt.actions.GetText(true), "Auto-Prompt:") {
		t.Fatalf("actions bar = %q, want an Auto-Prompt label", pt.actions.GetText(true))
	}

	capture := pt.GetInputCapture()
	if got := capture(tcell.NewEventKey(tcell.KeyRune, 'p', tcell.ModNone)); got != nil {
		t.Fatal("expected 'p' to be swallowed (capture returns nil)")
	}
	if pt.autoPrompt {
		t.Error("autoPrompt = true after one 'p' press, want false")
	}

	if got := capture(tcell.NewEventKey(tcell.KeyRune, 'p', tcell.ModNone)); got != nil {
		t.Fatal("expected 'p' to be swallowed (capture returns nil)")
	}
	if !pt.autoPrompt {
		t.Error("autoPrompt = false after two 'p' presses, want true")
	}
}

// TestPendingTableDecideLater guards the "Decide Later" safety valve
// (openApprovalDialog's last button, and Dialog's Esc target): it must
// leave the request untouched (no Approve/Reject call) while switching
// Auto-Prompt off, so a still-pending, still-undecided request can't spin
// the chain into reopening the very same dialog forever.
func TestPendingTableDecideLater(t *testing.T) {
	client := &fakePendingClient{pending: []Pending{{ID: "req-1", ClientKey: "abc", Method: nip46.MethodPing}}}
	app := tui.NewApp()
	pt := NewPendingTable(app, client, true)
	pt.Init(t.Context())

	if pt.promptedID != "req-1" {
		t.Fatalf("promptedID = %q, want %q before Decide Later", pt.promptedID, "req-1")
	}

	pt.deferDecision("req-1")

	if pt.autoPrompt {
		t.Error("autoPrompt = true after Decide Later, want false")
	}
	if pt.promptedID != "" {
		t.Errorf("promptedID = %q after Decide Later, want empty", pt.promptedID)
	}
	if client.approvedID != "" || client.rejectedID != "" {
		t.Errorf("Decide Later must not resolve the request, got approvedID=%q rejectedID=%q", client.approvedID, client.rejectedID)
	}
}

// TestPendingTableResolveClearsPromptedID guards resolve's own state
// bookkeeping (the synchronous half -- the deferred tryPromptNext
// follow-up needs a running Application event loop to observe and isn't
// exercised here): once the request that was shown gets resolved,
// promptedID must clear so the next poll tick (or toggle) is free to show
// another.
func TestPendingTableResolveClearsPromptedID(t *testing.T) {
	client := &fakePendingClient{pending: []Pending{{ID: "req-1", ClientKey: "abc", Method: nip46.MethodPing}}}
	pt := newTestPendingTable(t, client)

	if pt.promptedID != "req-1" {
		t.Fatalf("promptedID = %q, want %q before resolving", pt.promptedID, "req-1")
	}

	pt.resolve("req-1", Allow, nil)

	if pt.promptedID != "" {
		t.Errorf("promptedID = %q after resolve, want empty", pt.promptedID)
	}
	if client.approvedID != "req-1" {
		t.Errorf("approvedID = %q, want %q", client.approvedID, "req-1")
	}
}

// waitForAutoPromptFocus starts a real Application.Run() loop backed by a
// simulated screen and waits for the auto-prompted dialog to actually
// open. Needed since maybeAutoPrompt (board.go) now defers the open via
// deferFollowUpDialog -- see that function's own doc comment -- so the
// goroutine it spawns only actually runs the open once Run() is really
// draining its update queue, not synchronously inside Init() the way it
// used to. t.Cleanup stops the app once the test finishes. Returns the
// screen too, for a caller that wants to inspect the rendered buffer
// (e.g. confirming an overlay's own border reaches the actual screen
// edges), not just the focused primitive.
func waitForAutoPromptFocus(t *testing.T, app *tui.App) (tview.Primitive, tcell.SimulationScreen) {
	t.Helper()

	screen := tcell.NewSimulationScreen("")
	screen.SetSize(80, 25)
	app.SetScreen(screen)

	go app.Run()
	t.Cleanup(app.Stop)

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if focus := app.GetFocus(); focus != nil {
			return focus, screen
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("auto-prompted dialog never opened within the deadline")
	return nil, screen
}

// TestPendingTableSignEventApprovalOverlayApproveButtonWorks guards
// openApprovalDialog's sign_event branch (openSignEventApprovalDialog):
// unlike every other method, which still opens the plain ShowDialog Modal,
// a sign_event request with Event set must route to the custom
// ShowOverlay-based dialog that shows the unsigned JSON above the same
// button row -- and that overlay's buttons must actually still work.
// Modal/Form both ultimately delegate focus down to a *tview.Button (see
// tview's Modal.Focus/Form.Focus), so button 0 ("Approve Once") getting
// focus doesn't by itself prove the overlay opened; actually firing its
// Enter handler and observing the fake client react is what proves the
// wiring survived the new code path end to end.
func TestPendingTableSignEventApprovalOverlayApproveButtonWorks(t *testing.T) {
	event := &nip01.Event{ID: "abcd", PubKey: "ef01", Kind: 1, Content: "hello", CreatedAt: 1}
	client := &fakePendingClient{pending: []Pending{{ID: "req-1", ClientKey: "abc", Method: nip46.MethodSignEvent, Kind: 1, Event: event}}}
	app := tui.NewApp()
	pt := NewPendingTable(app, client, true)
	pt.Init(t.Context())

	if pt.promptedID != "req-1" {
		t.Fatalf("promptedID = %q, want %q", pt.promptedID, "req-1")
	}

	focus, _ := waitForAutoPromptFocus(t, app)
	button, ok := focus.(*tview.Button)
	if !ok {
		t.Fatalf("GetFocus() = %T, want *tview.Button (the overlay's first button)", app.GetFocus())
	}
	if got := button.GetLabel(); got != "Approve Once" {
		t.Fatalf("focused button label = %q, want %q", got, "Approve Once")
	}

	button.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(p tview.Primitive) { app.SetFocus(p) })

	if client.approvedID != "req-1" {
		t.Errorf("Approve was not called through the sign_event overlay's button, approvedID = %q", client.approvedID)
	}
}

// TestPendingTableSignEventApprovalOverlayRejectButtonWorks Tab-walks to
// the overlay's "Reject Once" button (kind 0 is a sensitiveKind, so no
// "Always: any kind" button sits between "Always: kind 0" and it -- see
// policy.go's sensitiveKinds) and confirms it maps to the same Reject
// action the plain Modal path's identically-labeled button uses --
// guarding against a stale closure over the loop variable in
// openSignEventApprovalDialog's button-building loop, which would silently
// wire every button to the last func instead of its own.
func TestPendingTableSignEventApprovalOverlayRejectButtonWorks(t *testing.T) {
	event := &nip01.Event{ID: "abcd", PubKey: "ef01", Kind: 0, Content: "{}", CreatedAt: 1}
	client := &fakePendingClient{pending: []Pending{{ID: "req-1", ClientKey: "abc", Method: nip46.MethodSignEvent, Kind: 0, Event: event}}}
	app := tui.NewApp()
	pt := NewPendingTable(app, client, true)
	pt.Init(t.Context())

	if pt.promptedID != "req-1" {
		t.Fatalf("promptedID = %q, want %q", pt.promptedID, "req-1")
	}

	waitForAutoPromptFocus(t, app)

	setFocus := func(p tview.Primitive) { app.SetFocus(p) }
	tab := tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
	for i := 0; i < 2; i++ {
		button, ok := app.GetFocus().(*tview.Button)
		if !ok {
			t.Fatalf("GetFocus() = %T, want *tview.Button", app.GetFocus())
		}
		button.InputHandler()(tab, setFocus)
	}

	button, ok := app.GetFocus().(*tview.Button)
	if !ok {
		t.Fatalf("GetFocus() = %T, want *tview.Button", app.GetFocus())
	}
	if got := button.GetLabel(); got != "Reject Once" {
		t.Fatalf("focused button label after 2 Tabs = %q, want %q", got, "Reject Once")
	}
	button.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), setFocus)

	if client.rejectedID != "req-1" {
		t.Errorf("Reject was not called through the sign_event overlay's Reject Once button, rejectedID = %q", client.rejectedID)
	}
}

// TestPendingTableSignEventApprovalFallsBackWithoutEvent guards
// openApprovalDialog's defensive branch condition (p.Event != nil, not
// just p.Method == sign_event): a sign_event Pending missing its Event
// (unreachable via Handler.Handle today, but not guaranteed by the type
// system) must still fall back to the plain Modal path rather than opening
// an overlay with nothing to show, and must not panic.
func TestPendingTableSignEventApprovalFallsBackWithoutEvent(t *testing.T) {
	client := &fakePendingClient{pending: []Pending{{ID: "req-1", ClientKey: "abc", Method: nip46.MethodSignEvent, Kind: 1, Event: nil}}}
	app := tui.NewApp()
	pt := NewPendingTable(app, client, true)
	pt.Init(t.Context())

	if pt.promptedID != "req-1" {
		t.Fatalf("promptedID = %q, want %q", pt.promptedID, "req-1")
	}
	focus, _ := waitForAutoPromptFocus(t, app)
	if focus == nil {
		t.Fatal("expected the fallback Modal to hold focus")
	}
}

// TestPendingTableAlwaysButtonOpensDurationDialog guards a regression in
// openApprovalDialog's plain-Modal path (any non-sign_event method, e.g.
// nip04_decrypt): its "Always" button called t.openDurationDialog directly
// instead of going through deferFollowUpDialog like every other
// button-opens-a-new-dialog case here (resolve, openDurationDialog's own
// "Cancel"). Since this dialog is shown via ShowDialog, whose wrapper runs
// fn() and *then* unconditionally SwitchToPage("main") (see App.ShowDialog),
// calling openDurationDialog synchronously meant the duration dialog it
// opened was immediately hidden again by that trailing SwitchToPage before a
// frame ever rendered it -- from the operator's seat, clicking "Always" just
// closed the approval dialog and showed nothing.
// Needs the full board (App.Init + Load), not a bare PendingTable, for two
// reasons the bug only shows up under: (1) ShowDialog's own wrapper
// (App.ShowDialog) does `a.Focus(a.lastFocusedIndex)` after fn() returns,
// which is a no-op when a.childs is nil (a bare PendingTable never sets it),
// silently masking the real "focus stolen back to the board" symptom; and
// (2) Application.draw's root == nil early return means nothing is ever
// actually drawn to inspect, matching
// TestPendingTableSignEventApprovalOverlayFillsFullScreenNoMargin's own doc
// comment about why it needs the same setup.
func TestPendingTableAlwaysButtonOpensDurationDialog(t *testing.T) {
	client := &fullBoardTestClient{status: StatusInfo{IdentityPub: testNpubPub}}
	client.setPending([]Pending{{ID: "req-1", ClientKey: "abc", Method: nip46.MethodNIP04Decrypt}})

	app := tui.NewApp().Init()
	flowLogger := &tui.FlowLogger{}
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(80, 25)
	app.SetScreen(screen)

	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)
	app.Load(board)

	go app.Run()
	defer app.Stop()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if b, ok := app.GetFocus().(*tview.Button); ok && b.GetLabel() == "Approve Once" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	button, ok := app.GetFocus().(*tview.Button)
	if !ok || button.GetLabel() != "Approve Once" {
		t.Fatalf("approval dialog never opened, focus = %v", app.GetFocus())
	}

	setFocus := func(p tview.Primitive) { app.SetFocus(p) }
	app.QueueUpdateDraw(func() { button.InputHandler()(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), setFocus) })

	button, ok = app.GetFocus().(*tview.Button)
	if !ok || button.GetLabel() != "Always" {
		t.Fatalf("focused button after 1 Tab = %v, want %q", app.GetFocus(), "Always")
	}
	app.QueueUpdateDraw(func() { button.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), setFocus) })

	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if b, ok := app.GetFocus().(*tview.Button); ok && b.GetLabel() == "1 hour" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("duration dialog never opened after Always, focus = %v -- ShowDialog's own SwitchToPage(\"main\")+Focus(lastFocusedIndex) likely clobbered it", app.GetFocus())
}

// fakeSessionsClient is a minimal, directly-configurable BunkerClient stub
// for exercising SessionsTable's own Update/onCountChange wiring.
type fakeSessionsClient struct {
	BunkerClient
	sessions []Session
	history  []HistoryEntry

	// setNamePubkey/setNameValue record the last SetName call, so a test
	// can assert on it without a real Store -- mirrors fakePendingClient's
	// own approvedID/rejectedID recording of Approve/Reject.
	setNamePubkey string
	setNameValue  string

	revokedGrantPubkey, revokedGrantMethod string
	revokedGrantKind                       *int
	setGrantPubkey                         string
	setGrantValue                          Grant
}

func (c *fakeSessionsClient) ListSessions() ([]Session, error) { return c.sessions, nil }
func (c *fakeSessionsClient) History() ([]HistoryEntry, error) { return c.history, nil }

// RevokeGrant/SetGrant mutate c.sessions in place (not just record the
// call, like setNamePubkey/setNameValue above) -- openGrantsOverlay's own
// reopenGrantsOverlay re-fetches via ListSessions after either one, so a
// test asserting on the *reopened* overlay's contents needs the fake's
// own state to actually reflect the mutation, the same way a real Store
// would.
func (c *fakeSessionsClient) RevokeGrant(pubkey, method string, kind *int) (bool, error) {
	c.revokedGrantPubkey, c.revokedGrantMethod, c.revokedGrantKind = pubkey, method, kind
	for i := range c.sessions {
		if c.sessions[i].Pubkey != pubkey {
			continue
		}
		for j, g := range c.sessions[i].Grants {
			if g.Method == method && sameScope(g, Grant{Method: method, Kind: kind}) {
				c.sessions[i].Grants = append(c.sessions[i].Grants[:j], c.sessions[i].Grants[j+1:]...)
				return true, nil
			}
		}
	}
	return false, nil
}

func (c *fakeSessionsClient) SetGrant(pubkey string, grant Grant) error {
	c.setGrantPubkey, c.setGrantValue = pubkey, grant
	for i := range c.sessions {
		if c.sessions[i].Pubkey == pubkey {
			c.sessions[i].Grants = append(c.sessions[i].Grants, grant)
			return nil
		}
	}
	return nil
}

func (c *fakeSessionsClient) SetName(pubkey, name string) (bool, error) {
	c.setNamePubkey, c.setNameValue = pubkey, name
	return true, nil
}

// TestSessionsTableOnCountChangeFiresOnUpdate guards the height-tracking
// fix in NewBunkerBoard: SessionsTable must report its real, freshly-
// fetched count on every Update (including the synchronous one Init now
// performs), not just once at construction when rendered is still nil.
func TestSessionsTableOnCountChangeFiresOnUpdate(t *testing.T) {
	client := &fakeSessionsClient{sessions: []Session{{Pubkey: "a"}, {Pubkey: "b"}, {Pubkey: "c"}}}
	app := tui.NewApp()
	st := NewSessionsTable(app, client)

	var gotCount int
	calls := 0
	st.onCountChange = func(n int) { gotCount = n; calls++ }

	st.Init(t.Context())

	if calls == 0 {
		t.Fatal("expected onCountChange to fire from Init's synchronous Update")
	}
	if gotCount != 3 {
		t.Errorf("onCountChange got count %d, want 3", gotCount)
	}
}

// TestWireButtonArrowNavMovesBetweenButtonsButNotWhileEditing guards
// wireButtonArrowNav, which openNostrconnectInput and openRenameInput both
// use so their Connect/Cancel and Save/Cancel buttons support Left/Right
// the same way ShowDialog's tview.Modal-based dialogs already do (Modal
// remaps arrow keys to Tab/Backtab itself; a plain tview.Form has no such
// mapping otherwise -- Button.InputHandler only reacts to Enter/Tab/
// Backtab/Escape). Driven through form.InputHandler() (not the focused
// child's InputHandler() directly, unlike this file's Tab-only tests
// above/below) since SetInputCapture only ever runs as part of the Form's
// own wrapped InputHandler -- calling a child's InputHandler straight
// from a test would skip the capture entirely and silently prove nothing.
func TestWireButtonArrowNavMovesBetweenButtonsButNotWhileEditing(t *testing.T) {
	app := tui.NewApp()
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(80, 25)
	app.SetScreen(screen)

	input := tview.NewInputField().SetLabel("Name: ").SetFieldWidth(0)
	form := tview.NewForm()
	form.AddFormItem(input)
	form.AddButton("Connect", func() {})
	form.AddButton("Cancel", func() {})
	wireButtonArrowNav(form, input)

	app.SetFocus(form)
	setFocus := func(p tview.Primitive) { app.SetFocus(p) }
	left := tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone)
	right := tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone)
	tab := tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)

	// While the InputField has focus, Left/Right must keep moving the text
	// cursor, not jump to a button -- the whole reason wireButtonArrowNav
	// takes skip and checks its focus first.
	form.InputHandler()(left, setFocus)
	if _, ok := app.GetFocus().(*tview.InputField); !ok {
		t.Fatalf("GetFocus() after Left while editing = %T, want *tview.InputField", app.GetFocus())
	}
	form.InputHandler()(right, setFocus)
	if _, ok := app.GetFocus().(*tview.InputField); !ok {
		t.Fatalf("GetFocus() after Right while editing = %T, want *tview.InputField", app.GetFocus())
	}

	form.InputHandler()(tab, setFocus)
	button, ok := app.GetFocus().(*tview.Button)
	if !ok || button.GetLabel() != "Connect" {
		t.Fatalf("focus after Tab off the field = %v, want %q", app.GetFocus(), "Connect")
	}

	form.InputHandler()(right, setFocus)
	button, ok = app.GetFocus().(*tview.Button)
	if !ok || button.GetLabel() != "Cancel" {
		t.Fatalf("focus after Right = %v, want %q", app.GetFocus(), "Cancel")
	}

	form.InputHandler()(left, setFocus)
	button, ok = app.GetFocus().(*tview.Button)
	if !ok || button.GetLabel() != "Connect" {
		t.Fatalf("focus after Left = %v, want %q", app.GetFocus(), "Connect")
	}
}

// TestSessionsTableRenameModalPrefillSaveUnmodified guards openRenameInput's
// pre-fill: opening it for a session that already has a Nickname shows
// that Nickname in the InputField, and saving without editing it calls
// SetName with that same unchanged text (not blank, not doubled).
func TestSessionsTableRenameModalPrefillSaveUnmodified(t *testing.T) {
	client := &fakeSessionsClient{sessions: []Session{{Pubkey: "abc123", Nickname: "Old Name"}}}
	app := tui.NewApp()
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(80, 25)
	app.SetScreen(screen) // openRenameInput's positionedOverlayRect needs a real screen size, even without a running app.Run() loop
	st := NewSessionsTable(app, client)
	st.Init(t.Context())

	s, ok := st.sessionAt(1)
	if !ok {
		t.Fatal("sessionAt(1) not found")
	}
	st.openRenameInput(s)

	field, ok := app.GetFocus().(*tview.InputField)
	if !ok {
		t.Fatalf("GetFocus() = %T, want *tview.InputField (pre-filled with the current Nickname)", app.GetFocus())
	}
	if got := field.GetText(); got != "Old Name" {
		t.Errorf("input pre-fill = %q, want %q", got, "Old Name")
	}

	setFocus := func(p tview.Primitive) { app.SetFocus(p) }
	field.InputHandler()(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), setFocus)

	button, ok := app.GetFocus().(*tview.Button)
	if !ok {
		t.Fatalf("GetFocus() after Tab = %T, want *tview.Button", app.GetFocus())
	}
	if got := button.GetLabel(); got != "Save" {
		t.Fatalf("focused button label = %q, want %q", got, "Save")
	}
	button.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), setFocus)

	if client.setNamePubkey != "abc123" || client.setNameValue != "Old Name" {
		t.Errorf("SetName called with (%q, %q), want (\"abc123\", \"Old Name\")", client.setNamePubkey, client.setNameValue)
	}
}

// TestSessionsTableRenameModalTypedNameAndSave covers the other half:
// a session with no Nickname yet opens to a blank field, and typing a
// name in (SetText, since simulating individual keystrokes isn't this
// codebase's existing InputField-testing convention) then Save calls
// SetName with it.
func TestSessionsTableRenameModalTypedNameAndSave(t *testing.T) {
	client := &fakeSessionsClient{sessions: []Session{{Pubkey: "abc123"}}}
	app := tui.NewApp()
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(80, 25)
	app.SetScreen(screen)
	st := NewSessionsTable(app, client)
	st.Init(t.Context())

	s, _ := st.sessionAt(1)
	st.openRenameInput(s)

	field, ok := app.GetFocus().(*tview.InputField)
	if !ok {
		t.Fatalf("GetFocus() = %T, want *tview.InputField (blank -- no Nickname to pre-fill)", app.GetFocus())
	}
	if got := field.GetText(); got != "" {
		t.Errorf("input pre-fill = %q, want empty (no Nickname set)", got)
	}
	field.SetText("New Name")

	setFocus := func(p tview.Primitive) { app.SetFocus(p) }
	field.InputHandler()(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), setFocus)
	button, ok := app.GetFocus().(*tview.Button)
	if !ok || button.GetLabel() != "Save" {
		t.Fatalf("focused button after Tab = %v, want \"Save\"", button)
	}
	button.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), setFocus)

	if client.setNamePubkey != "abc123" || client.setNameValue != "New Name" {
		t.Errorf("SetName called with (%q, %q), want (\"abc123\", \"New Name\")", client.setNamePubkey, client.setNameValue)
	}
}

// TestSessionsTableRenameModalCancelDoesNotCallSetName guards the
// opposite path: "Cancel" must dismiss without touching the app's name.
func TestSessionsTableRenameModalCancelDoesNotCallSetName(t *testing.T) {
	client := &fakeSessionsClient{sessions: []Session{{Pubkey: "abc123", Nickname: "Old Name"}}}
	app := tui.NewApp()
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(80, 25)
	app.SetScreen(screen)
	st := NewSessionsTable(app, client)
	st.Init(t.Context())

	s, _ := st.sessionAt(1)
	st.openRenameInput(s)

	field, ok := app.GetFocus().(*tview.InputField)
	if !ok {
		t.Fatalf("GetFocus() = %T, want *tview.InputField", app.GetFocus())
	}
	field.SetText("Should Not Be Saved")

	setFocus := func(p tview.Primitive) { app.SetFocus(p) }
	tab := tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
	field.InputHandler()(tab, setFocus) // -> "Save"
	button, ok := app.GetFocus().(*tview.Button)
	if !ok {
		t.Fatalf("GetFocus() after 1 Tab = %T, want *tview.Button", app.GetFocus())
	}
	button.InputHandler()(tab, setFocus) // -> "Cancel"
	button, ok = app.GetFocus().(*tview.Button)
	if !ok || button.GetLabel() != "Cancel" {
		t.Fatalf("focused button after 2 Tabs = %v, want \"Cancel\"", button)
	}
	button.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), setFocus)

	if client.setNamePubkey != "" {
		t.Errorf("SetName was called (%q, %q), want no call at all", client.setNamePubkey, client.setNameValue)
	}
}

// TestSessionsTableGrantsOverlayRendersRows guards openGrantsOverlay's own
// per-grant table contents: plain-language scope (grantScopeLabel),
// allow/deny status, and duration -- one row per grant, in order.
func TestSessionsTableGrantsOverlayRendersRows(t *testing.T) {
	kind1 := 1
	client := &fakeSessionsClient{sessions: []Session{{
		Pubkey: "abc123",
		Grants: []Grant{
			GrantForever(nip46.MethodPing, nil, time.Now()),
			DenyAlways(nip46.MethodSignEvent, time.Now()),
		},
	}}}
	client.sessions[0].Grants[1].Kind = &kind1 // DenyAlways itself never sets Kind -- pin it here so this exercises the sign_event kind label too
	app := tui.NewApp()
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(80, 25)
	app.SetScreen(screen)
	st := NewSessionsTable(app, client)
	st.Init(t.Context())

	s, ok := st.sessionAt(1)
	if !ok {
		t.Fatal("sessionAt(1) not found")
	}
	st.openGrantsOverlay(s)

	table, ok := app.GetFocus().(*tview.Table)
	if !ok {
		t.Fatalf("GetFocus() = %T, want *tview.Table", app.GetFocus())
	}
	if got, want := table.GetCell(1, 0).Text, grantScopeLabel(client.sessions[0].Grants[0]); got != want {
		t.Errorf("row 1 scope = %q, want %q", got, want)
	}
	if got := table.GetCell(1, 1).Text; got != "Allowed" {
		t.Errorf("row 1 status = %q, want Allowed", got)
	}
	if got, want := table.GetCell(2, 0).Text, grantScopeLabel(client.sessions[0].Grants[1]); got != want {
		t.Errorf("row 2 scope = %q, want %q", got, want)
	}
	if got := table.GetCell(2, 1).Text; got != "Blocked" {
		t.Errorf("row 2 status = %q, want Blocked", got)
	}
}

// TestSessionsTableGrantsOverlayRevokeReturnsToRefreshedOverlay guards the
// fix in openGrantsOverlay's own 'x' handler: App.ConfirmDelete's OK/
// Cancel both hardcode a return to "main" with no way back, which would
// have silently ejected the operator out of the Grants view after every
// single revoke. Confirming "Revoke" here must call RevokeGrant with the
// selected grant's own (method, kind) and land back in the *same* overlay,
// now showing one fewer row -- not on the Trusted Apps table underneath.
func TestSessionsTableGrantsOverlayRevokeReturnsToRefreshedOverlay(t *testing.T) {
	kind1 := 1
	client := &fakeSessionsClient{sessions: []Session{{
		Pubkey: "abc123",
		Grants: []Grant{
			GrantForever(nip46.MethodSignEvent, &kind1, time.Now()),
			GrantForever(nip46.MethodPing, nil, time.Now()),
		},
	}}}
	// A real running event loop, not just a bare tui.NewApp() (the rename-
	// modal tests' own convention) -- the fix under test reopens the
	// overlay via deferFollowUpDialog (a goroutine + QueueUpdateDraw),
	// which only ever actually runs while Application.Run's own loop is
	// draining it, same as TestPendingTableShowConnectingDoesNotClobber
	// RacingApproveDialog's own reasoning.
	app := tui.NewApp().Init()
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(80, 25)
	app.SetScreen(screen)
	go app.Run()
	defer app.Stop()

	st := NewSessionsTable(app, client)
	st.Init(t.Context())
	s, _ := st.sessionAt(1)

	// Every call that touches app/tview state goes through
	// QueueUpdateDraw, not directly from this goroutine -- Run's own loop
	// is concurrently redrawing (this table's own render() ticker
	// included), and driving InputHandlers straight from the test
	// goroutine races tview's internal state exactly the way
	// TestPendingTableShowConnectingDoesNotClobberRacingApproveDialog's
	// own QueueUpdateDraw calls avoid. Plain reads of app.GetFocus() are
	// fine directly, matching that same test's own polling loops.
	app.QueueUpdateDraw(func() { st.openGrantsOverlay(s) })
	waitFor(t, func() bool { _, ok := app.GetFocus().(*tview.Table); return ok })

	app.QueueUpdateDraw(func() {
		table := app.GetFocus().(*tview.Table)
		table.Select(1, 0) // the sign_event kind:1 row
		table.GetInputCapture()(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone))
	})
	waitFor(t, func() bool {
		b, ok := app.GetFocus().(*tview.Button)
		return ok && b.GetLabel() == "Revoke"
	})

	// Now on the "Revoke permission?" confirm dialog -- press "Revoke"
	// (already focused, the dialog's default first button).
	app.QueueUpdateDraw(func() {
		button := app.GetFocus().(*tview.Button)
		button.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(p tview.Primitive) { app.SetFocus(p) })
	})
	waitFor(t, func() bool { return client.revokedGrantPubkey != "" })

	if client.revokedGrantPubkey != "abc123" || client.revokedGrantMethod != nip46.MethodSignEvent {
		t.Fatalf("RevokeGrant called with (%q, %q, kind=%v), want (\"abc123\", %q, kind=1)",
			client.revokedGrantPubkey, client.revokedGrantMethod, client.revokedGrantKind, nip46.MethodSignEvent)
	}
	if client.revokedGrantKind == nil || *client.revokedGrantKind != 1 {
		t.Fatalf("RevokeGrant kind = %v, want *1", client.revokedGrantKind)
	}

	// The overlay must have reopened (not dropped back to "main") showing
	// the refreshed grant list -- one row (ping), not two.
	waitFor(t, func() bool {
		_, ok := app.GetFocus().(*tview.Table)
		return ok
	})
	reopened := app.GetFocus().(*tview.Table)
	if got, want := reopened.GetCell(1, 0).Text, grantScopeLabel(GrantForever(nip46.MethodPing, nil, time.Now())); got != want {
		t.Errorf("reopened overlay row 1 = %q, want %q (only the ping grant left)", got, want)
	}
	if got := reopened.GetRowCount(); got != 2 { // header + the one surviving grant
		t.Errorf("reopened overlay has %d rows, want 2 (header + one grant left)", got)
	}
}

// TestSessionsTableGrantsOverlayRevokeCancelReturnsUnchanged mirrors the
// above for "Cancel": no RevokeGrant call, and the overlay reopens with
// its original, unmodified grant list.
func TestSessionsTableGrantsOverlayRevokeCancelReturnsUnchanged(t *testing.T) {
	client := &fakeSessionsClient{sessions: []Session{{
		Pubkey: "abc123",
		Grants: []Grant{GrantForever(nip46.MethodPing, nil, time.Now())},
	}}}
	app := tui.NewApp().Init()
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(80, 25)
	app.SetScreen(screen)
	go app.Run()
	defer app.Stop()

	st := NewSessionsTable(app, client)
	st.Init(t.Context())
	s, _ := st.sessionAt(1)

	app.QueueUpdateDraw(func() { st.openGrantsOverlay(s) })
	waitFor(t, func() bool { _, ok := app.GetFocus().(*tview.Table); return ok })

	app.QueueUpdateDraw(func() {
		table := app.GetFocus().(*tview.Table)
		table.Select(1, 0)
		table.GetInputCapture()(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone))
	})
	waitFor(t, func() bool {
		b, ok := app.GetFocus().(*tview.Button)
		return ok && b.GetLabel() == "Revoke"
	})

	var cancelLabel string
	app.QueueUpdateDraw(func() {
		setFocus := func(p tview.Primitive) { app.SetFocus(p) }
		button := app.GetFocus().(*tview.Button)
		button.InputHandler()(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone), setFocus)
		button = app.GetFocus().(*tview.Button)
		cancelLabel = button.GetLabel()
		button.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), setFocus)
	})
	waitFor(t, func() bool { _, ok := app.GetFocus().(*tview.Table); return ok })

	if cancelLabel != "Cancel" {
		t.Fatalf("focused button after Tab = %q, want Cancel", cancelLabel)
	}
	if client.revokedGrantPubkey != "" {
		t.Errorf("RevokeGrant was called (%q), want no call at all", client.revokedGrantPubkey)
	}
	waitFor(t, func() bool {
		_, ok := app.GetFocus().(*tview.Table)
		return ok
	})
	reopened := app.GetFocus().(*tview.Table)
	if got, want := reopened.GetCell(1, 0).Text, grantScopeLabel(GrantForever(nip46.MethodPing, nil, time.Now())); got != want {
		t.Errorf("reopened overlay row 1 = %q, want %q (unchanged)", got, want)
	}
}

// TestSessionsTableGrantsOverlayEscapeCloses guards the table-level Esc
// handling openGrantsOverlay adds by hand (a bare tview.Table has no
// built-in Cancel-on-Esc the way Form does) -- the capture must swallow
// the key (return nil) rather than let it fall through unhandled.
func TestSessionsTableGrantsOverlayEscapeCloses(t *testing.T) {
	client := &fakeSessionsClient{sessions: []Session{{Pubkey: "abc123"}}}
	app := tui.NewApp()
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(80, 25)
	app.SetScreen(screen)
	st := NewSessionsTable(app, client)
	st.Init(t.Context())

	s, _ := st.sessionAt(1)
	st.openGrantsOverlay(s)
	table := app.GetFocus().(*tview.Table)
	capture := table.GetInputCapture()
	if capture == nil {
		t.Fatal("expected openGrantsOverlay to install an input capture on its table")
	}
	if got := capture(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)); got != nil {
		t.Error("expected Esc to be swallowed (capture returns nil)")
	}
}

// TestSessionsTableRendersNewColumns guards the actual table contents
// for the Name/Trusted Since/Last Request/Kinds columns added alongside
// App/Grants.
func TestSessionsTableRendersNewColumns(t *testing.T) {
	pairedAt := time.Now().Add(-3 * time.Hour)
	lastRequestAt := time.Now().Add(-10 * time.Minute)
	kind1 := 1
	client := &fakeSessionsClient{
		sessions: []Session{
			{
				Pubkey:   "abc123",
				AppName:  "Primal",
				AppURL:   "https://primal.net",
				Nickname: "My Wallet",
				PairedAt: pairedAt,
				Grants:   []Grant{{Method: nip46.MethodSignEvent, Verdict: Allow, Kind: &kind1}},
			},
			{Pubkey: "def456"}, // bunker:// pairing: no name/URL, no grants at all
		},
		history: []HistoryEntry{
			// Most-recent-first, matching Daemon.History's own order: the
			// older entry for "abc123" must lose to the newer one below,
			// not overwrite it.
			{ClientKey: "abc123", CreatedAt: lastRequestAt},
			{ClientKey: "abc123", CreatedAt: lastRequestAt.Add(-time.Hour)},
		},
	}
	app := tui.NewApp()
	st := NewSessionsTable(app, client)
	st.Init(t.Context())

	if got := st.GetCell(1, 0).Text; got != shortHex("abc123") {
		t.Errorf("row 1 App cell = %q, want %q (always the raw pubkey, even with a Nickname set)", got, shortHex("abc123"))
	}
	if got := st.GetCell(1, 1).Text; got != "My Wallet" {
		t.Errorf("row 1 Name cell = %q, want %q (Nickname wins over self-reported name)", got, "My Wallet")
	}
	if got := st.GetCell(1, 2).Text; got != "3h ago" {
		t.Errorf("row 1 Trusted Since cell = %q, want %q", got, "3h ago")
	}
	if got := st.GetCell(1, 3).Text; got != "10m ago" {
		t.Errorf("row 1 Last Request cell = %q, want %q", got, "10m ago")
	}
	if got := st.GetCell(1, 4).Text; got != "1" {
		t.Errorf("row 1 Kinds cell = %q, want \"1\"", got)
	}

	if got := st.GetCell(2, 0).Text; got != shortHex("def456") {
		t.Errorf("row 2 App cell = %q, want %q (always the raw pubkey)", got, shortHex("def456"))
	}
	if got := st.GetCell(2, 1).Text; got != "-" {
		t.Errorf("row 2 Name cell = %q, want \"-\" (no Nickname, no self-reported name for a bunker:// pairing)", got)
	}
	if got := st.GetCell(2, 2).Text; got != "-" {
		t.Errorf("row 2 Trusted Since cell = %q, want \"-\" (zero PairedAt)", got)
	}
	if got := st.GetCell(2, 3).Text; got != "-" {
		t.Errorf("row 2 Last Request cell = %q, want \"-\" (no history entries for this pubkey)", got)
	}
	if got := st.GetCell(2, 4).Text; got != "-" {
		t.Errorf("row 2 Kinds cell = %q, want \"-\" (no sign_event grant)", got)
	}
}

// TestSessionsTableSortedByLastRequest guards the panel's row order:
// most-recently-used apps first, an app with no request history at all
// last, regardless of ListSessions' own (arbitrary) order.
func TestSessionsTableSortedByLastRequest(t *testing.T) {
	now := time.Now()
	client := &fakeSessionsClient{
		sessions: []Session{
			// Deliberately not already in last-request order, and "never
			// requested" isn't last either -- Update must do the sorting,
			// not just happen to preserve ListSessions' order.
			{Pubkey: "stale", PairedAt: now.Add(-48 * time.Hour)},
			{Pubkey: "never-requested", PairedAt: now.Add(-72 * time.Hour)},
			{Pubkey: "fresh", PairedAt: now.Add(-24 * time.Hour)},
		},
		history: []HistoryEntry{
			{ClientKey: "fresh", CreatedAt: now.Add(-5 * time.Minute)},
			{ClientKey: "stale", CreatedAt: now.Add(-5 * time.Hour)},
		},
	}
	app := tui.NewApp()
	st := NewSessionsTable(app, client)
	st.Init(t.Context())

	wantOrder := []string{shortHex("fresh"), shortHex("stale"), shortHex("never-requested")}
	for i, want := range wantOrder {
		if got := st.GetCell(i+1, 0).Text; got != want {
			t.Errorf("row %d App cell = %q, want %q (order: fresh, stale, never-requested)", i+1, got, want)
		}
	}
}

func TestSummarizeApp(t *testing.T) {
	tests := []struct {
		name string
		s    Session
		want string
	}{
		{name: "no name at all", s: Session{}, want: "-"},
		{name: "name only", s: Session{AppName: "Damus"}, want: "Damus"},
		{name: "name and url", s: Session{AppName: "Primal", AppURL: "https://primal.net"}, want: "Primal (https://primal.net)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarizeApp(tt.s); got != tt.want {
				t.Errorf("summarizeApp() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{name: "seconds ago", ago: 30 * time.Second, want: "just now"},
		{name: "minutes ago", ago: 5 * time.Minute, want: "5m ago"},
		{name: "hours ago", ago: 3 * time.Hour, want: "3h ago"},
		{name: "days ago", ago: 50 * time.Hour, want: "2d ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatElapsed(time.Now().Add(-tt.ago)); got != tt.want {
				t.Errorf("formatElapsed() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("zero time -- no data, not \"just now\"", func(t *testing.T) {
		if got := formatElapsed(time.Time{}); got != "-" {
			t.Errorf("formatElapsed(zero) = %q, want \"-\"", got)
		}
	})
}

func TestSummarizeKinds(t *testing.T) {
	kind1, kind7, kind30023 := 1, 7, 30023
	tests := []struct {
		name   string
		grants []Grant
		want   string
	}{
		{name: "no grants", grants: nil, want: "-"},
		{name: "no sign_event grant", grants: []Grant{{Method: nip46.MethodPing}}, want: "-"},
		{name: "any-kind grant", grants: []Grant{{Method: nip46.MethodSignEvent, Kind: nil}}, want: "any"},
		{
			name: "exact kinds, sorted and deduplicated",
			grants: []Grant{
				{Method: nip46.MethodSignEvent, Kind: &kind30023},
				{Method: nip46.MethodSignEvent, Kind: &kind1},
				{Method: nip46.MethodSignEvent, Kind: &kind1},
				{Method: nip46.MethodSignEvent, Kind: &kind7},
			},
			want: "1, 7, 30023",
		},
		{
			name: "any-kind takes priority over exact kinds also present",
			grants: []Grant{
				{Method: nip46.MethodSignEvent, Kind: &kind1},
				{Method: nip46.MethodSignEvent, Kind: nil},
			},
			want: "any",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarizeKinds(tt.grants); got != tt.want {
				t.Errorf("summarizeKinds() = %q, want %q", got, tt.want)
			}
		})
	}
}

// fakeHistoryClient is a minimal, directly-configurable BunkerClient stub
// for exercising HistoryTable's own Update/onCountChange wiring.
type fakeHistoryClient struct {
	BunkerClient
	history  []HistoryEntry
	sessions []Session
}

func (c *fakeHistoryClient) History() ([]HistoryEntry, error) { return c.history, nil }
func (c *fakeHistoryClient) ListSessions() ([]Session, error) { return c.sessions, nil }

// TestHistoryTableInitFetchesSynchronously mirrors
// TestPendingTableInitFetchesSynchronously: HistoryTable shares
// PendingTable's own row (historyPendingRow) instead of sizing itself to
// its content, so unlike SessionsTable it has no onCountChange height
// hook to guard -- but its title count and rows must still populate from
// Init's own synchronous Update, not just eventually from the first
// render tick.
func TestHistoryTableInitFetchesSynchronously(t *testing.T) {
	client := &fakeHistoryClient{history: []HistoryEntry{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	app := tui.NewApp()
	ht := NewHistoryTable(app, client)

	ht.Init(t.Context())

	// updateTitle wraps the count in its own color tags (see
	// SessionsTable.updateTitle's identical shape), so the count itself
	// is bracketed as "]3[" rather than a bare "[3]".
	if !strings.Contains(ht.GetTitle(), "]3[") {
		t.Errorf("GetTitle() = %q, want it to show a count of 3 synchronously after Init", ht.GetTitle())
	}
}

// TestHistoryTableRendersMethodKindSplitAndStatus guards the actual
// table contents: Method and Kind must land in their own separate
// columns (see historyTableHeaders' own doc comment -- this mirrors
// PendingTable rather than folding them into one field), and Status must
// reflect historyStatus's own text.
func TestHistoryTableRendersMethodKindSplitAndStatus(t *testing.T) {
	client := &fakeHistoryClient{history: []HistoryEntry{
		{ID: "req-1", ClientKey: "abc123", Method: nip46.MethodSignEvent, Kind: 1, Verdict: Allow, Remembered: true},
		{ID: "req-2", ClientKey: "def456", Method: nip46.MethodPing, Verdict: Deny},
	}}
	app := tui.NewApp()
	ht := NewHistoryTable(app, client)
	ht.Init(t.Context())

	if got := ht.GetCell(1, 2).Text; got != nip46.MethodSignEvent {
		t.Errorf("row 1 Method cell = %q, want %q", got, nip46.MethodSignEvent)
	}
	if got := ht.GetCell(1, 3).Text; got != "1" {
		t.Errorf("row 1 Kind cell = %q, want \"1\" (its own column, not folded into Method)", got)
	}
	if got := ht.GetCell(1, 4).Text; got != "Approved (always)" {
		t.Errorf("row 1 Status cell = %q, want %q", got, "Approved (always)")
	}

	if got := ht.GetCell(2, 2).Text; got != nip46.MethodPing {
		t.Errorf("row 2 Method cell = %q, want %q", got, nip46.MethodPing)
	}
	if got := ht.GetCell(2, 3).Text; got != "-" {
		t.Errorf("row 2 Kind cell = %q, want \"-\" (not a sign_event)", got)
	}
	if got := ht.GetCell(2, 4).Text; got != "Rejected" {
		t.Errorf("row 2 Status cell = %q, want %q", got, "Rejected")
	}
}

// TestHistoryTableHistoryAtMapsRowToEntry guards the row-to-slice-index
// offset historyAt uses (row 0 is the header, same convention
// PendingTable.pendingAt uses) -- the plumbing showEventDetail's
// SetSelectedFunc wiring depends on to look up which row was picked.
func TestHistoryTableHistoryAtMapsRowToEntry(t *testing.T) {
	client := &fakeHistoryClient{history: []HistoryEntry{{ID: "req-1"}, {ID: "req-2"}}}
	app := tui.NewApp()
	ht := NewHistoryTable(app, client)
	ht.Init(t.Context())

	if _, ok := ht.historyAt(0); ok {
		t.Error("historyAt(0) (the header row) = ok, want not found")
	}
	if h, ok := ht.historyAt(1); !ok || h.ID != "req-1" {
		t.Errorf("historyAt(1) = %+v, %v, want req-1/true", h, ok)
	}
	if h, ok := ht.historyAt(2); !ok || h.ID != "req-2" {
		t.Errorf("historyAt(2) = %+v, %v, want req-2/true", h, ok)
	}
	if _, ok := ht.historyAt(3); ok {
		t.Error("historyAt(3) (past the end) = ok, want not found")
	}
}

// TestHistoryTableShowEventDetailOpensOverlayForSignedEvent guards
// showEventDetail's happy path: an approved entry with a signed Event
// must open an overlay whose default-focused button is "Close", not
// "Copy" -- a stray Enter (e.g. from muscle memory dismissing an
// unrelated dialog) should never silently copy something to the
// clipboard -- reusing tui.ColorizeEventJSON the same way
// client/tui.ShowEvent does (see that function's own doc comment) rather
// than a duplicated colorizer.
func TestHistoryTableShowEventDetailOpensOverlayForSignedEvent(t *testing.T) {
	event := &nip01.Event{ID: "abcd", PubKey: "ef01", Kind: 1, Content: "hello", CreatedAt: 1, Sig: "deadbeef"}
	app := tui.NewApp()
	ht := NewHistoryTable(app, &fakeHistoryClient{})
	ht.Init(t.Context())

	ht.showEventDetail(HistoryEntry{ID: "req-1", Method: nip46.MethodSignEvent, Verdict: Allow, Event: event})

	button, ok := app.GetFocus().(*tview.Button)
	if !ok {
		t.Fatalf("GetFocus() = %T, want *tview.Button (the overlay's first button)", app.GetFocus())
	}
	if got := button.GetLabel(); got != "Close" {
		t.Fatalf("focused button label = %q, want %q", got, "Close")
	}
}

// TestHistoryTableShowEventDetailOpensOverlayForUnsignedExpiredEvent
// guards the actual point of HistoryEntry.Event now being populated
// unconditionally (recordHistory, daemon.go): an Expired sign_event --
// never approved, never signed -- must still open the same viewer,
// showing what was originally being asked for even though nothing was
// ever signed, rather than the old no-op (Event used to stay nil for
// anything but an approved sign_event).
func TestHistoryTableShowEventDetailOpensOverlayForUnsignedExpiredEvent(t *testing.T) {
	unsigned := &nip01.Event{PubKey: "ef01", Kind: 1, Content: "never got decided in time"} // Sig == "" -- never signed
	app := tui.NewApp()
	ht := NewHistoryTable(app, &fakeHistoryClient{})
	ht.Init(t.Context())

	ht.showEventDetail(HistoryEntry{ID: "req-1", Method: nip46.MethodSignEvent, Expired: true, Verdict: Deny, Event: unsigned})

	button, ok := app.GetFocus().(*tview.Button)
	if !ok {
		t.Fatalf("GetFocus() = %T, want *tview.Button (the overlay's first button)", app.GetFocus())
	}
	if got := button.GetLabel(); got != "Close" {
		t.Fatalf("focused button label = %q, want %q", got, "Close")
	}
}

// TestHistoryTableShowEventDetailCopyButtonCopiesSignedJSON clicks the
// overlay's "Copy" button and confirms it actually invokes copyToClipboard
// with the event's signed JSON -- not just that a button labeled "Copy"
// exists. writeOSC52 writes directly to os.Stdout (see its own doc
// comment), so captureStdout (clipboard_test.go) is the only way to
// observe what was actually copied without a real terminal.
func TestHistoryTableShowEventDetailCopyButtonCopiesSignedJSON(t *testing.T) {
	event := &nip01.Event{ID: "abcd", PubKey: "ef01", Kind: 1, Content: "hello", CreatedAt: 1, Sig: "deadbeef"}
	app := tui.NewApp()
	ht := NewHistoryTable(app, &fakeHistoryClient{})
	ht.Init(t.Context())

	out := captureStdout(t, func() {
		ht.showEventDetail(HistoryEntry{ID: "req-1", Method: nip46.MethodSignEvent, Verdict: Allow, Event: event})

		setFocus := func(p tview.Primitive) { app.SetFocus(p) }
		button, ok := app.GetFocus().(*tview.Button)
		if !ok {
			t.Fatalf("GetFocus() = %T, want *tview.Button", app.GetFocus())
		}
		// "Close" is the default focus (form.SetFocus(1)) -- Backtab once
		// to reach "Copy" (added first, index 0) before activating it.
		button.InputHandler()(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone), setFocus)
		button, ok = app.GetFocus().(*tview.Button)
		if !ok || button.GetLabel() != "Copy" {
			t.Fatalf("GetFocus() after Backtab = %v, want the \"Copy\" button", app.GetFocus())
		}
		button.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), setFocus)
	})

	wantJSON, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	wantPayload := base64.StdEncoding.EncodeToString(wantJSON)
	if !strings.Contains(out, wantPayload) {
		t.Errorf("stdout after clicking Copy = %q, want it to contain the OSC52-encoded signed event JSON (%q)", out, wantPayload)
	}
}

// TestHistoryTableShowEventDetailCopyButtonCopiesUnsignedJSON is the
// rejected/expired counterpart: Copy must still work, copying the
// never-signed event exactly as it was recorded (Sig == "").
func TestHistoryTableShowEventDetailCopyButtonCopiesUnsignedJSON(t *testing.T) {
	unsigned := &nip01.Event{PubKey: "ef01", Kind: 1, Content: "rejected before signing"}
	app := tui.NewApp()
	ht := NewHistoryTable(app, &fakeHistoryClient{})
	ht.Init(t.Context())

	out := captureStdout(t, func() {
		ht.showEventDetail(HistoryEntry{ID: "req-1", Method: nip46.MethodSignEvent, Verdict: Deny, Event: unsigned})

		setFocus := func(p tview.Primitive) { app.SetFocus(p) }
		button, ok := app.GetFocus().(*tview.Button)
		if !ok {
			t.Fatalf("GetFocus() = %T, want *tview.Button", app.GetFocus())
		}
		// "Close" is the default focus (form.SetFocus(1)) -- Backtab once
		// to reach "Copy" (added first, index 0) before activating it.
		button.InputHandler()(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone), setFocus)
		button, ok = app.GetFocus().(*tview.Button)
		if !ok || button.GetLabel() != "Copy" {
			t.Fatalf("GetFocus() after Backtab = %v, want the \"Copy\" button", app.GetFocus())
		}
		button.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), setFocus)
	})

	wantJSON, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	wantPayload := base64.StdEncoding.EncodeToString(wantJSON)
	if !strings.Contains(out, wantPayload) {
		t.Errorf("stdout after clicking Copy = %q, want it to contain the OSC52-encoded unsigned event JSON (%q)", out, wantPayload)
	}
}

// TestHistoryTableShowEventDetailNoopWithoutEvent guards the defensive
// nil check: an entry for a method that never has an event at all (every
// method but sign_event) must not open anything or panic.
func TestHistoryTableShowEventDetailNoopWithoutEvent(t *testing.T) {
	app := tui.NewApp()
	ht := NewHistoryTable(app, &fakeHistoryClient{})
	ht.Init(t.Context())

	ht.showEventDetail(HistoryEntry{ID: "req-1", Method: nip46.MethodPing, Verdict: Allow})

	if app.GetFocus() != nil {
		t.Errorf("GetFocus() = %v after showEventDetail with no Event, want nil (nothing should open)", app.GetFocus())
	}
}

func TestAnyRelayConnected(t *testing.T) {
	tests := []struct {
		name     string
		statuses []RelayStatus
		want     bool
	}{
		{name: "none configured", statuses: nil, want: false},
		{name: "all disconnected", statuses: []RelayStatus{{URL: "a", Connected: false}, {URL: "b", Connected: false}}, want: false},
		{name: "one of several connected", statuses: []RelayStatus{{URL: "a", Connected: false}, {URL: "b", Connected: true}}, want: true},
		{name: "all connected", statuses: []RelayStatus{{URL: "a", Connected: true}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := anyRelayConnected(tt.statuses); got != tt.want {
				t.Errorf("anyRelayConnected(%+v) = %v, want %v", tt.statuses, got, tt.want)
			}
		})
	}
}

func TestAnyRelayConnecting(t *testing.T) {
	tests := []struct {
		name     string
		statuses []RelayStatus
		want     bool
	}{
		{name: "none configured", statuses: nil, want: false},
		{name: "all confirmed down -- no first attempt still in flight", statuses: []RelayStatus{{URL: "a"}, {URL: "b"}}, want: false},
		{name: "one still trying its first connection", statuses: []RelayStatus{{URL: "a"}, {URL: "b", Connecting: true}}, want: true},
		{name: "connected relays are never also Connecting", statuses: []RelayStatus{{URL: "a", Connected: true}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := anyRelayConnecting(tt.statuses); got != tt.want {
				t.Errorf("anyRelayConnecting(%+v) = %v, want %v", tt.statuses, got, tt.want)
			}
		})
	}
}

// fakeStatusClient is a minimal, directly-configurable BunkerClient stub
// for exercising AlertBar/IdentityBar's own Update wiring, and (via its
// no-op ListPending/ListSessions/Logs) safe to hand to a full
// NewBunkerBoard without every other panel's own construction-time Update
// panicking on the embedded nil BunkerClient. A non-nil err simulates the
// daemon connection being gone (see IdentityBar.Update) -- it only ever
// affects Status(): the other methods deliberately stay error-free so a
// daemon-lost test exercises just IdentityBar's own path, not every
// panel's independent error handling at once.
type fakeStatusClient struct {
	BunkerClient
	status  StatusInfo
	err     error
	stopped bool
	history []HistoryEntry
}

func (c *fakeStatusClient) Status() (StatusInfo, error)      { return c.status, c.err }
func (c *fakeStatusClient) ListPending() ([]Pending, error)  { return nil, nil }
func (c *fakeStatusClient) ListSessions() ([]Session, error) { return nil, nil }
func (c *fakeStatusClient) History() ([]HistoryEntry, error) { return c.history, nil }
func (c *fakeStatusClient) Logs() (LogSnapshot, error)       { return LogSnapshot{}, nil }
func (c *fakeStatusClient) Stop() error                      { c.stopped = true; return nil }

// TestAlertBarTracksRelayHealth guards both the toggle logic (onAlert
// fires active=true only when nothing's connected) and that the visible
// text actually changes with it -- a silent height-only bug (right
// onAlert, stale/blank text) would be just as misleading as showing the
// alert at the wrong time.
func TestAlertBarTracksRelayHealth(t *testing.T) {
	client := &fakeStatusClient{status: StatusInfo{RelayStatuses: []RelayStatus{{URL: "wss://relay.example", Connected: false}}}}
	app := tui.NewApp()
	bar := NewAlertBar(app, client)

	var gotActive bool
	calls := 0
	bar.onAlert = func(active bool) { gotActive = active; calls++ }

	bar.Init(t.Context())

	if calls == 0 {
		t.Fatal("expected onAlert to fire from Init's synchronous Update")
	}
	if !gotActive {
		t.Error("expected onAlert(true) with no relay connected")
	}
	if !strings.Contains(bar.GetText(true), "No relay connected") {
		t.Errorf("GetText() = %q, want it to warn about no relay connected", bar.GetText(true))
	}

	// Recovering (a relay reconnects) must clear both the callback state
	// and the visible text -- not just stop updating it, which would leave
	// a stale warning on screen after the problem is already gone.
	client.status = StatusInfo{RelayStatuses: []RelayStatus{{URL: "wss://relay.example", Connected: true}}}
	bar.Update()

	if gotActive {
		t.Error("expected onAlert(false) once a relay reconnects")
	}
	if got := bar.GetText(true); strings.TrimSpace(got) != "" {
		t.Errorf("GetText() = %q, want it cleared once healthy", got)
	}
}

// TestAlertBarSuppressedDuringStartupRace guards the fix for a false
// alarm on a perfectly healthy startup: AlertBar.Init runs its own first
// Update synchronously, at the same instant Daemon.Run has only just
// spawned its runRelay goroutines -- before any of them have dialed
// anything, every relay reads Connected=false, which used to be
// indistinguishable from a real, confirmed failure. Connecting=true
// (Daemon.RelayStatuses' own signal for "first attempt not resolved yet")
// must keep the alert off during that window.
func TestAlertBarSuppressedDuringStartupRace(t *testing.T) {
	client := &fakeStatusClient{status: StatusInfo{RelayStatuses: []RelayStatus{{URL: "wss://relay.example", Connecting: true}}}}
	app := tui.NewApp()
	bar := NewAlertBar(app, client)

	var gotActive bool
	calls := 0
	bar.onAlert = func(active bool) { gotActive = active; calls++ }

	bar.Init(t.Context())

	if calls == 0 {
		t.Fatal("expected onAlert to fire from Init's synchronous Update")
	}
	if gotActive {
		t.Error("expected onAlert(false) while every relay is still trying its first connection, not a confirmed failure")
	}
	if got := bar.GetText(true); strings.TrimSpace(got) != "" {
		t.Errorf("GetText() = %q, want no warning shown during the startup race", got)
	}

	// Once the first attempt actually resolves as a failure (Connecting
	// clears, Connected stays false), the alert must activate normally --
	// this isn't a blanket suppression, only a startup grace window.
	client.status = StatusInfo{RelayStatuses: []RelayStatus{{URL: "wss://relay.example"}}}
	bar.Update()

	if !gotActive {
		t.Error("expected onAlert(true) once the first attempt has actually failed (Connecting=false, Connected=false)")
	}
}

// TestIdentityBarOnDaemonLostFiresOnceOnStatusError guards the fix for a
// TUI that silently freezes -- with no error, no exit, nothing on screen
// explaining why -- when its daemon connection dies underneath it (e.g.
// `ncli bunker stop` from another terminal). onDaemonLost must fire
// exactly once even though every following Update also errors identically
// forever (see IdentityBar.Update's own doc comment on why that's not a
// retryable hiccup for this client).
func TestIdentityBarOnDaemonLostFiresOnceOnStatusError(t *testing.T) {
	client := &fakeStatusClient{err: errors.New("use of closed network connection")}
	app := tui.NewApp()
	bar := NewIdentityBar(app, client)

	calls := 0
	bar.onDaemonLost = func() { calls++ }

	bar.Init(t.Context())
	bar.Update()
	bar.Update()

	if calls != 1 {
		t.Errorf("onDaemonLost fired %d times, want exactly 1", calls)
	}
}

// TestIdentityBarOnDaemonLostNotCalledWhenHealthy is
// TestIdentityBarOnDaemonLostFiresOnceOnStatusError's inverse: a healthy
// client must never trigger the daemon-lost exit path.
func TestIdentityBarOnDaemonLostNotCalledWhenHealthy(t *testing.T) {
	client := &fakeStatusClient{status: StatusInfo{IdentityPub: testNpubPub}}
	app := tui.NewApp()
	bar := NewIdentityBar(app, client)

	calls := 0
	bar.onDaemonLost = func() { calls++ }

	bar.Init(t.Context())

	if calls != 0 {
		t.Errorf("onDaemonLost fired %d times on a healthy client, want 0", calls)
	}
}

// TestBunkerBoardDaemonLost drives NewBunkerBoard end to end against a
// client whose Status() always errors, confirming the whole wire-up --
// IdentityBar's callback through to BunkerBoard.daemonLost -- actually
// closes the channel DaemonLost() reports on, not just that the callback
// itself gets invoked in isolation.
func TestBunkerBoardDaemonLost(t *testing.T) {
	client := &fakeStatusClient{err: errors.New("use of closed network connection")}
	app := tui.NewApp()
	flowLogger := &tui.FlowLogger{}

	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)

	if !board.DaemonLost() {
		t.Fatal("expected DaemonLost() to report true once IdentityBar's synchronous Init-time Update already saw the client failing")
	}
}

// TestBunkerBoardDaemonLost_Healthy is TestBunkerBoardDaemonLost's
// inverse: a healthy client must never report the daemon as lost.
func TestBunkerBoardDaemonLost_Healthy(t *testing.T) {
	client := &fakeStatusClient{status: StatusInfo{IdentityPub: testNpubPub}}
	app := tui.NewApp()
	flowLogger := &tui.FlowLogger{}

	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)

	if board.DaemonLost() {
		t.Fatal("expected DaemonLost() to report false for a healthy client")
	}
}

// TestBunkerBoardBackgroundKeyIsGlobalAndDropsQ guards two things at
// once: 'b' (background/stop) is bound on the board's own outer Flex, not
// scoped to whichever panel happens to be focused, and 'q' -- a
// second key that used to do the exact same thing -- was deliberately
// dropped rather than kept as a redundant alias.
func TestBunkerBoardBackgroundKeyIsGlobalAndDropsQ(t *testing.T) {
	client := &fakeStatusClient{status: StatusInfo{IdentityPub: testNpubPub}}
	app := tui.NewApp()
	flowLogger := &tui.FlowLogger{}

	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)

	capture := board.GetInputCapture()
	if capture == nil {
		t.Fatal("expected NewBunkerBoard to install an input capture on the board's own Flex")
	}

	if got := capture(tcell.NewEventKey(tcell.KeyRune, 'b', tcell.ModNone)); got != nil {
		t.Error("expected 'b' to be swallowed (capture returns nil) regardless of which panel is focused")
	}
	if got := capture(tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone)); got == nil {
		t.Error("expected 'q' to fall through unswallowed -- it must no longer trigger the background/stop dialog")
	}
}

// TestBunkerBoardConnectKeyIsGlobal guards 'c' moving from PendingTable's
// own SetInputCapture to the board's -- pairing a new app must be
// reachable no matter which panel is focused, not just Pending (where it
// used to be scoped). Checks more than just "the keystroke was swallowed"
// (both a Modal-backed ShowDialog and a Form-backed ShowOverlay
// ultimately delegate focus to a *tview.Button either way, so that alone
// wouldn't prove which dialog opened -- see feedback_tview_overlay_testing
// memory): the focused button's own label confirms it's really
// openConnectDialog's "Connect a new app" dialog, not some other overlay.
func TestBunkerBoardConnectKeyIsGlobal(t *testing.T) {
	client := &fakeStatusClient{status: StatusInfo{IdentityPub: testNpubPub}}
	app := tui.NewApp()
	flowLogger := &tui.FlowLogger{}

	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)

	capture := board.GetInputCapture()
	if capture == nil {
		t.Fatal("expected NewBunkerBoard to install an input capture on the board's own Flex")
	}

	if got := capture(tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModNone)); got != nil {
		t.Fatal("expected 'c' to be swallowed (capture returns nil) regardless of which panel is focused")
	}

	button, ok := app.GetFocus().(*tview.Button)
	if !ok {
		t.Fatalf("GetFocus() = %T, want *tview.Button (the connect dialog's first button)", app.GetFocus())
	}
	if got := button.GetLabel(); got != "Show bunker:// URI" {
		t.Errorf("focused button label = %q, want %q -- openConnectDialog should have opened", got, "Show bunker:// URI")
	}
}

// TestBunkerBoardHandleCtrlC guards the fix for Ctrl+C silently leaving
// the daemon running in the background: unlike 'b' (which opens a
// confirmation dialog), Ctrl+C must stop the daemon immediately and
// report itself as handled, so client/tui's own Ctrl-C-closes-the-
// application default never also fires on the same keystroke (see
// client/tui.CtrlCHandler's own doc comment).
func TestBunkerBoardHandleCtrlC(t *testing.T) {
	client := &fakeStatusClient{status: StatusInfo{IdentityPub: testNpubPub}}
	app := tui.NewApp()
	flowLogger := &tui.FlowLogger{}

	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)

	if got := board.HandleCtrlC(); !got {
		t.Error("HandleCtrlC() = false, want true (must claim the keystroke)")
	}
	if !client.stopped {
		t.Error("expected HandleCtrlC to call client.Stop(), the daemon was left running")
	}
}

// TestBunkerBoardFooterHintsAreContextual guards the focus-aware footer:
// each panel's own hints should mention only the keys that actually do
// something while that panel is focused, plus the three board-wide
// exceptions (Switch Panel, Background, Connect) in every variant --
// Connect used to be Pending-only, moved here alongside Background once
// 'c' itself became a board-wide key (see NewBunkerBoard).
func TestBunkerBoardFooterHintsAreContextual(t *testing.T) {
	event := &nip01.Event{ID: "abcd", PubKey: "ef01", Kind: 1, Content: "hi", CreatedAt: 1, Sig: "deadbeef"}
	client := &fakeStatusClient{
		status:  StatusInfo{IdentityPub: testNpubPub},
		history: []HistoryEntry{{ID: "req-1", Method: nip46.MethodSignEvent, Kind: 1, Verdict: Allow, Event: event}},
	}
	app := tui.NewApp()
	flowLogger := &tui.FlowLogger{}

	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)
	// The <Enter> hint is conditional on the *selected* row having an
	// event to show (see FooterHints' own b.history case) -- select the
	// one sign_event row Init's synchronous Update already populated.
	board.history.Select(1, 0)

	cases := []struct {
		name         string
		focused      tview.Primitive
		wantContain  []string
		wantAbsent   []string
		firstDynamic string // the earliest panel-specific hint expected to appear -- must come after all three global ones
	}{
		{
			name:         "sessions",
			focused:      board.sessions,
			wantContain:  []string{"Revoke", "Background", "Connect", "Switch Panel"},
			wantAbsent:   []string{"<a>/<r>/<Enter>", "Toggle Wrap"},
			firstDynamic: "Revoke",
		},
		{
			name:         "logger",
			focused:      board.logger.FocusTarget(),
			wantContain:  []string{"Toggle Wrap", "Toggle AutoScroll", "Background", "Connect", "Switch Panel"},
			wantAbsent:   []string{"Revoke", "<a>/<r>/<Enter>"},
			firstDynamic: "Toggle Wrap",
		},
		{
			// <a>/<r>/<Enter> merged into one hint (see FooterHints' own
			// doc comment) -- a single "Approve/Reject/More" label, not
			// two separate footer entries.
			name:         "pending",
			focused:      board.pending.FocusTarget(),
			wantContain:  []string{"<a>/<r>/<Enter>", "Approve/Reject/More", "Toggle Auto-Prompt", "Connect", "Background", "Switch Panel"},
			wantAbsent:   []string{"Revoke", "Toggle Wrap"},
			firstDynamic: "Approve/Reject/More",
		},
		{
			// Read-only for deciding anything (see HistoryTable's own doc
			// comment), but Enter still opens a sign_event row's event
			// JSON (showEventDetail) -- a real panel-specific hint, not
			// just the three global ones.
			name:         "history",
			focused:      board.history,
			wantContain:  []string{"Background", "Connect", "Switch Panel", "<Enter>", "View Event"},
			wantAbsent:   []string{"Revoke", "<a>/<r>/<Enter>", "Toggle Wrap"},
			firstDynamic: "View Event",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			hints := board.FooterHints(tt.focused)
			for _, want := range tt.wantContain {
				if !strings.Contains(hints, want) {
					t.Errorf("FooterHints(%s) = %q, want it to contain %q", tt.name, hints, want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(hints, absent) {
					t.Errorf("FooterHints(%s) = %q, want it to NOT contain %q", tt.name, hints, absent)
				}
			}

			if tt.firstDynamic == "" {
				return
			}

			// Global hints (Switch Panel, Background, Connect) must always
			// lead, in that fixed order, before any panel-specific one --
			// regardless of which panel is focused.
			switchIdx := strings.Index(hints, "Switch Panel")
			backgroundIdx := strings.Index(hints, "Background")
			connectIdx := strings.Index(hints, "Connect")
			dynamicIdx := strings.Index(hints, tt.firstDynamic)
			if switchIdx == -1 || backgroundIdx == -1 || connectIdx == -1 || dynamicIdx == -1 {
				t.Fatalf("FooterHints(%s) = %q, missing one of Switch Panel/Background/Connect/%s entirely", tt.name, hints, tt.firstDynamic)
			}
			if !(switchIdx < backgroundIdx && backgroundIdx < connectIdx && connectIdx < dynamicIdx) {
				t.Errorf("FooterHints(%s) = %q, want Switch Panel then Background then Connect then %s, got indices %d/%d/%d/%d",
					tt.name, hints, tt.firstDynamic, switchIdx, backgroundIdx, connectIdx, dynamicIdx)
			}
		})
	}
}

// TestBunkerBoardHistoryFooterHintFollowsSelectedRow guards the actual
// point of making the hint row-conditional rather than just panel-wide:
// a footer promising <Enter> View Event on a row where Enter would
// silently do nothing (every non-sign_event method) is worse than no
// hint at all. Also exercises HistoryTable's own SetSelectionChangedFunc
// -- Select() calls the registered callback synchronously (see tview's
// own Table.Select), so this indirectly proves that wiring doesn't panic
// even with no footer yet attached (app.Load was never called here).
func TestBunkerBoardHistoryFooterHintFollowsSelectedRow(t *testing.T) {
	event := &nip01.Event{ID: "abcd", PubKey: "ef01", Kind: 1, Content: "hi", CreatedAt: 1, Sig: "deadbeef"}
	client := &fakeStatusClient{
		status: StatusInfo{IdentityPub: testNpubPub},
		history: []HistoryEntry{
			{ID: "req-2", Method: nip46.MethodSignEvent, Kind: 1, Verdict: Allow, Event: event}, // newest -- row 1 (History() is most-recent-first)
			{ID: "req-1", Method: nip46.MethodPing, Verdict: Allow},                             // oldest -- row 2
		},
	}
	app := tui.NewApp()
	flowLogger := &tui.FlowLogger{}

	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)

	board.history.Select(1, 0)
	if hints := board.FooterHints(board.history); !strings.Contains(hints, "View Event") {
		t.Errorf("FooterHints on the sign_event row = %q, want it to contain %q", hints, "View Event")
	}

	board.history.Select(2, 0)
	if hints := board.FooterHints(board.history); strings.Contains(hints, "View Event") {
		t.Errorf("FooterHints on the ping row = %q, want it to NOT contain %q", hints, "View Event")
	}
}

// fullBoardTestClient satisfies every BunkerClient method NewBunkerBoard's
// panels call, for TestBunkerBoardOverlayDoesNotBleedThroughToPanelBorders
// below -- unlike the narrower single-purpose fakes elsewhere in this
// file, that test needs the *whole* board running (every panel's own
// render() ticker actively redrawing concurrently), not just one panel in
// isolation. pending is set via setPending (mutex-protected) rather than
// the struct literal so a test can start the board with nothing pending
// and inject a request only *after* Load/Run are running -- see that
// test's own doc comment on why the timing matters here.
type fullBoardTestClient struct {
	BunkerClient
	status StatusInfo

	mu      sync.Mutex
	pending []Pending

	// connectGate, if non-nil, makes Connect block until it's closed --
	// standing in for the real up-to-a-minute network wait so a test can
	// deterministically race something else (e.g. an unrelated approval
	// dialog) against Connect's own completion. nil (the common case) means
	// Connect returns immediately, as before this field existed.
	connectGate chan struct{}

	// statusDelay, if non-zero, makes Status() block for that long before
	// returning -- standing in for the real IPC round-trip that
	// IdentityBar.Init and AlertBar.Init each make synchronously as part
	// of NewBunkerBoard's own construction. Used by
	// TestBunkerTUIStartupShowsSplashWhileBoardLoads to make that
	// construction slow enough to observe.
	statusDelay time.Duration
}

func (c *fullBoardTestClient) Status() (StatusInfo, error) {
	if c.statusDelay > 0 {
		time.Sleep(c.statusDelay)
	}
	return c.status, nil
}
func (c *fullBoardTestClient) ListPending() ([]Pending, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Pending(nil), c.pending...), nil
}
func (c *fullBoardTestClient) setPending(p []Pending) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = p
}
func (c *fullBoardTestClient) ListSessions() ([]Session, error) { return nil, nil }
func (c *fullBoardTestClient) History() ([]HistoryEntry, error) { return nil, nil }
func (c *fullBoardTestClient) Logs() (LogSnapshot, error)       { return LogSnapshot{}, nil }
func (c *fullBoardTestClient) Connect(uri string, spec *GrantSpec) (string, error) {
	if c.connectGate != nil {
		<-c.connectGate
	}
	return "bunker://" + testNpubPub + "?relay=wss://relay.example&secret=deadbeef", nil
}

// screenContains reports whether substr appears, intact, within any single
// row of screen's current contents -- checked one row at a time (not the
// whole buffer joined together) so a phrase that happens to wrap across
// rows can't produce a false match from two unrelated rows getting
// concatenated.
func screenContains(screen tcell.SimulationScreen, substr string) bool {
	cells, width, height := screen.GetContents()
	for y := 0; y < height; y++ {
		var row strings.Builder
		for x := 0; x < width; x++ {
			idx := y*width + x
			if idx < 0 || idx >= len(cells) || len(cells[idx].Runes) == 0 {
				row.WriteRune(' ')
				continue
			}
			row.WriteRune(cells[idx].Runes[0])
		}
		if strings.Contains(row.String(), substr) {
			return true
		}
	}
	return false
}

// TestBunkerTUIStartupShowsSplashWhileBoardLoads is a regression guard for
// runTUI's own board-loading order (cli/bunker/command.go): NewBunkerBoard
// blocks on real IPC (IdentityBar.Init and AlertBar.Init both call
// Status() synchronously) before it has anything to show, and app.Load's
// own splashOnce sleep adds a further fixed 1s floor on top of that. If
// that work ran to completion *before* app.Run() ever started (as it used
// to, when runTUI built the board and called app.Load synchronously ahead
// of app.Run()), the splashscreen page Init() already added would get
// swapped out for "main" before the very first frame was ever drawn --
// tview only starts pumping draws once Run() begins, so an operator would
// never actually see the splash, just an instant jump to (an incomplete)
// board. This mirrors runTUI's real fix (board construction + Load moved
// into a goroutine, started concurrently with Run(), not before it)
// against a fullBoardTestClient whose Status() is slowed down to stand in
// for that real IPC latency, and checks tui.WELCOME_MESSAGE (the splash's
// one bit of text the ordinary board header doesn't also share -- both
// show the same logo art, via client/tui/header.go's own Header, so that
// alone can't tell them apart) is genuinely visible on screen while the
// board is still loading, then gone once it's done.
func TestBunkerTUIStartupShowsSplashWhileBoardLoads(t *testing.T) {
	client := &fullBoardTestClient{
		status:      StatusInfo{IdentityPub: testNpubPub},
		statusDelay: 300 * time.Millisecond,
	}

	app := tui.NewApp().Init()
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(80, 25)
	app.SetScreen(screen)

	go app.Run()
	defer app.Stop()

	// Mirrors runTUI's own ordering exactly: board construction happens in
	// the background, started concurrently with Run() above, not before
	// it; the actual app.Load that swaps the visible page is dispatched
	// through QueueUpdateDraw rather than called directly from this
	// goroutine, since Run() is by now the only goroutine allowed to
	// touch app's pages (see runTUI's own doc comment on this exact
	// point).
	go func() {
		flowLogger := &tui.FlowLogger{}
		board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)
		app.QueueUpdateDraw(func() { app.Load(board) })
	}()

	// screenHas reads screen's contents on the Application's own
	// event-loop goroutine (via QueueUpdate) instead of straight from this
	// one -- Run()'s own draw() calls write to the same SimulationScreen
	// concurrently, and, unlike app's own pages, tcell.SimulationScreen
	// has no locking of its own that would make a direct cross-goroutine
	// read safe. "ownership" (a single word, so immune to wherever
	// SplashScreen's own word-wrap happens to break the fuller message)
	// stands in for tui.WELCOME_MESSAGE itself.
	screenHas := func(substr string) bool {
		var found bool
		app.QueueUpdate(func() { found = screenContains(screen, substr) })
		return found
	}

	// Well before NewBunkerBoard's own artificially-slowed Status() calls
	// (300ms each, called at least twice -- once from IdentityBar.Init,
	// once from AlertBar.Init) can have returned.
	time.Sleep(100 * time.Millisecond)
	if !screenHas("ownership") {
		t.Fatal("splashscreen's welcome message isn't visible at t=100ms -- want it still showing while the board is still loading")
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && !screenHas("Signing as") {
		time.Sleep(10 * time.Millisecond)
	}
	if !screenHas("Signing as") {
		t.Fatal("board content (IdentityBar's \"Signing as:\") never appeared on screen within the deadline")
	}
	if screenHas("ownership") {
		t.Error("splashscreen's welcome message is still visible after the board finished loading -- want it fully swapped out for the board")
	}
}

// TestBunkerBoardOverlayDoesNotBleedThroughToPanelBorders is a regression
// guard for a real rendering bug found and fixed this session:
// centerOverlay's old nil Flex spacer items were never drawn at all
// (tview.Flex skips a nil item outright), leaving each margin's fill
// entirely up to the surrounding Flex's own one-time background fill from
// its own Draw call. That's fine for a single, isolated Draw -- but this
// board redraws constantly (every panel runs its own render() ticker
// independently, all calling QueueUpdateDraw), and under that real
// concurrent redraw traffic the margin kept showing stale content bled
// through from whatever panel sits underneath (its border, its
// live-updating text) instead of staying blank. Reproducing this needed a
// real Application.Run() loop -- a single synchronous Draw call never
// showed the bug, only repeated concurrent redraws did -- so this uses a
// real (simulated) screen and actually runs the app, the same way this
// file's other real-Run()-loop tests do (see
// TestBunkerBoardConnectKeyIsGlobal and friends). The fix (overlaySpacer,
// a real Box instead of nil) is what this test guards -- see its own doc
// comment for the mechanism.
//
// The board starts with nothing pending and the sign_event request is
// injected only after Load/Run are already running, deliberately -- kept
// that way (rather than folded together) even after the *separate*
// pre-load focus-stealing bug this uncovered was also fixed (see
// TestBunkerBoardAutoPromptSurvivesPreLoadRace below), so each test keeps
// guarding one specific thing.
func TestBunkerBoardOverlayDoesNotBleedThroughToPanelBorders(t *testing.T) {
	client := &fullBoardTestClient{status: StatusInfo{IdentityPub: testNpubPub}}

	app := tui.NewApp().Init()
	flowLogger := &tui.FlowLogger{}
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(80, 25)
	app.SetScreen(screen)

	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)
	app.Load(board)

	go app.Run()
	defer app.Stop()

	time.Sleep(150 * time.Millisecond)

	// Open a plain centerOverlay(box, 90, 85) directly -- deliberately not
	// through one of board.go's own real dialogs (which, since this bug
	// was found, all moved to a full-screen 100/100 overlay specifically
	// to remove their own margin entirely -- see openSignEventApprovalDialog's
	// own comment). Testing the fix through centerOverlay itself, with an
	// explicit margin, keeps this guard meaningful regardless of what
	// percentage any particular caller happens to choose today.
	box := tview.NewBox().SetBorder(true).SetBackgroundColor(tcell.ColorDefault)
	app.QueueUpdateDraw(func() {
		app.ShowOverlay("bleedTestOverlay", centerOverlay(box, 90, 85), box)
	})

	// Give every other panel's own render() ticker (renderInterval, 2s)
	// at least one full cycle to redraw concurrently while the overlay is
	// open -- that concurrent redraw traffic is what exposed the bug;
	// checking immediately after opening wasn't enough to distinguish a
	// fixed centerOverlay from a broken one during investigation.
	time.Sleep(2500 * time.Millisecond)

	_, h := screen.Size()
	cells, width, _ := screen.GetContents()

	// centerOverlay(box, 90, 85) on an 80-wide screen leaves a margin in
	// columns 76..79; scanning that column range across every row also
	// catches the top/bottom margins bleeding through at the corners.
	var bled []string
	for y := 0; y < h; y++ {
		for x := 76; x < width; x++ {
			idx := y*width + x
			if idx < 0 || idx >= len(cells) {
				continue
			}
			r := cells[idx].Runes
			if len(r) > 0 && r[0] != ' ' && r[0] != 0 {
				bled = append(bled, fmt.Sprintf("(%d,%d)=%q", x, y, string(r)))
			}
		}
	}
	if len(bled) > 0 {
		t.Errorf("overlay's right margin (x=76..%d) shows %d non-blank cell(s) bled through from the panel underneath: %v", width-1, len(bled), bled)
	}
}

// TestBunkerBoardAutoPromptSurvivesPreLoadRace is a regression guard for
// a real bug found (and fixed, in maybeAutoPrompt) while investigating
// TestBunkerBoardOverlayDoesNotBleedThroughToPanelBorders above: a
// sign_event request already pending the instant the board is
// constructed makes PendingTable.Init's own synchronous auto-prompt fire
// *inside NewBunkerBoard's own constructor*, before the caller has any
// chance to call app.Load. At that point the dialog's own page gets added
// to the App's Pages before "main" is even one of them -- Load's own
// subsequent bookkeeping (SwitchToPage("main"), which hides every *other*
// page) used to silently steal the dialog's focus back to the board
// (specifically, all the way back down to the pending table itself) the
// moment Load ran, with nothing on screen indicating anything had gone
// wrong. maybeAutoPrompt's fix defers the actual dialog open
// (deferFollowUpDialog) so it can never run before Load has already
// returned. This constructs the board with the request already pending
// (unlike the sibling bleed-through test, which deliberately injects it
// post-Load to isolate that other bug) to guard the exact failure
// sequence that was observed.
func TestBunkerBoardAutoPromptSurvivesPreLoadRace(t *testing.T) {
	event := &nip01.Event{ID: "abcd", PubKey: "ef01", Kind: 1, Content: "hi", CreatedAt: 1}
	client := &fullBoardTestClient{status: StatusInfo{IdentityPub: testNpubPub}}
	client.setPending([]Pending{{ID: "req-1", ClientKey: "abc", Method: nip46.MethodSignEvent, Kind: 1, Event: event}})

	app := tui.NewApp().Init()
	flowLogger := &tui.FlowLogger{}
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(80, 25)
	app.SetScreen(screen)

	// The request is already pending *before* this call -- Init's own
	// synchronous Update (called from inside NewPendingTable(...).Init,
	// itself called from inside NewBunkerBoard's own constructor) fires
	// the auto-prompt here, strictly before app.Load below ever runs.
	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)
	if board.pending.promptedID != "req-1" {
		t.Fatalf("promptedID = %q, want %q -- the auto-prompt should have claimed it synchronously even pre-Load", board.pending.promptedID, "req-1")
	}

	app.Load(board)
	go app.Run()
	defer app.Stop()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := app.GetFocus().(*tview.Button); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	button, ok := app.GetFocus().(*tview.Button)
	if !ok {
		t.Fatalf("GetFocus() = %T, want the approval dialog's button -- Load must not steal focus back to the board", app.GetFocus())
	}
	if got := button.GetLabel(); got != "Approve Once" {
		t.Errorf("focused button label = %q, want %q", got, "Approve Once")
	}
	if board.HasFocus() {
		t.Error("board.HasFocus() = true while the approval dialog should hold exclusive focus -- Load's own page bookkeeping stole it back")
	}
}

// screenCellRune returns the rune at (x, y) in a GetContents() buffer, or
// 0 if that cell is out of range or blank -- shared by tests that need to
// check specific screen positions rather than scan a whole region.
func screenCellRune(cells []tcell.SimCell, width, x, y int) rune {
	idx := y*width + x
	if idx < 0 || idx >= len(cells) || len(cells[idx].Runes) == 0 {
		return 0
	}
	return cells[idx].Runes[0]
}

// TestPendingTableSignEventApprovalOverlayFillsFullScreenNoMargin guards
// the fix for a real user complaint: the previous 90/85 centered overlay
// (centerOverlay) left a margin where the board underneath was
// glimpsable around the edges -- see overlaySpacer's own doc comment for
// the deeper bleed-through bug that margin also exposed, fixed
// separately. The dialog now uses a full 100/100 centerOverlay instead,
// so its own border should sit directly on the screen's actual edges,
// not inset from them by any margin at all. Needs a real root (App.Init
// + Load, via the full board), unlike waitForAutoPromptFocus's other
// callers -- a bare PendingTable with no App.Init/Load never has
// Application.root set, so Application.draw's own early return
// (root == nil) means nothing is ever actually drawn to inspect.
func TestPendingTableSignEventApprovalOverlayFillsFullScreenNoMargin(t *testing.T) {
	event := &nip01.Event{ID: "abcd", PubKey: "ef01", Kind: 1, Content: "hello", CreatedAt: 1}
	client := &fullBoardTestClient{status: StatusInfo{IdentityPub: testNpubPub}}
	client.setPending([]Pending{{ID: "req-1", ClientKey: "abc", Method: nip46.MethodSignEvent, Kind: 1, Event: event}})

	app := tui.NewApp().Init()
	flowLogger := &tui.FlowLogger{}
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(80, 25)
	app.SetScreen(screen)

	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)
	app.Load(board)

	go app.Run()
	defer app.Stop()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := app.GetFocus().(*tview.Button); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := app.GetFocus().(*tview.Button); !ok {
		t.Fatalf("sign_event approval overlay never opened, focus = %T", app.GetFocus())
	}

	cells, width, _ := screen.GetContents()

	// The dialog's own border should sit directly on the top row and the
	// leftmost/rightmost columns -- a real margin (like the old 90/85
	// centering) would leave those cells blank instead.
	if r := screenCellRune(cells, width, 0, 0); r == 0 || r == ' ' {
		t.Errorf("top-left cell = %q, want the dialog's own border character (no margin above/left of it)", r)
	}
	if r := screenCellRune(cells, width, width-1, 0); r == 0 || r == ' ' {
		t.Errorf("top-right cell = %q, want the dialog's own border character (no margin to its right)", r)
	}
}

// TestHistoryTableShowEventDetailBorderStaysConsistentUnderConcurrency is
// a regression guard for a second, related bleed-through bug found right
// after the margin one: even with centerOverlay at 100/100 (no outer
// margin at all), view's own SetBorderPadding(1, 0, 2, 2) left a second,
// smaller gap -- inside the border, between it and the real content --
// that bled the exact same way under this board's real concurrent redraw
// traffic (every panel's own independent render() ticker). Confirmed via
// a minimal repro during investigation: a single ticker never showed it,
// but matching this board's real panel count (multiple independent
// tickers all racing to redraw while the dialog, whose own Form is
// focused, sits open) did, consistently. Fixed by dropping
// SetBorderPadding entirely (view.go's own comment) rather than trying to
// patch the gap itself -- the border-padding mechanism is Box-internal,
// not something this package can independently guarantee gets refilled
// on every concurrent redraw the way overlaySpacer's real primitives can.
// This checks the border column itself (not a padding gap, since there
// no longer is one) stays the single correct, uniform rune across every
// body row -- a stray different rune there would mean something else is
// still corrupting it.
func TestHistoryTableShowEventDetailBorderStaysConsistentUnderConcurrency(t *testing.T) {
	event := &nip01.Event{ID: "abcd", PubKey: "ef01", Kind: 1, Content: "hi", CreatedAt: 1, Sig: "deadbeef"}
	client := &fullBoardTestClient{status: StatusInfo{IdentityPub: testNpubPub}}

	app := tui.NewApp().Init()
	flowLogger := &tui.FlowLogger{}
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(160, 45)
	app.SetScreen(screen)

	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)
	app.Load(board)

	go app.Run()
	defer app.Stop()

	time.Sleep(150 * time.Millisecond)

	board.history.mu.Lock()
	board.history.rendered = []HistoryEntry{{ID: "req-1", Method: nip46.MethodSignEvent, Kind: 1, Verdict: Allow, Event: event}}
	board.history.mu.Unlock()

	app.QueueUpdateDraw(func() {
		board.history.showEventDetail(HistoryEntry{ID: "req-1", Method: nip46.MethodSignEvent, Kind: 1, Verdict: Allow, Event: event})
	})

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := app.GetFocus().(*tview.Button); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := app.GetFocus().(*tview.Button); !ok {
		t.Fatalf("event detail overlay never opened, focus = %T", app.GetFocus())
	}

	// Give every other panel's own render() ticker (renderInterval, 2s)
	// at least one full cycle to redraw concurrently while the overlay is
	// open -- see the sibling bleed-through test's own comment on why
	// this matters.
	time.Sleep(2500 * time.Millisecond)

	cells, width, h := screen.GetContents()

	// The dialog's own content (header text, JSON, form) never legitimately
	// contains box-drawing characters -- those only ever come from a
	// *different* widget's border. Scanning columns 1..3 (both where the
	// old SetBorderPadding(1, 0, 2, 2) gap used to sit, and where content
	// starts now that it's gone) for any of them is a signature check for
	// exactly this bleed, regardless of which column the fix happens to
	// put real content in.
	boxDrawing := map[rune]bool{
		'─': true, '│': true, '┌': true, '┐': true, '└': true, '┘': true,
		'━': true, '┃': true, '┏': true, '┓': true, '┗': true, '┛': true,
	}
	var bled []string
	for y := 1; y < h-1; y++ {
		for x := 1; x <= 3; x++ {
			r := screenCellRune(cells, width, x, y)
			if boxDrawing[r] {
				bled = append(bled, fmt.Sprintf("(%d,%d)=%q", x, y, string(r)))
			}
		}
	}
	if len(bled) > 0 {
		t.Errorf("found %d box-drawing character(s) inside the dialog's own content area (columns 1..3) -- another panel's border bled through: %v", len(bled), bled)
	}
}

// TestPendingTableShowBunkerURIActsLikeARealModal guards the fix for a
// user-reported regression: showBunkerURI used to be a full-screen
// centerOverlay page whose own margins had to actively stay filled (see
// overlaySpacer's own doc comment) -- which, cosmetically, meant the
// board behind it was replaced by a large solid-color void instead of
// staying visible, unlike a real modal dialog. It's now positioned
// directly (App.ShowPositionedOverlay, resize=false) instead of wrapped
// in a full-screen spacer Flex, so "main" keeps drawing itself normally
// everywhere except the dialog's own small rect. This checks both
// halves: the dialog itself renders at the expected position, and a
// point on the board well outside that rect is still live (main is
// still actually drawing there, not left blank), even after other
// panels' tickers have had a real chance to redraw concurrently.
func TestPendingTableShowBunkerURIActsLikeARealModal(t *testing.T) {
	client := &fullBoardTestClient{status: StatusInfo{IdentityPub: testNpubPub}}

	app := tui.NewApp().Init()
	flowLogger := &tui.FlowLogger{}
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(160, 45)
	app.SetScreen(screen)

	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)
	app.Load(board)

	go app.Run()
	defer app.Stop()

	time.Sleep(150 * time.Millisecond)

	app.QueueUpdateDraw(func() { board.pending.showBunkerURI() })

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := app.GetFocus().(*tview.Button); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := app.GetFocus().(*tview.Button); !ok {
		t.Fatalf("bunker:// URI overlay never opened, focus = %T", app.GetFocus())
	}

	// Let other panels' own render() tickers churn concurrently, same as
	// the sibling bleed-through tests -- this is what actually exposed
	// the original bugs, not the instant right after opening.
	time.Sleep(2500 * time.Millisecond)

	x, y, w, h := positionedOverlayRectFixedHeight(app, 100, 12)
	t.Logf("expected dialog rect: x=%d y=%d w=%d h=%d (screen 160x45)", x, y, w, h)

	cells, width, height := screen.GetContents()

	// The dialog's own title should be findable somewhere on its own top
	// border row.
	rowStr := ""
	for cx := x; cx < x+w && cx < width; cx++ {
		rowStr += string(screenCellRune(cells, width, cx, y))
	}
	if !strings.Contains(rowStr, "bunker") {
		t.Errorf("dialog's own title row (y=%d) = %q, want it to contain \"bunker\"", y, rowStr)
	}

	// Rows well outside the dialog's rect must still show live board
	// content, not a blank fill -- a real modal leaves the rest of the
	// screen alone. Checking for non-*space* here, not just non-zero: the
	// old full-screen centerOverlay approach this replaced also filled
	// its own margins with a deliberate blank space character (that was
	// its own fix, for the bleed-through bug), which would pass a
	// non-zero check just as easily as real content -- only "not a
	// uniformly blank row" actually distinguishes "board still visible"
	// from "board replaced by a solid-color void".
	rowHasContent := func(y int) bool {
		for cx := 0; cx < width; cx++ {
			if r := screenCellRune(cells, width, cx, y); r != 0 && r != ' ' {
				return true
			}
		}
		return false
	}

	if y > 0 && !rowHasContent(0) {
		t.Error("row y=0, above the dialog, is entirely blank -- want the board's own panels (e.g. the identity bar) still visible there")
	}

	belowY := y + h + 2
	if belowY < height && !rowHasContent(belowY) {
		t.Errorf("row y=%d, below the dialog, is entirely blank -- want the board's own panels still visible there", belowY)
	}

	// Guards a second, distinct bleed: heightRows (see the call above) used
	// to be 2 rows taller than what the Flex's own AddItem calls actually
	// claim (1+3+1+2+3 content + 2 border = 12) -- those 2 unclaimed rows
	// near the dialog's own bottom left view.Box.DrawForSubclass's
	// background paint not actually covering them, and Request History
	// showed straight through, *inside* the dialog's own left/right
	// border columns (not just outside them, which the checks above and
	// below already cover). "TIME"/"STATUS" are Request History's/Pending
	// Requests' own header text and never legitimately appear inside this
	// dialog's own content; a bare box-drawing rune inside those columns
	// would mean the same thing for a different reason (a table's border
	// rather than its header text).
	boxDrawing := map[rune]bool{
		'─': true, '│': true, '┌': true, '┐': true, '└': true, '┘': true,
		'━': true, '┃': true, '┏': true, '┓': true, '┗': true, '┛': true,
	}
	var bled []string
	for cy := y + 1; cy < y+h-1 && cy < height; cy++ {
		row := ""
		for cx := x + 1; cx < x+w-1 && cx < width; cx++ {
			r := screenCellRune(cells, width, cx, cy)
			row += string(r)
			if boxDrawing[r] {
				bled = append(bled, fmt.Sprintf("(%d,%d)=%q", cx, cy, string(r)))
			}
		}
		if strings.Contains(row, "TIME") || strings.Contains(row, "STATUS") {
			bled = append(bled, fmt.Sprintf("row %d contains %q", cy, strings.TrimSpace(row)))
		}
	}
	if len(bled) > 0 {
		t.Errorf("found %d sign(s) of another panel bleeding through inside the dialog's own content area: %v", len(bled), bled)
	}
}

// TestPendingTableShowConnectingDoesNotClobberRacingApproveDialog is a
// regression guard for a real bug: openNostrconnectInput's submit used to
// hand off through App.Alert + App.DismissDialog (a.pages.
// SwitchToPage("main")) for its up-to-a-minute network wait. That shares
// the same "dialog" page ShowDialog/Auto-Prompt's own approval dialog
// uses, and DismissDialog closes whatever currently occupies it with no
// regard for what that is -- if Auto-Prompt (or a manual Enter) opened an
// unrelated approval dialog while the pairing attempt was still in
// flight, the eventual completion silently swept it off screen with no
// error and no trace, and left PendingTable.promptedID stuck pointing at
// it -- wedging Auto-Prompt for good. showConnecting fixes this by using
// its own dedicated overlay page instead of the shared "dialog" one; this
// drives the actual race (a real client.Connect held open via
// connectGate, a real approval dialog opened while it's in flight) to
// prove the fix holds up, not just the isolated App.DismissOverlay unit
// test (client/tui's own TestDismissOverlayDoesNotStealFocusWhenAnother
// PageIsOnTop) that guards the mechanism it now relies on.
func TestPendingTableShowConnectingDoesNotClobberRacingApproveDialog(t *testing.T) {
	client := &fullBoardTestClient{status: StatusInfo{IdentityPub: testNpubPub}}
	client.connectGate = make(chan struct{})

	app := tui.NewApp().Init()
	flowLogger := &tui.FlowLogger{}
	screen := tcell.NewSimulationScreen("")
	screen.SetSize(160, 45)
	app.SetScreen(screen)

	board := NewBunkerBoard(app, t.Context(), client, flowLogger, true)
	app.Load(board)

	go app.Run()
	defer app.Stop()

	time.Sleep(150 * time.Millisecond)

	// Start the nostrconnect:// pairing flow -- opens the "Connecting..."
	// overlay and blocks in the background on client.Connect (held open by
	// connectGate), standing in for the real up-to-a-minute network wait.
	app.QueueUpdateDraw(func() {
		board.pending.showConnecting("nostrconnect://deadbeef?relay=wss://relay.example")
	})

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := app.GetFocus().(*tview.Button); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := app.GetFocus().(*tview.Button); !ok {
		t.Fatalf("connecting overlay never opened, focus = %T", app.GetFocus())
	}

	// While that's still in flight, an unrelated request needs a human
	// decision -- standing in for Auto-Prompt (or a manual Enter) opening
	// its own dialog on top.
	app.QueueUpdateDraw(func() {
		board.pending.openApprovalDialog(Pending{ID: "req-1", ClientKey: "abc", Method: nip46.MethodConnect})
	})

	deadline = time.Now().Add(4 * time.Second)
	var approveButton *tview.Button
	for time.Now().Before(deadline) {
		if b, ok := app.GetFocus().(*tview.Button); ok && b.GetLabel() == "Approve Once" {
			approveButton = b
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if approveButton == nil {
		t.Fatalf("approval dialog never opened, focus = %T", app.GetFocus())
	}

	// Now let the pairing attempt actually complete.
	close(client.connectGate)
	time.Sleep(300 * time.Millisecond)

	button, ok := app.GetFocus().(*tview.Button)
	if !ok || button.GetLabel() != "Approve Once" {
		t.Fatalf("focus after the racing pairing attempt completed = %v, want the still-open approval dialog's button untouched", app.GetFocus())
	}
	if board.pending.promptedID != "req-1" {
		t.Fatalf("promptedID = %q, want %q -- the approval dialog is still open and undecided, not silently swept away", board.pending.promptedID, "req-1")
	}
}
