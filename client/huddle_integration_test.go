package client

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/ncli/huddleclient"
	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/huddle/wire"
	"github.com/stretchr/testify/require"
)

// See integration/huddle/README.md. Shared helpers live in
// client/integrationharness_test.go.
const (
	huddleIntegrationComposeFile = "../integration/huddle/compose.yaml"
	huddleIntegrationRelayURL    = "ws://localhost:21530"
)

// huddlePeerKey is a distinct, deterministic private key per peer. Any 32-byte
// scalar below the curve order is a valid secp256k1 key, so counting up from 1
// yields as many identities as a scenario needs without a table of literals.
func huddlePeerKey(i int) string { return fmt.Sprintf("%064x", i+1) }

// huddleIntegrationEndpoint is where the relay container is actually reachable.
// It is the published port by default. NCLI_ITEST_HUDDLE_ENDPOINT overrides it
// for a host whose Docker daemon runs elsewhere, where the published port does
// not land on localhost -- the NIP-42 relay tag stays huddleIntegrationRelayURL
// either way, because the endpoint validates the tag against its own configured
// nip11.url rather than against wherever the client happened to dial.
func huddleIntegrationEndpoint() string {
	if override := os.Getenv("NCLI_ITEST_HUDDLE_ENDPOINT"); override != "" {
		return override
	}
	return huddleIntegrationRelayURL
}

func huddleEndpointFor(roomID string) string {
	return huddleIntegrationEndpoint() + "/huddle/" + roomID + "/audio"
}

// dialHuddlePeer joins roomID as peer i. version 0 means the client's default.
func dialHuddlePeer(t *testing.T, ctx context.Context, roomID string, i int, version uint8) *huddleclient.Client {
	t.Helper()
	c, err := huddleclient.Dial(ctx, huddleclient.Config{
		Endpoint:        huddleEndpointFor(roomID),
		RelayURL:        huddleIntegrationRelayURL,
		PrivKey:         huddlePeerKey(i),
		ProtocolVersion: version,
	})
	require.NoError(t, err, "peer %d could not join %s", i, roomID)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// speak sends one 20 ms frame carrying payload at a speaking level.
func speak(t *testing.T, c *huddleclient.Client, seq uint16, payload []byte) {
	t.Helper()
	require.NoError(t, c.Send(wire.FrameHeader{
		Seq:       seq,
		Ts48k:     uint32(seq) * wire.SamplesPerFrame,
		LevelDbov: -20,
	}, payload))
}

// nextFrame reads one frame, copying Opus because it aliases the read buffer
// only until the next read.
func nextFrame(t *testing.T, c *huddleclient.Client, timeout time.Duration) huddleclient.Frame {
	t.Helper()
	select {
	case f, ok := <-c.Frames():
		require.True(t, ok, "frame channel closed: %v", c.Err())
		f.Opus = append([]byte(nil), f.Opus...)
		return f
	case <-time.After(timeout):
		t.Fatalf("no frame within %v (client err: %v)", timeout, c.Err())
		return huddleclient.Frame{}
	}
}

func expectNoFrame(t *testing.T, c *huddleclient.Client, window time.Duration) {
	t.Helper()
	select {
	case f := <-c.Frames():
		t.Fatalf("unexpected frame from %s: % x", f.Author.Pubkey, f.Opus)
	case <-time.After(window):
	}
}

// TestHuddleIntegration brings up compose.yaml's one relay container with
// huddles enabled, then runs each scenario as a subtest against it. Needs
// Docker.
//
// Every scenario uses its own room id: rooms are created on first join and
// dropped when the last peer leaves, so isolating by id keeps a lingering peer
// from one scenario out of the next one's roster.
func TestHuddleIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker-based huddle integration test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found on PATH, skipping huddle integration test")
	}

	runCompose(t, huddleIntegrationComposeFile, "up", "-d", "--build")
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "-f", huddleIntegrationComposeFile, "down", "-v")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("docker compose down failed: %v\n%s", err, out)
		}
	})

	waitForRelayReady(t, huddleIntegrationEndpoint(), 60*time.Second)

	t.Run("TwoPeersHearEachOther", testHuddleTwoPeersHearEachOther)
	t.Run("EveryPeerHearsEveryOther", testHuddleEveryPeerHearsEveryOther)
	t.Run("JoinMidCall", testHuddleJoinMidCall)
	t.Run("LeaveRemovesThePeer", testHuddleLeaveRemovesThePeer)
	t.Run("SimultaneousSpeech", testHuddleSimultaneousSpeech)
	t.Run("DTXAndLevelSurvive", testHuddleDTXAndLevelSurvive)
	t.Run("RoomFullRefusesBeyondCapacity", testHuddleRoomFullRefusesBeyondCapacity)
	t.Run("AuthForAnotherRelayIsRejected", testHuddleAuthForAnotherRelayIsRejected)
	t.Run("VersionMismatchRequiresUpgrade", testHuddleVersionMismatchRequiresUpgrade)

	// The WebRTC door. Same relay, same rooms -- a browser peer and a WebSocket
	// peer on one room id are in one call, which MixedTransportConversation is
	// there to prove.
	t.Run("MixedTransportConversation", testHuddleMixedTransportConversation)
	t.Run("VideoAndScreenShare", testHuddleVideoAndScreenShare)
	t.Run("WebSocketPeerSeesNoVideo", testHuddleWebSocketPeerSeesNoVideo)
	t.Run("LateJoinerGetsTheLiveCall", testHuddleLateJoinerGetsTheLiveCall)
	t.Run("CandidatesBeforeTheOffer", testHuddleCandidatesBeforeTheOffer)
}

// testHuddleTwoPeersHearEachOther is the core guarantee: the payload crosses the
// relay untouched, attributed to its author, and never echoes back to its
// sender.
func testHuddleTwoPeersHearEachOther(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	alice := dialHuddlePeer(t, ctx, "two-peer", 1, 0)
	bob := dialHuddlePeer(t, ctx, "two-peer", 2, 0)

	payload := []byte("alice-opus-payload")
	speak(t, alice, 7, payload)

	got := nextFrame(t, bob, 15*time.Second)
	require.Equal(t, payload, got.Opus, "the Opus payload must cross the relay byte-identical")
	require.True(t, got.Attributed)
	require.Equal(t, alice.Self().Pubkey, got.Author.Pubkey, "frame attributed to the wrong peer")
	require.Equal(t, uint16(7), got.Header.Seq, "the header must survive too")
	require.Equal(t, int8(-20), got.Header.LevelDbov)

	// The sender is not a subscriber to itself.
	expectNoFrame(t, alice, 500*time.Millisecond)

	// And it works in the other direction on the same connection pair.
	reply := []byte("bob-opus-payload")
	speak(t, bob, 1, reply)
	back := nextFrame(t, alice, 15*time.Second)
	require.Equal(t, reply, back.Opus)
	require.Equal(t, bob.Self().Pubkey, back.Author.Pubkey)
}

func testHuddleEveryPeerHearsEveryOther(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const peers = 5
	clients := make([]*huddleclient.Client, peers)
	for i := 0; i < peers; i++ {
		clients[i] = dialHuddlePeer(t, ctx, "many-peer", 10+i, 0)
	}

	// Everyone sees everyone, including themselves, once the roster settles.
	for i, c := range clients {
		require.Eventually(t, func() bool { return len(c.Roster()) == peers },
			20*time.Second, 50*time.Millisecond, "peer %d's roster never reached %d", i, peers)
	}

	// Each peer says something identifiable, so a crossed stream is visible
	// rather than merely a count being right.
	want := map[string]string{}
	for i, c := range clients {
		payload := fmt.Sprintf("peer-%d-speaking", i)
		want[c.Self().Pubkey] = payload
		speak(t, c, uint16(i+1), []byte(payload))
	}

	for i, c := range clients {
		heard := map[string]string{}
		for len(heard) < peers-1 {
			f := nextFrame(t, c, 20*time.Second)
			require.True(t, f.Attributed, "peer %d got an unattributed frame", i)
			require.NotEqual(t, c.Self().Pubkey, f.Author.Pubkey, "peer %d heard itself", i)
			heard[f.Author.Pubkey] = string(f.Opus)
		}
		for pubkey, payload := range heard {
			require.Equal(t, want[pubkey], payload,
				"peer %d received the wrong payload for author %s", i, pubkey)
		}
	}
}

func testHuddleJoinMidCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	alice := dialHuddlePeer(t, ctx, "join-mid", 20, 0)
	bob := dialHuddlePeer(t, ctx, "join-mid", 21, 0)
	require.Eventually(t, func() bool { return len(alice.Roster()) == 2 },
		20*time.Second, 50*time.Millisecond, "alice never saw bob")

	carol := dialHuddlePeer(t, ctx, "join-mid", 22, 0)

	// Carol's own joined message already carries the people who were there.
	require.Eventually(t, func() bool { return len(carol.Roster()) == 3 },
		20*time.Second, 50*time.Millisecond, "carol's roster never showed the call in progress")
	// And the incumbents learn about her.
	require.Eventually(t, func() bool { return len(alice.Roster()) == 3 },
		20*time.Second, 50*time.Millisecond, "alice never saw carol join")

	// Carol receives audio sent after she arrived.
	speak(t, alice, 1, []byte("after-carol-joined"))
	got := nextFrame(t, carol, 15*time.Second)
	require.Equal(t, []byte("after-carol-joined"), got.Opus)
	require.Equal(t, alice.Self().Pubkey, got.Author.Pubkey)
	_ = bob
}

func testHuddleLeaveRemovesThePeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	alice := dialHuddlePeer(t, ctx, "leave-mid", 30, 0)
	bob := dialHuddlePeer(t, ctx, "leave-mid", 31, 0)
	require.Eventually(t, func() bool { return len(alice.Roster()) == 2 },
		20*time.Second, 50*time.Millisecond, "alice never saw bob")

	bobPubkey := bob.Self().Pubkey
	require.NoError(t, bob.Close())

	require.Eventually(t, func() bool { return len(alice.Roster()) == 1 },
		20*time.Second, 50*time.Millisecond, "bob's roster entry outlived his session")
	for _, p := range alice.Roster() {
		require.NotEqual(t, bobPubkey, p.Pubkey, "bob is still listed after leaving")
	}

	// The call continues for whoever is left: a third peer joins and is heard.
	carol := dialHuddlePeer(t, ctx, "leave-mid", 32, 0)
	speak(t, carol, 1, []byte("after-bob-left"))
	got := nextFrame(t, alice, 15*time.Second)
	require.Equal(t, []byte("after-bob-left"), got.Opus)
}

// testHuddleSimultaneousSpeech is the talk-over case: everyone sends at once for
// a sustained stretch. The assertion is deliberately not "no frames dropped" --
// the per-peer queue drops when full by design -- but that every peer keeps
// hearing every other one, and that no sender is stalled by a slow consumer.
func testHuddleSimultaneousSpeech(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const peers = 4
	clients := make([]*huddleclient.Client, peers)
	for i := 0; i < peers; i++ {
		clients[i] = dialHuddlePeer(t, ctx, "talk-over", 40+i, 0)
	}
	for i, c := range clients {
		require.Eventually(t, func() bool { return len(c.Roster()) == peers },
			20*time.Second, 50*time.Millisecond, "peer %d's roster never settled", i)
	}

	// Drain concurrently while everyone talks, so a full queue cannot be
	// mistaken for a delivery failure.
	heard := make([]map[string]int, peers)
	var mu sync.Mutex
	var readers sync.WaitGroup
	stop := make(chan struct{})
	for i := range clients {
		heard[i] = map[string]int{}
		readers.Add(1)
		go func(i int) {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				case f, ok := <-clients[i].Frames():
					if !ok {
						return
					}
					if f.Attributed {
						mu.Lock()
						heard[i][f.Author.Pubkey]++
						mu.Unlock()
					}
				}
			}
		}(i)
	}

	var senders sync.WaitGroup
	sendStart := time.Now()
	for i, c := range clients {
		senders.Add(1)
		go func(i int, c *huddleclient.Client) {
			defer senders.Done()
			for seq := 1; seq <= 50; seq++ { // 50 frames = 1s of speech
				speak(t, c, uint16(seq), []byte(fmt.Sprintf("p%d", i)))
				time.Sleep(20 * time.Millisecond)
			}
		}(i, c)
	}
	senders.Wait()
	sendElapsed := time.Since(sendStart)

	// 50 frames at 20 ms is ~1s of wall time. A sender blocked behind a slow
	// consumer would take far longer; this is the "never queues, drops instead"
	// guarantee observed from the sending side.
	require.Less(t, sendElapsed, 10*time.Second,
		"senders were stalled: %v for ~1s of speech", sendElapsed)

	time.Sleep(2 * time.Second) // let the tail arrive
	close(stop)
	readers.Wait()

	mu.Lock()
	defer mu.Unlock()
	for i := range clients {
		require.Len(t, heard[i], peers-1,
			"peer %d heard %d of %d others: %v", i, len(heard[i]), peers-1, heard[i])
		require.NotContains(t, heard[i], clients[i].Self().Pubkey, "peer %d heard itself", i)
	}
}

func testHuddleDTXAndLevelSurvive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	alice := dialHuddlePeer(t, ctx, "dtx", 50, 0)
	bob := dialHuddlePeer(t, ctx, "dtx", 51, 0)

	// A comfort-noise frame at the silence floor, with a reserved flag bit set
	// alongside DTX: the relay must forward the flags byte untouched, not
	// normalize it.
	const reservedBit uint8 = 0x40
	require.NoError(t, alice.Send(wire.FrameHeader{
		Seq:       3,
		Ts48k:     3 * wire.SamplesPerFrame,
		LevelDbov: wire.LevelSilenceFloor,
		Flags:     wire.FlagDTX | reservedBit,
	}, []byte("comfort-noise")))

	got := nextFrame(t, bob, 15*time.Second)
	require.Equal(t, []byte("comfort-noise"), got.Opus)
	require.True(t, got.Header.IsDTX(), "the DTX flag must survive the relay")
	require.Equal(t, wire.FlagDTX|reservedBit, got.Header.Flags,
		"reserved flag bits must be forwarded untouched")
	require.Equal(t, wire.LevelSilenceFloor, got.Header.LevelDbov)
	require.False(t, got.Speaking(huddleclient.DefaultSpeakingThreshold),
		"a silence-floor frame must not read as speech")
}

// testHuddleRoomFullRefusesBeyondCapacity fills a room to room.MaxPeers and
// checks the next joiner is refused with a code it can act on, while the
// existing call is unaffected.
func testHuddleRoomFullRefusesBeyondCapacity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	clients := make([]*huddleclient.Client, 0, room.MaxPeers)
	for i := 0; i < room.MaxPeers; i++ {
		clients = append(clients, dialHuddlePeer(t, ctx, "full", 100+i, 0))
	}

	_, err := huddleclient.Dial(ctx, huddleclient.Config{
		Endpoint: huddleEndpointFor("full"),
		RelayURL: huddleIntegrationRelayURL,
		PrivKey:  huddlePeerKey(100 + room.MaxPeers),
	})
	require.Error(t, err, "the %dth peer should have been refused", room.MaxPeers+1)
	var refused *huddleclient.RefusedError
	require.ErrorAs(t, err, &refused)
	require.Equal(t, "room_full", refused.Code)

	// The full room still works.
	speak(t, clients[0], 1, []byte("still-talking"))
	got := nextFrame(t, clients[1], 20*time.Second)
	require.Equal(t, []byte("still-talking"), got.Opus)
}

// testHuddleAuthForAnotherRelayIsRejected: the NIP-42 event's relay tag names
// the relay, so an event minted for a different one must not admit its bearer
// here -- otherwise a challenge captured elsewhere would be replayable.
func testHuddleAuthForAnotherRelayIsRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err := huddleclient.Dial(ctx, huddleclient.Config{
		Endpoint: huddleEndpointFor("wrong-relay"),
		RelayURL: "ws://not-this-relay.example",
		PrivKey:  huddlePeerKey(200),
	})
	require.Error(t, err, "an auth event naming another relay must be rejected")

	// Refused with a code, not dropped: a client needs to be able to tell "your
	// identity was not accepted" from a network failure.
	var refused *huddleclient.RefusedError
	require.ErrorAs(t, err, &refused)
	require.Equal(t, "auth_failed", refused.Code)
}

// testHuddleVersionMismatchRequiresUpgrade: a room pins its first peer's
// protocol version, and a later peer on a different one is told so explicitly
// rather than silently mis-parsing the routing prefix, whose shape differs
// between v2 and v3.
func testHuddleVersionMismatchRequiresUpgrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pinned := dialHuddlePeer(t, ctx, "version", 210, wire.CurrentProtocolVersion)
	require.NotEmpty(t, pinned.Self().Pubkey)

	_, err := huddleclient.Dial(ctx, huddleclient.Config{
		Endpoint:        huddleEndpointFor("version"),
		RelayURL:        huddleIntegrationRelayURL,
		PrivKey:         huddlePeerKey(211),
		ProtocolVersion: wire.CurrentProtocolVersion - 1,
	})
	require.Error(t, err, "a peer on a different version must be refused")
	var refused *huddleclient.RefusedError
	require.ErrorAs(t, err, &refused)
	require.Equal(t, "upgrade_required", refused.Code)
	require.NotNil(t, refused.CurrentVersion, "the refusal should name the room's version")
	require.Equal(t, uint8(wire.CurrentProtocolVersion), *refused.CurrentVersion)
}
