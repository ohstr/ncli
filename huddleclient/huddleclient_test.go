package huddleclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/ncli/huddleclient"
	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/huddle/wire"
	"github.com/ohstr/nmilat/huddle/wsaudio"
	"github.com/ohstr/nmilat/utils"
	"github.com/rs/zerolog"
)

const (
	relayURL  = "wss://relay.example"
	alicePriv = "0acd1245d4b0e9cae1a3b0e1a1e0cf3d1e5b2a7c8d9e0f1a2b3c4d5e6f70268c"
	bobPriv   = "1bde2356e5c1fadbf2b4c1f2b2f1d04e2f6c3b8d9eaf102b3c4d5e6f78901234"
	window    = 15 * time.Second
)

// The client is tested against the real relay-side handler, not a stub: the
// handshake, the roster and the frame framing are exactly the things a stub
// would get wrong in the same way the client does.
type harness struct {
	t     *testing.T
	srv   *httptest.Server
	rooms *room.Manager
}

func newHarness(t *testing.T, mutate func(*wsaudio.Config)) *harness {
	t.Helper()
	rooms := room.NewManager(0)
	cfg := wsaudio.Config{Enabled: true, RelayURL: relayURL, Rooms: rooms, Logger: zerolog.Nop()}
	if mutate != nil {
		mutate(&cfg)
	}
	mux := http.NewServeMux()
	mux.Handle("/huddle/{id}/audio", wsaudio.NewHandler(cfg))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &harness{t: t, srv: srv, rooms: cfg.Rooms}
}

func (h *harness) endpoint(roomID string) string {
	return "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/huddle/" + roomID + "/audio"
}

func (h *harness) dial(roomID, privKey string) *huddleclient.Client {
	h.t.Helper()
	c, err := huddleclient.Dial(context.Background(), huddleclient.Config{
		Endpoint: h.endpoint(roomID), RelayURL: relayURL, PrivKey: privKey,
	})
	if err != nil {
		h.t.Fatalf("Dial: %v", err)
	}
	h.t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestDialJoinsAndReportsSelf(t *testing.T) {
	h := newHarness(t, nil)
	pubkey, err := utils.GetPublicKey(alicePriv)
	if err != nil {
		t.Fatalf("GetPublicKey: %v", err)
	}

	c := h.dial("room-1", alicePriv)

	if c.Self().Pubkey != pubkey {
		t.Errorf("Self().Pubkey = %q, want %q", c.Self().Pubkey, pubkey)
	}
	if c.Self().Epoch == 0 {
		t.Error("Self().Epoch = 0, want epochs to start at 1")
	}
	// The roster includes our own entry, as the relay reports it.
	roster := c.Roster()
	if len(roster) != 1 || roster[0].Pubkey != pubkey {
		t.Errorf("Roster() = %+v, want just ourselves", roster)
	}
}

func TestDialRequiresAPrivateKey(t *testing.T) {
	h := newHarness(t, nil)
	_, err := huddleclient.Dial(context.Background(), huddleclient.Config{
		Endpoint: h.endpoint("room-1"), RelayURL: relayURL,
	})
	if !errors.Is(err, huddleclient.ErrNoPrivateKey) {
		t.Fatalf("err = %v, want ErrNoPrivateKey", err)
	}
}

// A refusal must carry the relay's code, so a caller can distinguish "room full"
// from "you may not join" without reading prose.
func TestDialSurfacesTheRelaysRefusalCode(t *testing.T) {
	h := newHarness(t, func(c *wsaudio.Config) {
		c.Authorize = func(context.Context, string, string) error { return errors.New("nope") }
	})

	_, err := huddleclient.Dial(context.Background(), huddleclient.Config{
		Endpoint: h.endpoint("room-1"), RelayURL: relayURL, PrivKey: alicePriv,
	})
	if !errors.Is(err, huddleclient.ErrRefused) {
		t.Fatalf("err = %v, want it to wrap ErrRefused", err)
	}
	var refused *huddleclient.RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want a *RefusedError", err)
	}
	if refused.Code != wsaudio.CodeJoinRejected {
		t.Errorf("Code = %q, want %q", refused.Code, wsaudio.CodeJoinRejected)
	}
}

func TestDialFailsOnABadAuthEvent(t *testing.T) {
	h := newHarness(t, nil)
	// Signing for a different relay: the endpoint validates the relay tag.
	_, err := huddleclient.Dial(context.Background(), huddleclient.Config{
		Endpoint: h.endpoint("room-1"), RelayURL: "wss://somewhere.else", PrivKey: alicePriv,
	})
	var refused *huddleclient.RefusedError
	if !errors.As(err, &refused) || refused.Code != wsaudio.CodeAuthFailed {
		t.Fatalf("err = %v, want auth_failed", err)
	}
}

// Two clients in a room: one speaks, the other receives it attributed to the
// speaker's pubkey -- which is only possible because the client tracks the
// roster and maps the routing index back.
func TestFramesArriveAttributedToTheSpeaker(t *testing.T) {
	h := newHarness(t, nil)

	listener := h.dial("room-1", bobPriv)
	speaker := h.dial("room-1", alicePriv)

	speakerPub, err := utils.GetPublicKey(alicePriv)
	if err != nil {
		t.Fatalf("GetPublicKey: %v", err)
	}

	// Wait for the listener to learn about the speaker before attributing.
	deadline := time.Now().Add(window)
	for len(listener.Roster()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("listener roster = %+v, want both peers", listener.Roster())
		}
		time.Sleep(10 * time.Millisecond)
	}

	opus := []byte{0xFC, 0x01, 0x02, 0x03}
	go func() {
		seq, ts := uint16(1), uint32(960)
		for i := 0; i < 200; i++ {
			_ = speaker.Send(wire.FrameHeader{Seq: seq, Ts48k: ts, LevelDbov: -20}, opus)
			seq++
			ts += 960
			time.Sleep(10 * time.Millisecond)
		}
	}()

	timeout := time.After(window)
	for {
		select {
		case frame, ok := <-listener.Frames():
			if !ok {
				t.Fatalf("the frame stream closed early: %v", listener.Err())
			}
			if !frame.Attributed {
				continue // roster may still have been catching up
			}
			if frame.Author.Pubkey != speakerPub {
				t.Fatalf("attributed to %q, want the speaker %q", frame.Author.Pubkey, speakerPub)
			}
			if string(frame.Opus) != string(opus) {
				t.Errorf("Opus = %x, want %x", frame.Opus, opus)
			}
			if frame.Header.LevelDbov != -20 {
				t.Errorf("LevelDbov = %d, want -20", frame.Header.LevelDbov)
			}
			if !frame.Speaking(huddleclient.DefaultSpeakingThreshold) {
				t.Error("Speaking() = false for a -20 dBov frame")
			}
			return
		case <-timeout:
			t.Fatal("the listener never received an attributed frame")
		}
	}
}

// A speaker never hears itself, so its own stream stays empty.
func TestASpeakerDoesNotHearItself(t *testing.T) {
	h := newHarness(t, nil)
	speaker := h.dial("room-1", alicePriv)

	for i := 0; i < 5; i++ {
		if err := speaker.Send(wire.FrameHeader{Seq: uint16(i)}, []byte{0x01}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	select {
	case frame := <-speaker.Frames():
		t.Errorf("the speaker received its own frame: %+v", frame)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestRosterTracksALeave(t *testing.T) {
	h := newHarness(t, nil)
	stayer := h.dial("room-1", bobPriv)
	leaver := h.dial("room-1", alicePriv)

	deadline := time.Now().Add(window)
	for len(stayer.Roster()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("roster = %+v, want 2", stayer.Roster())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := leaver.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	deadline = time.Now().Add(window)
	for len(stayer.Roster()) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("roster = %+v, want the leaver removed", stayer.Roster())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSpeakingThreshold(t *testing.T) {
	tests := []struct {
		name  string
		level int8
		dtx   bool
		want  bool
	}{
		{name: "loud speech", level: -20, want: true},
		{name: "just above the threshold", level: -54, want: true},
		{name: "at the threshold", level: huddleclient.DefaultSpeakingThreshold},
		{name: "below the threshold", level: -80},
		{name: "silence floor", level: wire.LevelSilenceFloor},
		// Comfort noise is not speech however loud the level claims to be.
		{name: "dtx is never speaking", level: -10, dtx: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			header := wire.FrameHeader{LevelDbov: tc.level}
			if tc.dtx {
				header.Flags |= wire.FlagDTX
			}
			frame := huddleclient.Frame{Header: header}
			if got := frame.Speaking(huddleclient.DefaultSpeakingThreshold); got != tc.want {
				t.Errorf("Speaking() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCloseIsIdempotentAndStopsSending(t *testing.T) {
	h := newHarness(t, nil)
	c := h.dial("room-1", alicePriv)

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := c.Send(wire.FrameHeader{}, []byte{0x01}); !errors.Is(err, huddleclient.ErrClosed) {
		t.Errorf("Send after Close = %v, want ErrClosed", err)
	}
	// A clean close is not an error.
	if err := c.Err(); err != nil {
		t.Errorf("Err() = %v, want nil after our own Close", err)
	}
	// The stream is closed, so a consumer ranging over it terminates.
	if _, ok := <-c.Frames(); ok {
		t.Error("Frames() still delivering after Close")
	}
}

func TestSendIgnoresAnEmptyPayload(t *testing.T) {
	h := newHarness(t, nil)
	c := h.dial("room-1", alicePriv)
	if err := c.Send(wire.FrameHeader{}, nil); err != nil {
		t.Errorf("Send(nil) = %v, want nil: there is no audio to send, which is not an error", err)
	}
}
