package relay

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/ohstr/ncli/cli/common"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
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
// matched nothing in RelayConfig, by dotted path, sorted, each spelled as
// the config file writes it (viper lowercases keys; see spelledAsWritten),
// e.g. "httpBridge.qurey". An unknown section holding
// only shared keys is skipped; one mixing both reports its other leaves.
func decodeRelayConfig(v *viper.Viper, cfg *RelayConfig) ([]string, error) {
	var md mapstructure.Metadata
	if err := v.Unmarshal(cfg, func(dc *mapstructure.DecoderConfig) { dc.Metadata = &md }); err != nil {
		return nil, err
	}
	unused := md.Unused
	nullUnused, err := unknownNullKeys(v)
	if err != nil {
		return nil, err
	}
	unused = append(unused, nullUnused...)

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
	unknown = spelledAsWritten(v.ConfigFileUsed(), unknown)
	sort.Strings(unknown)
	return unknown, nil
}

// keySegment matches one name in a reported key: "a.b[c].d" is a, b, c, d.
var keySegment = regexp.MustCompile(`[^.\[\]]+`)

// spelledAsWritten gives each key in keys (lowercased by viper) the
// spelling it has in the config file at path, segment by segment, so
// "limits.connectionperip" is reported as the "limits.connectionPerIP" the
// operator wrote. A segment the file doesn't hold, such as a key set only
// by the environment, stays as it is, and so does everything after it.
func spelledAsWritten(path string, keys []string) []string {
	if path == "" || len(keys) == 0 {
		return keys
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return keys
	}
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 {
		return keys
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		node := doc.Content[0]
		out[i] = keySegment.ReplaceAllStringFunc(k, func(seg string) string {
			if node == nil || node.Kind != yaml.MappingNode {
				node = nil
				return seg
			}
			for j := 0; j+1 < len(node.Content); j += 2 {
				if strings.EqualFold(node.Content[j].Value, seg) {
					written := node.Content[j].Value
					node = node.Content[j+1]
					return written
				}
			}
			node = nil
			return seg
		})
	}
	return out
}

// unknownNullKeys returns the keys with no value ("bogus:") that RelayConfig
// doesn't know. viper.Unmarshal never sees them (AllSettings drops a nil
// value, though AllKeys keeps it), so they're decoded again on their own,
// as nil, which any field accepts.
func unknownNullKeys(v *viper.Viper) ([]string, error) {
	nulls := map[string]any{}
	found := false
	for _, k := range v.AllKeys() {
		if v.Get(k) != nil {
			continue
		}
		found = true
		parts := strings.Split(k, ".")
		m := nulls
		for _, p := range parts[:len(parts)-1] {
			sub, ok := m[p].(map[string]any)
			if !ok {
				sub = map[string]any{}
				m[p] = sub
			}
			m = sub
		}
		m[parts[len(parts)-1]] = nil
	}
	if !found {
		return nil, nil
	}
	var md mapstructure.Metadata
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{Metadata: &md, Result: new(RelayConfig)})
	if err != nil {
		return nil, err
	}
	if err := dec.Decode(nulls); err != nil {
		return nil, err
	}
	return md.Unused, nil
}

// unknownKeyHints say what to use instead of a NIP-11 key the relay sets
// itself, by lowercased key: an older config may still hold one.
var unknownKeyHints = map[string]string{
	"nip11.limitation.min_pow_difficulty": "use pow.min",
	"nip11.supported_nips":                "computed by the relay",
}

// unknownKeyHint is the hint for key, or "".
func unknownKeyHint(key string) string {
	return unknownKeyHints[strings.ToLower(key)]
}

// describeUnknownKey is key with its hint, if it has one.
func describeUnknownKey(key string) string {
	if h := unknownKeyHint(key); h != "" {
		return key + " (" + h + ")"
	}
	return key
}

// warnUnknownKey logs a start's warning for key, with its hint.
func warnUnknownKey(ev *zerolog.Event, key string) {
	ev = ev.Str("key", key)
	if h := unknownKeyHint(key); h != "" {
		ev = ev.Str("hint", h)
	}
	ev.Msg("unknown config key, ignored")
}

// unknownKeysError is --check's failure for keys the relay doesn't know.
func unknownKeysError(unknown []string) error {
	described := make([]string, len(unknown))
	for i, k := range unknown {
		described[i] = describeUnknownKey(k)
	}
	err := &common.CLIError{
		Err:  fmt.Errorf("unknown config keys: %s", strings.Join(described, ", ")),
		Code: common.CodeInvalidInput,
	}
	if len(unknown) == 1 {
		err.Err = errors.New("unknown config key: " + described[0])
		err.Input = unknown[0]
	}
	return err
}
