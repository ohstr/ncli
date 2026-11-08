package relay

import (
	"github.com/rs/zerolog/log"
)

// warn records a config warning. Every warning initConfig finds goes
// through here rather than straight to the log, so configWarnings can list
// it in --check --json's "warnings" and the relay logs it once at startup.
func (c *RelayConfig) warn(msg string) {
	c.warnings = append(c.warnings, msg)
}

// configWarnings are all the warnings c deserves: the ones initConfig
// recorded, then those derived from the whole config. Never nil.
func (c *RelayConfig) configWarnings() []string {
	warnings := append([]string{}, c.warnings...)
	warnings = append(warnings, c.limitsWarnings()...)
	return warnings
}

// logConfigWarnings logs each of c's warnings once and returns them.
func (c *RelayConfig) logConfigWarnings() []string {
	warnings := c.configWarnings()
	for _, w := range warnings {
		log.Warn().Msg(w)
	}
	return warnings
}

// portIgnoredWarning is the warning for a port that listen overrides.
const portIgnoredWarning = "port is ignored: listen sets the relay's address"

// powStrictWithoutMinWarning is the warning for pow.strict with no minimum.
const powStrictWithoutMinWarning = "pow.strict is true but pow.min is 0 -- there is no difficulty requirement to enforce"
