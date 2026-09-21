package tui

import (
	"testing"
	"time"

	"github.com/ohstr/nmilat/utils"
	"github.com/rs/zerolog/log"
)

func init() {
	utils.InitLogger()
}

func TestModelHeaders(t *testing.T) {

	sm := NewInboundMetrics(1, "/data/db/notes.db", func() {})
	headers := sm.Columns()
	row := sm.Row()
	log.Debug().Msgf("headers=%v", headers)
	log.Debug().Msgf("row=%v", row)
}

func TestModelUp(t *testing.T) {
	sum := NewInboundMetrics(1, "/data/db/notes.db", func() {})

	headers := sum.Columns()
	row := sum.Row()

	log.Debug().Msgf("headers=%v", headers)
	log.Debug().Msgf("row=%v", row)
	log.Debug().Msgf("check: %d==%d", len(headers), len(row))
}

func TestFormatAge(t *testing.T) {
	const (
		m = time.Minute
		h = time.Hour
		d = 24 * time.Hour
		w = 7 * d
	)
	tests := []struct {
		in   time.Duration
		want string
	}{
		{-5 * time.Second, "0s"},
		{0, "0s"},
		{999 * time.Millisecond, "0s"},
		{45 * time.Second, "45s"},
		{m, "1m"},
		{5*m + 12*time.Second, "5m12s"},
		{59*m + 59*time.Second, "59m59s"},
		{h, "1h"},
		{h + 5*time.Second, "1h"},
		{3*h + 27*m + 9*time.Second, "3h27m"},
		{23*h + 59*m, "23h59m"},
		{d, "1d"},
		{2*d + 4*h + 30*m, "2d4h"},
		{6*d + 23*h, "6d23h"},
		{w, "1w"},
		{w + 12*h, "1w"},
		{10*d + m, "1w3d"},
		{2 * w, "2w"},
		{15 * d, "2w1d"},
		{52 * w, "52w"},
	}
	for _, tt := range tests {
		if got := formatAge(tt.in); got != tt.want {
			t.Errorf("formatAge(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRowAgeCellUsesDaysAndWeeks(t *testing.T) {
	fm := NewInboundMetrics(1, "/data/db/notes.db", func() {})
	fm.createdAt = time.Now().Add(-(10*24*time.Hour + time.Minute))

	row := fm.Row()
	if got, want := row[len(row)-1], "1w3d"; got != want {
		t.Errorf("Age cell = %q, want %q", got, want)
	}
}

func TestModelDown(t *testing.T) {
	sdm := NewOutboundMetrics(1, "/data/db/notes.db", func() {})

	headers := sdm.Columns()
	row := sdm.Row()

	log.Debug().Msgf("headers=%v", headers)
	log.Debug().Msgf("row=%v", row)

	log.Debug().Msgf("check: %d==%d", len(headers), len(row))

}
