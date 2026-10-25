package relay

import (
	"bytes"
	stdlog "log"
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestCheckMaxStatsTargets(t *testing.T) {
	cfg := func(maxTargets int, maxMessage int64) *RelayConfig {
		c := &RelayConfig{Cache: &CacheConfig{Profiles: &ProfilesConfig{Stats: &ProfileStatsConfig{Enabled: true, MaxTargets: maxTargets}}}}
		c.Nip11.Limitation.MaxMessageLength = maxMessage
		return c
	}
	// 256 KiB holds about 3,900 #d pubkeys.
	if err := checkMaxStatsTargets(cfg(3900, 256<<10)); err != nil {
		t.Errorf("3900 at 256 KiB: %v", err)
	}
	if err := checkMaxStatsTargets(cfg(4000, 256<<10)); err == nil || !strings.Contains(err.Error(), "at most 3912") {
		t.Errorf("4000 at 256 KiB = %v, want the ceiling named", err)
	}
	if err := checkMaxStatsTargets(cfg(-1, 256<<10)); err == nil {
		t.Error("negative maxTargets accepted")
	}
	if err := checkMaxStatsTargets(&RelayConfig{}); err != nil {
		t.Errorf("no cache block: %v", err)
	}
}

func TestStdLogGoesToDebug(t *testing.T) {
	t.Cleanup(func() { stdlog.SetOutput(os.Stderr); stdlog.SetFlags(stdlog.LstdFlags) })
	var buf bytes.Buffer
	redirectStdLog(zerolog.New(&buf).Level(zerolog.InfoLevel))
	stdlog.Print("protocol error: received DATA on a HEAD request")
	if buf.Len() != 0 {
		t.Errorf("logged above debug: %s", buf.String())
	}

	buf.Reset()
	redirectStdLog(zerolog.New(&buf).Level(zerolog.DebugLevel))
	stdlog.Print("protocol error: received DATA on a HEAD request")
	if !strings.Contains(buf.String(), `"level":"debug"`) || !strings.Contains(buf.String(), "received DATA") {
		t.Errorf("debug line = %s", buf.String())
	}
}

func TestCheckSearchBatching(t *testing.T) {
	cfg := func(batch, inFlight int) *RelayConfig {
		return &RelayConfig{Cache: &CacheConfig{Profiles: &ProfilesConfig{Search: &SearchConfig{Enabled: true, BatchSize: batch, MaxInFlight: inFlight}}}}
	}
	for _, ok := range [][2]int{{0, 0}, {1, 1}, {10_000, 4}, {maxSearchBatch, maxSearchInFlight}} {
		if err := checkSearchBatching(cfg(ok[0], ok[1])); err != nil {
			t.Errorf("batchSize %d maxInFlight %d: %v", ok[0], ok[1], err)
		}
	}
	for _, bad := range [][2]int{{-1, 0}, {maxSearchBatch + 1, 0}, {0, -1}, {0, maxSearchInFlight + 1}} {
		if err := checkSearchBatching(cfg(bad[0], bad[1])); err == nil {
			t.Errorf("batchSize %d maxInFlight %d passed", bad[0], bad[1])
		}
	}
	if err := checkSearchBatching(&RelayConfig{}); err != nil {
		t.Errorf("no search: %v", err)
	}
}

func TestSearchConfig_MeiliOptions(t *testing.T) {
	off := false
	on := true
	if n := len((&SearchConfig{SearchAbout: &off}).meiliOptions()); n != 1 {
		t.Errorf("searchAbout false: %d options, want 1", n)
	}
	for _, s := range []*SearchConfig{nil, {}, {SearchAbout: &on}} {
		if n := len(s.meiliOptions()); n != 0 {
			t.Errorf("%+v: %d options, want 0", s, n)
		}
	}
}
