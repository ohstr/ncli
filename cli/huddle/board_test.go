package huddle

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/ncli/huddleclient"
	"github.com/ohstr/nmilat/huddle/wire"
)

// Two well-formed 32-byte hex pubkeys, so nip19 encoding is exercised for real
// rather than falling through shortNpub's hex fallback.
const (
	selfPub = "0000000000000000000000000000000000000000000000000000000000000001"
	peerPub = "0000000000000000000000000000000000000000000000000000000000000002"
)

type fakeClient struct {
	self   huddleclient.Peer
	frames chan huddleclient.Frame

	mu     sync.Mutex
	roster []huddleclient.Peer
	err    error
	closes int
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		self:   huddleclient.Peer{Pubkey: selfPub, Index: 0},
		frames: make(chan huddleclient.Frame, 8),
		roster: []huddleclient.Peer{{Pubkey: selfPub, Index: 0}},
	}
}

func (f *fakeClient) Self() huddleclient.Peer { return f.self }

func (f *fakeClient) Roster() []huddleclient.Peer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]huddleclient.Peer(nil), f.roster...)
}

func (f *fakeClient) Frames() <-chan huddleclient.Frame { return f.frames }

func (f *fakeClient) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func (f *fakeClient) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	return nil
}

func (f *fakeClient) setRoster(peers ...huddleclient.Peer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roster = peers
}

func (f *fakeClient) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeClient) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

func TestMeter(t *testing.T) {
	tests := []struct {
		name  string
		level int8
		want  string
	}{
		{"silence floor is empty", wire.LevelSilenceFloor, strings.Repeat("·", meterBars)},
		{"at the meter floor is empty", meterFloor, strings.Repeat("·", meterBars)},
		{"below the meter floor is empty", meterFloor - 10, strings.Repeat("·", meterBars)},
		{"halfway", meterFloor / 2, strings.Repeat("█", 5) + strings.Repeat("·", 5)},
		{"full scale at 0 dBov", 0, strings.Repeat("█", meterBars)},
		{"one step up from the floor", meterFloor + 6, "█" + strings.Repeat("·", 9)},
		// Levels are client-authored; a positive value is out of spec and must
		// clamp rather than overrun the bar or panic on a negative repeat.
		{"positive level clamps", 100, strings.Repeat("█", meterBars)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := meter(tt.level)
			if got != tt.want {
				t.Errorf("meter(%d) = %q, want %q", tt.level, got, tt.want)
			}
			if len([]rune(got)) != meterBars {
				t.Errorf("meter(%d) has %d runes, want %d -- the meter must be fixed width",
					tt.level, len([]rune(got)), meterBars)
			}
		})
	}
}

func TestShortNpub(t *testing.T) {
	got := shortNpub(selfPub)
	if !strings.HasPrefix(got, "npub1") {
		t.Errorf("want a bech32 npub, got %q", got)
	}
	if !strings.Contains(got, "...") {
		t.Errorf("want a truncated npub, got %q", got)
	}
	if len(got) != 21 {
		t.Errorf("want the 14+3+4 shape (21 chars), got %d in %q", len(got), got)
	}

	// Not encodable: must fall back rather than return empty.
	bad := shortNpub("not-a-pubkey-but-long-enough-to-truncate")
	if bad == "" || strings.HasPrefix(bad, "npub") {
		t.Errorf("want a hex fallback for an unencodable key, got %q", bad)
	}
	if short := shortNpub("abc"); short != "abc" {
		t.Errorf("a short unencodable key should pass through, got %q", short)
	}
}

func TestStateText(t *testing.T) {
	tests := []struct {
		name string
		p    Participant
		want string
	}{
		{"speaking", Participant{Speaking: true, Heard: true}, "speaking"},
		{"heard but quiet", Participant{Heard: true}, "silent"},
		{"never heard", Participant{}, "no audio yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stateText(tt.p); got != tt.want {
				t.Errorf("stateText(%+v) = %q, want %q", tt.p, got, tt.want)
			}
		})
	}

	// The protocol carries no mute signal, so nothing here may claim to know a
	// peer is muted -- that would be inventing information.
	for _, tt := range tests {
		if strings.Contains(strings.ToLower(stateText(tt.p)), "mute") {
			t.Errorf("stateText must never report mute state: %q", stateText(tt.p))
		}
	}
}

func cellText(t *testing.T, b *Board, row, col int) string {
	t.Helper()
	cell := b.table.GetCell(row, col)
	if cell == nil {
		t.Fatalf("no cell at (%d,%d)", row, col)
	}
	return cell.Text
}

func TestBoardRendersTheRoster(t *testing.T) {
	fc := newFakeClient()
	fc.setRoster(
		huddleclient.Peer{Pubkey: selfPub, Index: 0},
		huddleclient.Peer{Pubkey: peerPub, Index: 3},
	)
	b := NewBoard(nil, fc, "room-1")

	b.roster.sync(fc.Roster())
	b.roster.heardFrame(huddleclient.Frame{
		Author:     huddleclient.Peer{Pubkey: peerPub},
		Header:     wire.FrameHeader{LevelDbov: -12},
		Attributed: true,
	})
	b.render(b.roster.participants())

	if got := cellText(t, b, 0, 1); got != "PARTICIPANT" {
		t.Errorf("want a header row, got %q", got)
	}
	// Self is row 1 and labelled.
	if got := cellText(t, b, 1, 1); !strings.Contains(got, "(you)") {
		t.Errorf("want self labelled on the first row, got %q", got)
	}
	if got := cellText(t, b, 1, 3); got != "no audio yet" {
		t.Errorf("want self silent, got %q", got)
	}
	// The peer that spoke is row 2, lit and marked speaking.
	if got := cellText(t, b, 2, 0); !strings.Contains(got, "●") {
		t.Errorf("want a filled indicator for the speaker, got %q", got)
	}
	if got := cellText(t, b, 2, 3); got != "speaking" {
		t.Errorf("want the speaker marked speaking, got %q", got)
	}
	if got := cellText(t, b, 2, 2); got == strings.Repeat("·", meterBars) {
		t.Errorf("want a non-empty meter for a -12 dBov frame, got %q", got)
	}
	if title := b.panel.GetTitle(); !strings.Contains(title, "room-1") ||
		!strings.Contains(title, "talking") {
		t.Errorf("want the room and a talking count in the title, got %q", title)
	}
}

func TestBoardOpensWithTheRosterAlreadyPopulated(t *testing.T) {
	fc := newFakeClient()
	fc.setRoster(
		huddleclient.Peer{Pubkey: selfPub, Index: 0},
		huddleclient.Peer{Pubkey: peerPub, Index: 1},
	)

	// No Run, no tick: whatever is on screen here is the very first paint.
	b := NewBoard(nil, fc, "room-1")

	if got := len(b.Participants()); got != 2 {
		t.Errorf("want the roster seeded at construction, got %d rows", got)
	}
	if got := cellText(t, b, 1, 1); !strings.Contains(got, "(you)") {
		t.Errorf("want self on the first painted row, got %q", got)
	}
	if title := b.panel.GetTitle(); !strings.Contains(title, "[2]") {
		t.Errorf("want the participant count in the opening title, got %q", title)
	}
}

func TestBoardStatusLine(t *testing.T) {
	fc := newFakeClient()
	b := NewBoard(nil, fc, "room-1")

	// Alone in the room: say so, because an empty roster otherwise reads as a
	// broken connection.
	if got := b.statusLine(1); !strings.Contains(got, "waiting for others") {
		t.Errorf("want a waiting hint when alone, got %q", got)
	}
	if got := b.statusLine(3); !strings.Contains(got, "connected") ||
		strings.Contains(got, "waiting") {
		t.Errorf("want a plain connected status with company, got %q", got)
	}

	fc.setErr(context.DeadlineExceeded)
	got := b.statusLine(3)
	if !strings.Contains(got, "disconnected") ||
		!strings.Contains(got, context.DeadlineExceeded.Error()) {
		t.Errorf("want the disconnect reason surfaced, got %q", got)
	}
}

func TestBoardSeams(t *testing.T) {
	fc := newFakeClient()
	b := NewBoard(nil, fc, "room-1")

	childs := b.Childs()
	if len(childs) != 1 || childs[0] != b.table {
		t.Errorf("want just the table focusable, got %+v", childs)
	}
	hints := b.FooterHints(nil)
	for _, want := range []string{"<Tab>", "<q>", "<Ctrl+C>", "Leave"} {
		if !strings.Contains(hints, want) {
			t.Errorf("footer hints missing %q: %q", want, hints)
		}
	}
	// Without an app there is no dialog to show, so Ctrl+C must fall through to
	// tview rather than swallow the keystroke and trap the user.
	if b.HandleCtrlC() {
		t.Error("HandleCtrlC should not claim the key with no app attached")
	}
}

func TestBoardLeaveIsIdempotent(t *testing.T) {
	fc := newFakeClient()
	b := NewBoard(nil, fc, "room-1")

	b.Leave()
	b.Leave()
	if got := fc.closeCount(); got != 1 {
		t.Errorf("want the client closed exactly once, got %d", got)
	}
}

func TestBoardRunClearsSpeakingWhenAPeerGoesQuiet(t *testing.T) {
	fc := newFakeClient()
	fc.setRoster(
		huddleclient.Peer{Pubkey: selfPub, Index: 0},
		huddleclient.Peer{Pubkey: peerPub, Index: 1},
	)
	b := NewBoard(nil, fc, "room-1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Run(ctx)
	}()

	fc.frames <- huddleclient.Frame{
		Author:     huddleclient.Peer{Pubkey: peerPub},
		Header:     wire.FrameHeader{LevelDbov: -12},
		Attributed: true,
	}

	// Non-fatal lookup: the row only appears once Run's first tick syncs the
	// roster, so "not there yet" is a legitimate intermediate state here.
	peerState := func() (present, speaking bool) {
		for _, r := range b.roster.participants() {
			if r.Pubkey == peerPub {
				return true, r.Speaking
			}
		}
		return false, false
	}

	// Lights up on the frame...
	if !waitFor(t, 2*time.Second, func() bool {
		present, speaking := peerState()
		return present && speaking
	}) {
		t.Fatal("peer never showed as speaking after sending a frame")
	}

	// ...and clears on the refresh tick alone, with no further frames. This is
	// the property the ticker exists for: going quiet produces no event.
	if !waitFor(t, 2*time.Second, func() bool {
		present, speaking := peerState()
		return present && !speaking
	}) {
		t.Fatal("peer stayed lit after going quiet -- the refresh tick is not clearing it")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return on context cancellation")
	}
}

func TestBoardRunReturnsWhenTheCallEnds(t *testing.T) {
	fc := newFakeClient()
	b := NewBoard(nil, fc, "room-1")
	fc.setErr(context.Canceled)

	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Run(context.Background())
	}()

	// The reader goroutine closing its channel is how the client reports the
	// call is over.
	close(fc.frames)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return when the frame channel closed")
	}
	if got := b.statusLine(1); !strings.Contains(got, "disconnected") {
		t.Errorf("want the final draw to show the disconnect, got %q", got)
	}
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
