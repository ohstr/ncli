package relay

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/ohstr/ncli/cli/common"
	"github.com/spf13/viper"
)

// sharedConfigKeys are the keys other ncli commands read from the same
// config file (an ncli.yaml can hold both), so the relay leaves them alone.
// Each is one exact leaf key: anything else under the same section (a
// stray "query: {enabled: true}") is still reported. Keep in step with the
// viper.GetString / keyresolve config keys outside cli/relay.
var sharedConfigKeys = map[string]bool{
	"log_dir":         true,
	"vault.password":  true,
	"signer":          true,
	"signer.identity": true,
	"trust_owners":    true,
	"attest_label":    true,
	"auth_tag":        true,
	"delegate.issuer": true,
	"import.password": true,
	"query.identity":  true,
	"event.identity":  true,
	"space.identity":  true,
	"bunker.identity": true,
	"huddle.identity": true,
	"groups.identity": true,
}

// decodeRelayConfig decodes v into cfg and returns the config keys that
// matched nothing in RelayConfig, by dotted path, sorted. A known section
// keeps its documented spelling and the unknown part is lowercased (viper
// lowercases keys), e.g. "httpBridge.qurey". An unknown section holding
// only shared keys is skipped; one mixing both reports its other leaves.
func decodeRelayConfig(v *viper.Viper, cfg *RelayConfig) ([]string, error) {
	var md mapstructure.Metadata
	if err := v.Unmarshal(cfg, func(dc *mapstructure.DecoderConfig) { dc.Metadata = &md }); err != nil {
		return nil, err
	}
	unused := md.Unused

	leaves := v.AllKeys()
	seen := map[string]bool{}
	var unknown []string
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			unknown = append(unknown, k)
		}
	}
	for _, k := range unused {
		lk := strings.ToLower(k)
		var under, other []string
		for _, leaf := range leaves {
			if leaf == lk || strings.HasPrefix(leaf, lk+".") {
				under = append(under, leaf)
				if !sharedConfigKeys[leaf] {
					other = append(other, leaf)
				}
			}
		}
		switch {
		case len(under) == 0 || len(other) == len(under):
			if !sharedConfigKeys[lk] {
				add(k)
			}
		default:
			for _, leaf := range other {
				add(leaf)
			}
		}
	}
	sort.Strings(unknown)
	return unknown, nil
}

// unknownKeysError is --check's failure for keys the relay doesn't know.
func unknownKeysError(unknown []string) error {
	err := &common.CLIError{
		Err:  fmt.Errorf("unknown config keys: %s", strings.Join(unknown, ", ")),
		Code: common.CodeInvalidInput,
	}
	if len(unknown) == 1 {
		err.Err = errors.New("unknown config key: " + unknown[0])
		err.Input = unknown[0]
	}
	return err
}
