package tui

import (
	"testing"

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

func TestModelDown(t *testing.T) {
	sdm := NewOutboundMetrics(1, "/data/db/notes.db", func() {})

	headers := sdm.Columns()
	row := sdm.Row()

	log.Debug().Msgf("headers=%v", headers)
	log.Debug().Msgf("row=%v", row)

	log.Debug().Msgf("check: %d==%d", len(headers), len(row))

}
