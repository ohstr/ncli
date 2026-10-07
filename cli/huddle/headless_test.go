package huddle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/ncli/huddleclient"
	"github.com/ohstr/nmilat/huddle/wire"
)

type eventRecorder struct {
	mu     sync.Mutex
	events []joinEvent
}

func (r *eventRecorder) emit(e joinEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *eventRecorder) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.Type)
	}
	return out
}

func (r *eventRecorder) waitFor(t *testing.T, typ string) joinEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, e := range r.events {
			if e.Type == typ {
				r.mu.Unlock()
				return e
			}
		}
		r.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %q event; got %v", typ, r.types())
	return joinEvent{}
}

func TestRunHeadless_StreamsRosterSpeakingAndChat(t *testing.T) {
	fc := newFakeClient()
	chat := newFakeChat()
	rec := &eventRecorder{}

	ctx := context.Background()
	durationCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runHeadless(ctx, durationCtx, fc, "standup", chatActivity, chat, rec.emit) }()

	if e := rec.waitFor(t, "joined"); e.Room != "standup" || e.Self != selfPub || e.Activity != chatActivity {
		t.Errorf("joined = %+v", e)
	}

	fc.setRoster(huddleclient.Peer{Pubkey: selfPub}, huddleclient.Peer{Pubkey: peerPub, Index: 1})
	if e := rec.waitFor(t, "participant_joined"); e.Pubkey != peerPub {
		t.Errorf("participant_joined = %+v", e)
	}

	fc.frames <- huddleclient.Frame{
		Author:     huddleclient.Peer{Pubkey: peerPub},
		Header:     wire.FrameHeader{LevelDbov: -12},
		Attributed: true,
	}
	rec.waitFor(t, "speaking_started")

	chat.incoming <- chatEvent("m1", peerPub, "hello", 100, "")
	chat.incoming <- chatEvent("m1", peerPub, "hello", 100, "") // duplicate, dropped
	if e := rec.waitFor(t, "chat"); e.ID != "m1" || e.Content != "hello" || e.Pubkey != peerPub {
		t.Errorf("chat = %+v", e)
	}

	fc.setRoster(huddleclient.Peer{Pubkey: selfPub})
	rec.waitFor(t, "participant_left")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("runHeadless = %v", err)
	}
	if e := rec.waitFor(t, "ended"); e.Reason != endDuration {
		t.Errorf("ended = %+v, want reason %q", e, endDuration)
	}

	chats := 0
	for _, e := range rec.events {
		if e.Type == "chat" {
			chats++
		}
		if e.Type == "participant_joined" && e.Pubkey == selfPub {
			t.Errorf("self reported as a participant")
		}
	}
	if chats != 1 {
		t.Errorf("chat events = %d, want 1 (duplicate dropped)", chats)
	}
}

func TestRunHeadless_DroppedCallIsAnError(t *testing.T) {
	fc := newFakeClient()
	fc.setErr(errors.New("relay went away"))
	rec := &eventRecorder{}
	close(fc.frames)

	err := runHeadless(context.Background(), context.Background(), fc, "standup", "", nil, rec.emit)
	if err == nil || err.Error() != "relay went away" {
		t.Fatalf("runHeadless = %v, want the client's error", err)
	}
	if e := rec.waitFor(t, "ended"); e.Reason != endDisconnected {
		t.Errorf("ended = %+v", e)
	}
}
