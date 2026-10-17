package client

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The control plane over the /rtc door, against the relay container: roster
// updates, media state and leave reasons. These are what let a client tell a
// stopped camera from a lost one, and a hang-up from a dropped connection,
// without guessing from silence.

// controlWithin is how long a control message may take to cross the relay. It
// is a fan-out of one small JSON frame, so anything near a second is a bug.
const controlWithin = 3 * time.Second

// waitControl returns the first control message matching want, failing the
// test if none arrives in time. Non-matching messages are skipped.
func (p *rtcPeer) waitControl(what string, want func(rtcSignal) bool) rtcSignal {
	p.t.Helper()
	deadline := time.After(controlWithin)
	for {
		select {
		case m := <-p.controls:
			if want(m) {
				return m
			}
		case <-deadline:
			p.t.Fatalf("peer %d: no %s within %s", p.index, what, controlWithin)
			return rtcSignal{}
		}
	}
}

func (p *rtcPeer) announce(kind mediaKind, state string) {
	p.send(map[string]any{"type": "media", "kind": string(kind), "state": state, "track_id": string(kind), "stream_id": "stream-" + string(kind)})
}

func (p *rtcPeer) cameraPackets() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, r := range p.inbound {
		if r.kind == mediaCamera {
			n++
		}
	}
	return n
}

// testHuddleStoppedCameraIsAnnounced is the bug this exists for: a browser
// stopping its camera only detaches the track, so to everyone else the picture
// just stops. The publisher says so, and the others hear it at once.
func testHuddleStoppedCameraIsAnnounced(t *testing.T) {
	const roomID = "it-rtc-camera-off"
	bob := dialRTCPeer(t, roomID, 1, mediaAudio)
	alice := dialRTCPeer(t, roomID, 0, mediaAudio, mediaCamera)
	require.True(t, slices.Contains(alice.joined.Features, "media"), "relay does not advertise media: %v", alice.joined.Features)
	bob.waitControl("alice's joined", func(m rtcSignal) bool { return m.Type == "joined" && m.Pubkey == alice.pubkey })

	frame := randomPayload(t)
	stopped := make(chan struct{})
	go func() {
		seq := uint16(1)
		for {
			select {
			case <-stopped:
				return
			case <-time.After(20 * time.Millisecond):
			}
			alice.writeMedia(mediaCamera, seq, frame)
			seq++
		}
	}()
	alice.announce(mediaCamera, "live")
	eventually(t, "bob to see alice's camera", func() bool { return bob.sawPayload(mediaCamera, frame) })
	live := bob.waitControl("camera live", func(m rtcSignal) bool { return m.Type == "media" && m.Kind == "camera" })
	require.Equal(t, "live", live.State)

	// Stop the camera the way a browser does: packets cease, and the client says so.
	close(stopped)
	alice.announce(mediaCamera, "off")
	off := bob.waitControl("camera off", func(m rtcSignal) bool { return m.Type == "media" && m.Kind == "camera" })
	require.Equal(t, alice.pubkey, off.Pubkey)
	require.Equal(t, "off", off.State)
	require.Greater(t, off.Seq, live.Seq, "a later change must carry a later seq")

	// And the picture really has stopped -- nothing still in flight keeps arriving.
	time.Sleep(300 * time.Millisecond)
	settled := bob.cameraPackets()
	time.Sleep(time.Second)
	require.Equal(t, settled, bob.cameraPackets(), "camera packets kept arriving after the camera was stopped")
}

// testHuddleMicStateRoundTrip: mute is announced like any other media, so the
// others' mute icon is as current as the relay, not as the next presence event.
func testHuddleMicStateRoundTrip(t *testing.T) {
	const roomID = "it-rtc-mic-state"
	bob := dialRTCPeer(t, roomID, 1, mediaAudio)
	alice := dialRTCPeer(t, roomID, 0, mediaAudio)

	alice.announce(mediaAudio, "off")
	muted := bob.waitControl("mic off", func(m rtcSignal) bool { return m.Type == "media" && m.Kind == "mic" })
	require.Equal(t, "off", muted.State)
	alice.announce(mediaAudio, "live")
	unmuted := bob.waitControl("mic live", func(m rtcSignal) bool { return m.Type == "media" && m.Kind == "mic" })
	require.Equal(t, "live", unmuted.State)
	require.Greater(t, unmuted.Seq, muted.Seq)
}

// testHuddleLeaveVersusDrop: a hang-up and a lost connection are different
// events and are now reported as such.
func testHuddleLeaveVersusDrop(t *testing.T) {
	const roomID = "it-rtc-leave-reason"
	alice := dialRTCPeer(t, roomID, 0, mediaAudio)
	bob := dialRTCPeer(t, roomID, 1, mediaAudio)
	carol := dialRTCPeer(t, roomID, 2, mediaAudio)

	bob.send(map[string]any{"type": "leave"})
	left := alice.waitControl("bob's left", func(m rtcSignal) bool { return m.Type == "left" && m.Pubkey == bob.pubkey })
	require.Equal(t, "leave", left.Reason)

	_ = carol.conn.Close()
	dropped := alice.waitControl("carol's left", func(m rtcSignal) bool { return m.Type == "left" && m.Pubkey == carol.pubkey })
	require.Equal(t, "dropped", dropped.Reason)
}

// testHuddleLateJoinerSeesMediaState: someone arriving mid-call learns whose
// camera is off and who is sharing from the roster, not from the next change.
func testHuddleLateJoinerSeesMediaState(t *testing.T) {
	const roomID = "it-rtc-media-snapshot"
	bob := dialRTCPeer(t, roomID, 1, mediaAudio)
	alice := dialRTCPeer(t, roomID, 0, mediaAudio, mediaCamera, mediaScreen)

	alice.announce(mediaCamera, "off")
	alice.announce(mediaScreen, "live")
	bob.waitControl("camera off", func(m rtcSignal) bool { return m.Type == "media" && m.Kind == "camera" })
	bob.waitControl("screen live", func(m rtcSignal) bool { return m.Type == "media" && m.Kind == "screen" })

	carol := dialRTCPeer(t, roomID, 2, mediaAudio)
	var media []rtcMedia
	for _, p := range carol.joined.Peers {
		if p.Pubkey == alice.pubkey {
			media = p.Media
		}
	}
	require.Equal(t, []rtcMedia{
		{Kind: "camera", State: "off", TrackID: "camera", Seq: 1},
		{Kind: "screen", State: "live", TrackID: "screen", Seq: 2},
	}, media)
}
