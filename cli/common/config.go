// Package common holds infrastructure shared across ncli's cli/*
// subcommand packages: config loading and NIP-98 auth header generation.
package common

import (
	"os"
	"path/filepath"

	"github.com/ohstr/nmilat/nip19"
	"github.com/spf13/viper"
)

var (
	CfgFile string

	// ActiveRelayContext is set by ncli.InitConfig (cli/ncli/root.go) when
	// the resolved config file came from a saved `ncli relay context`
	// rather than an explicit --config flag or a ncli.yaml/relay.yaml in
	// the working directory. Purely informational -- it lets "using config
	// file" log lines (cli/relay/admin.go, cli/relay/command.go) name the
	// context alongside its path, so it's obvious at a glance which relay
	// a command is about to hit.
	ActiveRelayContext string
)

// localConfigNames is the priority order LoadViperConfig and
// FindLocalConfigFile search for an implicit (non---config) config file:
// ncli.yaml checked before relay.yaml, in whichever directory is searched.
var localConfigNames = []string{"ncli.yaml", "relay.yaml"}

// FindLocalConfigFile returns the path of the first of ncli.yaml/relay.yaml
// that exists directly in dir, or "" if neither does. Exported so
// ncli.InitConfig can check "does the working directory already have its
// own config" before falling back to a saved `ncli relay context` -- a
// directory's own config file keeps the same top priority it had before
// contexts existed.
func FindLocalConfigFile(dir string) string {
	for _, name := range localConfigNames {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// LoadViperConfig loads the configuration from a file or environment variables.
func LoadViperConfig(path string) error {
	if path != "" {
		viper.SetConfigFile(path)
		return viper.ReadInConfig()
	}

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()

	viper.AddConfigPath(cwd)
	viper.AddConfigPath(home)

	// Fix the config file's type to yaml (every ncli config example is
	// yaml) instead of leaving it unset: unset, viper's ReadInConfig sweeps
	// every registered decoder extension (yaml, yml, json, toml, ...) for
	// each config name in each search path -- up to ~28 stat calls in the
	// worst case (no config anywhere) on every single invocation. This is
	// scoped to this search-path branch only, not the explicit --config
	// path above, so `--config foo.json` etc. still works via SetConfigFile's
	// own extension-based type inference.
	viper.SetConfigType("yaml")

	// Priority: ncli.yaml then relay.yaml
	viper.SetConfigName("ncli")
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			viper.SetConfigName("relay")
			if err := viper.ReadInConfig(); err != nil {
				if _, ok := err.(viper.ConfigFileNotFoundError); ok {
					// No config file found, fallback to env
					viper.AutomaticEnv()
					return nil
				}
				return err
			}
		} else {
			return err
		}
	}

	viper.AutomaticEnv()
	return nil
}

// NormalizeKey ensures a key is in hex format, decoding nsec/npub if necessary.
func NormalizeKey(input string) string {
	return nip19.NormalizeToHex(input)
}
