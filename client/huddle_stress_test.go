package client

import (
	"context"
	"encoding/binary"
	"os/exec"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohstr/ncli/huddle/client"
	"github.com/ohstr/nmilat/huddle/room"
	"github.com/ohstr/nmilat/huddle/wire"
	"github.com/stretchr/testify/require"
)

// See integration/huddle/README.md's "Stress stack" section. Its own compose
// project and port so it can coexist with the integration stack. Not run by
// default CI -- `just test-integration-huddle-stress`.
const (
	huddleStressComposeFile = "../integration/huddle/stress-compose.yaml"
	huddleStressRelayURL    = "ws://localhost:21540"

	// huddleStressPeers fills the room to capacity. Occupancy is the variable
	// that matters: fan-out is heaviest at MaxPeers.
	huddleStressPeers = room.MaxPeers

	huddleSoakDuration  = 60 * time.Second
	huddleFrameInterval = 20 * time.Millisecond // 50 frames/s, the real cadence

	// An 8-byte send stamp plus 160 bytes, about a 20 ms Opus frame at 64 kbps.
	huddleStressPayloadBytes = 8 + 160
)

// stampFrame writes the send time into the payload. The relay treats the payload
// as opaque, so this rides through untouched -- and because every peer here runs
// in this one process, the reader's clock is the sender's clock, which makes the
// difference a true mouth-to-ear measurement with no clock sync to get wrong.
func stampFrame(buf []byte, at time.Time) {
	binary.BigEndian.PutUint64(buf, uint64(at.UnixNano()))
}

func frameLatency(payload []byte, now time.Time) (time.Duration, bool) {
	if len(payload) < 8 {
		return 0, false
	}
	return now.Sub(time.Unix(0, int64(binary.BigEndian.Uint64(payload)))), true
}

func dialHuddleStressPeer(t *testing.T, ctx context.Context, roomID string, i int) *client.Client {
	t.Helper()
	c, err := client.Dial(ctx, client.Config{
		Endpoint: huddleStressRelayURL + "/huddle/" + roomID + "/audio",
		RelayURL: huddleStressRelayURL,
		PrivKey:  huddlePeerKey(i),
		// A deeper client-side buffer than the default: this reader has to keep
		// up with 24 speakers, and a shallow channel here would measure the
		// test's own backlog rather than the relay's latency.
		FrameBuffer: 4096,
	})
	require.NoError(t, err, "stress peer %d could not join", i)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p / 100 * float64(len(sorted)-1))
	return sorted[idx]
}

// TestHuddleStress is the latency soak: a full room, real frame cadence, for a
// sustained stretch, measuring what a participant would actually hear.
//
// A benchmark cannot answer this. It runs unthrottled, so it measures throughput
// under overload; a call runs at exactly 50 frames a second per speaker, and what
// matters there is the latency distribution and whether it drifts upward over a
// minute. Needs Docker.
//
// Observed on first run (in-container loopback, AMD EPYC-Genoa):
//
//	2 of 25 speaking: 143952/143952 delivered (100%), p50 526us p95 747us
//	                  p99 938us max 5.4ms, drift 531us -> 512us
//	25 of 25 speaking: 65.5% delivered, p50 976us p95 1.79ms p99 2.45ms
//	                  max 7.2ms, drift 1.019ms -> 1.012ms
//
// Both cases paced exactly (senders finished within 1ms of the 60s target), so
// the queue sheds load rather than blocking, and neither drifts upward over the
// minute. Against a ~150 ms mouth-to-ear budget the relay's own contribution is
// well under 1% in the realistic case.
//
// Note what this does and does not say: peers here talk to the relay over
// loopback, so these are the relay's *own* latency and shedding behaviour, not
// end-to-end call latency. Real calls add network RTT and a client jitter buffer,
// both of which dominate these numbers. The value of measuring it this way is
// that it isolates the part this repo controls.
func TestHuddleStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker-based huddle stress test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found on PATH, skipping huddle stress test")
	}

	runCompose(t, huddleStressComposeFile, "up", "-d", "--build")
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "-f", huddleStressComposeFile, "down", "-v")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("docker compose down failed: %v\n%s", err, out)
		}
	})

	waitForRelayReady(t, huddleStressRelayURL, 60*time.Second)

	// Two active speakers in a room of 25 is what a real meeting looks like, and
	// is the case whose latency a participant actually experiences.
	t.Run("RealisticTwoSpeakers", func(t *testing.T) {
		testHuddleLatencySoak(t, "soak-realistic", 2, 95.0)
	})

	// Everyone talking at once is the pathological case. Delivery is allowed to
	// suffer -- the per-peer queue drops when full by design -- but latency must
	// not run away and senders must not stall.
	t.Run("PathologicalEveryoneSpeaks", func(t *testing.T) {
		testHuddleLatencySoak(t, "soak-pathological", huddleStressPeers, 0)
	})
}

// testHuddleLatencySoak runs a full room for huddleSoakDuration with `speakers`
// of them talking, then reports the latency distribution. minDelivery is the
// percentage of expected frames that must arrive, or 0 to only report it.
func testHuddleLatencySoak(t *testing.T, roomID string, speakers int, minDelivery float64) {
	ctx, cancel := context.WithTimeout(context.Background(), huddleSoakDuration+3*time.Minute)
	defer cancel()

	goroutinesBefore := runtime.NumGoroutine()

	clients := make([]*client.Client, huddleStressPeers)
	for i := range clients {
		clients[i] = dialHuddleStressPeer(t, ctx, roomID, i)
	}
	for i, c := range clients {
		require.Eventually(t, func() bool { return len(c.Roster()) == huddleStressPeers },
			60*time.Second, 100*time.Millisecond,
			"peer %d's roster never reached %d", i, huddleStressPeers)
	}

	type sample struct {
		at      time.Time
		latency time.Duration
	}
	// One slice per reader, appended only by that reader's own goroutine, read
	// only after they have all stopped -- so no lock is needed on the hot path,
	// which matters because a lock here would inflate the very thing being
	// measured.
	samples := make([][]sample, huddleStressPeers)
	var received, unattributed, malformed atomic.Int64

	readerStop := make(chan struct{})
	var readers sync.WaitGroup
	for i := range clients {
		samples[i] = make([]sample, 0, 1<<16)
		readers.Add(1)
		go func(i int) {
			defer readers.Done()
			for {
				select {
				case <-readerStop:
					return
				case f, ok := <-clients[i].Frames():
					if !ok {
						return
					}
					now := time.Now()
					received.Add(1)
					if !f.Attributed {
						unattributed.Add(1)
					}
					if d, ok := frameLatency(f.Opus, now); ok {
						samples[i] = append(samples[i], sample{at: now, latency: d})
					} else {
						malformed.Add(1)
					}
				}
			}
		}(i)
	}

	var sent atomic.Int64
	var senders sync.WaitGroup
	sendStart := time.Now()
	deadline := sendStart.Add(huddleSoakDuration)

	for i := 0; i < speakers; i++ {
		senders.Add(1)
		go func(i int) {
			defer senders.Done()
			buf := make([]byte, huddleStressPayloadBytes)
			ticker := time.NewTicker(huddleFrameInterval)
			defer ticker.Stop()
			var seq uint16
			for range ticker.C {
				if time.Now().After(deadline) {
					return
				}
				seq++
				stampFrame(buf, time.Now())
				err := clients[i].Send(wire.FrameHeader{
					Seq:       seq,
					Ts48k:     uint32(seq) * wire.SamplesPerFrame,
					LevelDbov: -20,
				}, buf)
				if err != nil {
					t.Errorf("peer %d send failed mid-soak: %v", i, err)
					return
				}
				sent.Add(1)
			}
		}(i)
	}
	senders.Wait()
	sendElapsed := time.Since(sendStart)

	// Let the tail arrive before stopping the readers, or the last frames in
	// flight would count as losses.
	time.Sleep(3 * time.Second)
	close(readerStop)
	readers.Wait()

	// --- results ---

	all := make([]time.Duration, 0, received.Load())
	var first, last []time.Duration
	firstCut := sendStart.Add(huddleSoakDuration / 10)
	lastCut := sendStart.Add(huddleSoakDuration - huddleSoakDuration/10)
	for _, per := range samples {
		for _, s := range per {
			all = append(all, s.latency)
			switch {
			case s.at.Before(firstCut):
				first = append(first, s.latency)
			case s.at.After(lastCut):
				last = append(last, s.latency)
			}
		}
	}
	require.NotEmpty(t, all, "no latency samples collected at all")
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	sort.Slice(first, func(i, j int) bool { return first[i] < first[j] })
	sort.Slice(last, func(i, j int) bool { return last[i] < last[j] })

	expected := sent.Load() * int64(huddleStressPeers-1)
	delivery := float64(received.Load()) / float64(expected) * 100

	t.Logf("soak: %d peers, %d speaking, %v", huddleStressPeers, speakers, huddleSoakDuration)
	t.Logf("  sent=%d expected-receives=%d received=%d delivery=%.2f%%",
		sent.Load(), expected, received.Load(), delivery)
	t.Logf("  latency p50=%v p95=%v p99=%v max=%v (n=%d)",
		percentile(all, 50), percentile(all, 95), percentile(all, 99), all[len(all)-1], len(all))
	t.Logf("  drift: first-tenth p50=%v -> last-tenth p50=%v", percentile(first, 50), percentile(last, 50))
	t.Logf("  unattributed=%d malformed=%d", unattributed.Load(), malformed.Load())
	t.Logf("  senders finished in %v (target %v)", sendElapsed, huddleSoakDuration)

	// --- assertions ---
	//
	// Deliberately loose. There is no historical baseline to compare against, so
	// these catch catastrophe and regression in shape, not absolute performance.
	// The logged numbers above are the real output of this test.

	require.Zero(t, malformed.Load(), "a frame payload was corrupted in transit")
	require.Zero(t, unattributed.Load(), "a frame arrived that could not be attributed")

	// Senders pace themselves with a ticker, so overshooting the target means
	// something blocked them -- the queue is supposed to drop, never block.
	require.Less(t, sendElapsed, huddleSoakDuration+20*time.Second,
		"senders were stalled: %v for a %v soak", sendElapsed, huddleSoakDuration)

	if minDelivery > 0 {
		require.GreaterOrEqual(t, delivery, minDelivery,
			"only %.2f%% of expected frames arrived", delivery)
	}

	// A ceiling that a working relay cannot plausibly hit. Anything near this is
	// a real problem regardless of what the baseline turns out to be.
	require.Less(t, percentile(all, 99), 2*time.Second, "p99 latency is implausible")

	// Unbounded queue growth shows up as latency climbing through the soak. A
	// steady call's last tenth should look like its first.
	if len(first) > 0 && len(last) > 0 {
		drift := percentile(last, 50)
		budget := 3*percentile(first, 50) + 50*time.Millisecond
		require.Less(t, drift, budget,
			"latency drifted upward: first-tenth p50=%v, last-tenth p50=%v",
			percentile(first, 50), drift)
	}

	// Goroutine leak: close every client, then let their read loops exit.
	for _, c := range clients {
		_ = c.Close()
	}
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= goroutinesBefore+huddleStressPeers
	}, 30*time.Second, 200*time.Millisecond,
		"goroutines did not settle after closing: before=%d now=%d",
		goroutinesBefore, runtime.NumGoroutine())
}
