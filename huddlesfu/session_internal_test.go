package huddlesfu

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/rs/zerolog"
)

// These cover the trickle-ICE queues directly. The behavioural tests next door
// can only observe the inbound half: the outbound race is between pion's
// gathering goroutine and the answer write, and on loopback the write wins
// every time, so an end-to-end test of it asserts an ordering that already held
// by luck. Here the queue is driven by hand, so the guarantee is actually
// checked.

// websocketPair returns the two ends of a live WebSocket.
func websocketPair(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()

	accepted := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		accepted <- conn
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	select {
	case server = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the server never accepted the connection")
	}
	t.Cleanup(func() { _ = server.Close() })
	return server, client
}

func readSignal(t *testing.T, conn *websocket.Conn) signal {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var s signal
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return s
}

// TestLocalCandidatesWaitForTheDescription is the outbound guarantee: pion
// starts gathering inside SetLocalDescription, so candidates exist before the
// answer reaches the wire. A client that receives one first rejects it, exactly
// as this server does in the other direction.
func TestLocalCandidatesWaitForTheDescription(t *testing.T) {
	server, client := websocketPair(t)
	s := &session{conn: server, log: zerolog.Nop()}

	gathered := []string{"candidate:1 host", "candidate:2 srflx", "candidate:3 relay"}
	for _, c := range gathered {
		if s.queueLocalCandidate(webrtc.ICECandidateInit{Candidate: c}) {
			t.Fatalf("%q was released before the description was written", c)
		}
	}

	if err := s.writeDescription("answer", "v=0"); err != nil {
		t.Fatalf("writeDescription: %v", err)
	}

	// The description comes out first...
	if got := readSignal(t, client); got.Type != "answer" || got.SDP != "v=0" {
		t.Fatalf("first frame = %+v, want the answer", got)
	}
	// ...then everything held back, in gathering order.
	for _, want := range gathered {
		got := readSignal(t, client)
		if got.Type != "candidate" {
			t.Fatalf("frame = %+v, want a candidate", got)
		}
		if got.Candidate == nil || got.Candidate.Candidate != want {
			t.Fatalf("candidate = %+v, want %q", got.Candidate, want)
		}
	}

	// Once the description is out, later candidates go straight to the wire.
	if !s.queueLocalCandidate(webrtc.ICECandidateInit{Candidate: "candidate:4 host"}) {
		t.Fatal("a candidate gathered after the description was still held back")
	}
}

// TestLocalCandidateQueueIsCapped keeps a peer that never triggers a
// description write from growing the queue without bound.
func TestLocalCandidateQueueIsCapped(t *testing.T) {
	server, _ := websocketPair(t)
	s := &session{conn: server, log: zerolog.Nop()}

	for i := 0; i < maxPendingCandidates*2; i++ {
		s.queueLocalCandidate(webrtc.ICECandidateInit{Candidate: "candidate:x"})
	}
	if len(s.pendingLocal) != maxPendingCandidates {
		t.Fatalf("queued %d, want the cap of %d", len(s.pendingLocal), maxPendingCandidates)
	}
}

// TestRemoteCandidatesWaitForTheOffer is the inbound half: pion rejects a
// candidate outright while there is no remote description, so one that arrives
// before the offer has to be held rather than discarded.
func TestRemoteCandidatesWaitForTheOffer(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("peer connection: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	s := &session{pc: pc, log: zerolog.Nop()}

	for i := 0; i < 3; i++ {
		s.addRemoteCandidate(webrtc.ICECandidateInit{Candidate: "candidate:1 1 udp 2130706431 127.0.0.1 9 typ host"})
	}
	if len(s.pendingRemote) != 3 {
		t.Fatalf("queued %d candidates, want 3", len(s.pendingRemote))
	}

	for i := 0; i < maxPendingCandidates*2; i++ {
		s.addRemoteCandidate(webrtc.ICECandidateInit{Candidate: "candidate:1 1 udp 2130706431 127.0.0.1 9 typ host"})
	}
	if len(s.pendingRemote) != maxPendingCandidates {
		t.Fatalf("queued %d, want the cap of %d", len(s.pendingRemote), maxPendingCandidates)
	}

	// Flushing empties the queue, whatever each candidate's own fate.
	s.flushRemoteCandidates()
	if len(s.pendingRemote) != 0 {
		t.Fatalf("%d candidates left queued after the flush", len(s.pendingRemote))
	}
}
