package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/ncli/cli/huddle"
	"github.com/ohstr/ncli/huddleclient"
	"github.com/ohstr/nmilat/huddle/wire"
	"github.com/stretchr/testify/require"
)

// dialHuddle joins roomID on ts as privKey, through the production client and
// the production endpoint builder -- so this exercises the URL shape the `ncli
// huddle join` command actually produces, not a hand-written copy of it.
func dialHuddle(t *testing.T, ctx context.Context, ts *httptest.Server, roomID, privKey string) *huddleclient.Client {
	t.Helper()

	base, err := url.Parse("ws" + strings.TrimPrefix(ts.URL, "http"))
	require.NoError(t, err)

	endpoint, err := huddle.Endpoint(base, roomID)
	require.NoError(t, err)

	client, err := huddleclient.Dial(ctx, huddleclient.Config{
		Endpoint: endpoint,
		RelayURL: huddleRelayURL,
		PrivKey:  privKey,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestHuddleBoardShowsWhoIsTalkingThroughARealRelay is the end-to-end check for
// the TUI's data path with nothing stubbed below it: a real relay, two real
// clients over real WebSockets, and the real Board consuming the frames. The
// board is built with a nil app so it renders synchronously and needs no
// terminal, which is what makes this runnable in CI at all.
func TestHuddleBoardShowsWhoIsTalkingThroughARealRelay(t *testing.T) {
	ts := bootRelay(t, &HuddleConfig{Enabled: true}, huddleRelayURL)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Bob is the viewer: his board is the thing under test.
	bob := dialHuddle(t, ctx, ts, "board-room", bobPriv)
	alice := dialHuddle(t, ctx, ts, "board-room", alicePriv)

	board := huddle.NewBoard(nil, bob, "board-room")
	runCtx, stopBoard := context.WithCancel(ctx)
	defer stopBoard()
	go board.Run(runCtx)

	alicePub := alice.Self().Pubkey
	require.NotEmpty(t, alicePub)

	rowFor := func(pubkey string) (huddle.Participant, bool) {
		for _, p := range board.Participants() {
			if p.Pubkey == pubkey {
				return p, true
			}
		}
		return huddle.Participant{}, false
	}

	// Alice appears on Bob's roster once the relay tells him she joined.
	require.Eventually(t, func() bool {
		_, ok := rowFor(alicePub)
		return ok
	}, 10*time.Second, 20*time.Millisecond, "Alice never appeared on Bob's roster")

	// Both of them, and no one else -- a board that invented rows would pass the
	// check above.
	require.Len(t, board.Participants(), 2)
	self, ok := rowFor(bob.Self().Pubkey)
	require.True(t, ok, "Bob is missing from his own roster")
	require.True(t, self.Self, "Bob's own row should be marked as self")

	// Alice talks: 20 ms frames at a speaking level, the same cadence a real
	// client sends at.
	talking, stopTalking := context.WithCancel(ctx)
	defer stopTalking()
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		var seq uint16
		for {
			select {
			case <-talking.Done():
				return
			case <-ticker.C:
				seq++
				_ = alice.Send(wire.FrameHeader{
					Seq:       seq,
					Ts48k:     uint32(seq) * 960,
					LevelDbov: -20,
				}, []byte("opus-payload"))
			}
		}
	}()

	require.Eventually(t, func() bool {
		row, ok := rowFor(alicePub)
		return ok && row.Speaking
	}, 10*time.Second, 20*time.Millisecond, "Alice never showed as speaking on Bob's board")

	row, _ := rowFor(alicePub)
	require.True(t, row.Heard, "audio arrived, so Heard must be set")
	require.Equal(t, int8(-20), row.Level, "the level must be the one Alice sent")

	// Bob is not sent his own audio, so his own row stays silent throughout --
	// the no-self-echo guarantee, observed from the UI's side.
	self, _ = rowFor(bob.Self().Pubkey)
	require.False(t, self.Speaking, "Bob must not hear himself")
	require.False(t, self.Heard, "Bob must not receive his own frames")

	// Alice goes quiet. Nothing is sent to say so, so only the board's own
	// refresh tick can clear her indicator.
	stopTalking()
	require.Eventually(t, func() bool {
		row, ok := rowFor(alicePub)
		return ok && !row.Speaking
	}, 10*time.Second, 20*time.Millisecond, "Alice stayed lit after going quiet")

	// She is still present and still remembered as having been heard -- going
	// quiet is not leaving.
	row, ok = rowFor(alicePub)
	require.True(t, ok, "a quiet peer must stay on the roster")
	require.True(t, row.Heard)

	// Alice leaves: her row goes away.
	require.NoError(t, alice.Close())
	require.Eventually(t, func() bool {
		_, ok := rowFor(alicePub)
		return !ok
	}, 10*time.Second, 20*time.Millisecond, "Alice's row outlived her session")
	require.Len(t, board.Participants(), 1, "only Bob should remain")
}

// TestHuddleBoardReportsARefusedRoom pins the operator-facing half: a relay with
// huddles disabled must produce a refusal the command can explain, not a bare
// dial error.
func TestHuddleBoardReportsARefusedRoom(t *testing.T) {
	ts := bootRelay(t, nil, huddleRelayURL)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	base, err := url.Parse("ws" + strings.TrimPrefix(ts.URL, "http"))
	require.NoError(t, err)
	endpoint, err := huddle.Endpoint(base, "board-room")
	require.NoError(t, err)

	_, err = huddleclient.Dial(ctx, huddleclient.Config{
		Endpoint: endpoint,
		RelayURL: huddleRelayURL,
		PrivKey:  alicePriv,
	})
	require.Error(t, err, "a relay with no huddle block must not admit a peer")

	// The endpoint is not mounted at all, so this is a 404 rather than a
	// protocol-level refusal. `ncli huddle join` keys its "this relay has no
	// huddles" message off exactly this, so the status has to survive the dial.
	var notUpgraded *huddleclient.DialError
	require.ErrorAs(t, err, &notUpgraded)
	require.Equal(t, http.StatusNotFound, notUpgraded.Status,
		"a relay with huddles off must 404 the upgrade, not fail some other way")
}
