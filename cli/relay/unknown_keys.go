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

// sharedConfigKeys are keys other ncli commands read from the same config
// file (an ncli.yaml can hold both), so the relay leaves them alone. A
// section name covers every key under it. Keep in step with the
// viper.GetString / keyresolve config keys outside cli/relay.
var sharedConfigKeys = []string{
	"log_dir",
	"vault",
	"signer",
	"trust_owners",
	"attest_label",
	"auth_tag",
	"delegate",
	"import",
	"query",
	"event",
	"space",
	"bunker",
	"huddle.identity",
	"groups.identity",
}

// decodeRelayConfig decodes v into cfg and returns the config keys that
// matched nothing in RelayConfig, by dotted path, sorted. A known section
// keeps its documented spelling and the unknown part is lowercased (viper
// lowercases keys), e.g. "httpBridge.qurey".
func decodeRelayConfig(v *viper.Viper, cfg *RelayConfig) ([]string, error) {
	var md mapstructure.Metadata
	if err := v.Unmarshal(cfg, func(dc *mapstructure.DecoderConfig) { dc.Metadata = &md }); err != nil {
		return nil, err
	}
	var unknown []string
	for _, k := range md.Unused {
		if !isSharedConfigKey(k) {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	return unknown, nil
}

func isSharedConfigKey(key string) bool {
	key = strings.ToLower(key)
	for _, s := range sharedConfigKeys {
		if key == s || strings.HasPrefix(key, s+".") {
			return true
		}
	}
	return false
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
