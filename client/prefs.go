package client

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"

	"github.com/ohstr/ncli/cli/common"
	"sigs.k8s.io/yaml"
)

const prefsFileName = "prefs.yaml"

// Prefs holds persistent ncli preferences that aren't tied to any single
// project's spec files -- the default relay list consulted by commands
// (dump, find) when they aren't given an explicit source, the local
// identity vault's own keypair reference (see client/vault.go), and the
// named `ncli relay context` config-file shortcuts below.
type Prefs struct {
	Relays        []string          `json:"relays,omitempty" yaml:"relays,omitempty"`
	VaultIdentity *VaultIdentityRef `json:"vault_identity,omitempty" yaml:"vault_identity,omitempty"`

	// RelayContexts maps a short name to an absolute ncli/relay config
	// file path -- e.g. {"prod": "/etc/ncli/prod.yaml"} -- so relay admin
	// commands (stats, members, invites, roles, ...) can target a
	// specific relay by name instead of repeating --config on every
	// invocation. Managed via `ncli relay context add/remove`.
	RelayContexts map[string]string `json:"relay_contexts,omitempty" yaml:"relay_contexts,omitempty"`

	// CurrentRelayContext is the RelayContexts key currently in effect,
	// set via `ncli relay context use <name>`. common.LoadViperConfig's
	// caller (ncli.InitConfig) falls back to this context's path whenever
	// --config is omitted and the working directory has no ncli.yaml/
	// relay.yaml of its own.
	CurrentRelayContext string `json:"current_relay_context,omitempty" yaml:"current_relay_context,omitempty"`
}

// VaultIdentityRef is the vault's own keypair, used only to derive each
// saved identity's per-entry NIP-44 encryption key (see client/vault.go) --
// it is not itself a saved identity. Npub is plaintext (harmless to store
// openly); EncryptedNsec is the vault's private key wrapped with NIP-49
// ("ncryptsec1...") under the password chosen at vault-creation time.
type VaultIdentityRef struct {
	Npub          string `json:"npub" yaml:"npub"`
	EncryptedNsec string `json:"encrypted_nsec" yaml:"encrypted_nsec"`
}

// PrefsPath returns the OS-appropriate path to prefs.yaml, under ncli's
// shared app config directory (see common.AppConfigDir) -- the same
// directory the CLI's log file and crash log live under.
func PrefsPath() string {
	return filepath.Join(common.AppConfigDir(), prefsFileName)
}

// LoadPrefs reads prefs.yaml, returning an empty Prefs (not an error) if
// it doesn't exist yet -- a fresh install has no preferences configured.
func LoadPrefs() (*Prefs, error) {
	path := PrefsPath()

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Prefs{}, nil
	} else if err != nil {
		return nil, err
	}

	var p Prefs
	if err := yaml.UnmarshalStrict(data, &p); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	return &p, nil
}

// SavePrefs writes p to prefs.yaml, creating its parent directory if
// needed.
func SavePrefs(p *Prefs) error {
	path := PrefsPath()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(p)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}

// AddRelay validates and appends relay to p.Relays, reporting false
// (without modifying p) if it's already present.
func (p *Prefs) AddRelay(relay string) (bool, error) {
	if _, _, err := resolveRelayURL(relay); err != nil {
		return false, err
	}
	if slices.Contains(p.Relays, relay) {
		return false, nil
	}
	p.Relays = append(p.Relays, relay)
	return true, nil
}

// RemoveRelay removes relay from p.Relays, reporting false if it wasn't
// present.
func (p *Prefs) RemoveRelay(relay string) bool {
	idx := slices.Index(p.Relays, relay)
	if idx == -1 {
		return false
	}
	p.Relays = slices.Delete(p.Relays, idx, idx+1)
	return true
}

// AddRelayContext saves name -> configPath's absolute form in
// p.RelayContexts, overwriting any existing entry for name. configPath
// must already exist as a regular file -- a typo'd path here would
// otherwise surface only later, as a confusing failure to load config the
// next time the context is used.
func (p *Prefs) AddRelayContext(name, configPath string) (string, error) {
	if name == "" {
		return "", errors.New("context name must not be empty")
	}

	abs, err := filepath.Abs(configPath)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", configPath, err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("config file %q: %w", abs, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory, not a config file", abs)
	}

	if p.RelayContexts == nil {
		p.RelayContexts = map[string]string{}
	}
	p.RelayContexts[name] = abs
	return abs, nil
}

// RemoveRelayContext deletes name from p.RelayContexts, reporting false if
// it wasn't present. Also clears CurrentRelayContext if it pointed at the
// removed name, rather than leaving it dangling on a name that no longer
// resolves to anything.
func (p *Prefs) RemoveRelayContext(name string) bool {
	if _, ok := p.RelayContexts[name]; !ok {
		return false
	}
	delete(p.RelayContexts, name)
	if p.CurrentRelayContext == name {
		p.CurrentRelayContext = ""
	}
	return true
}

// UseRelayContext sets p.CurrentRelayContext to name, failing if name
// isn't a saved context -- switching to an unknown context would otherwise
// silently fall back to cwd/home discovery, exactly the ambiguity
// contexts exist to remove.
func (p *Prefs) UseRelayContext(name string) error {
	if _, ok := p.RelayContexts[name]; !ok {
		return fmt.Errorf("no such relay context %q (run `ncli relay context` to list configured contexts)", name)
	}
	p.CurrentRelayContext = name
	return nil
}

// CurrentRelayContextPath returns the config file path of the current
// relay context, and false if none is set (or it points at a name that's
// since been removed).
func (p *Prefs) CurrentRelayContextPath() (path string, ok bool) {
	if p.CurrentRelayContext == "" {
		return "", false
	}
	path, ok = p.RelayContexts[p.CurrentRelayContext]
	return path, ok
}

// PrefsRelayURLs loads prefs.yaml and validates every configured relay.
// It errors out (naming `ncli prefs relays add`) if none are configured,
// since callers use this specifically as a fallback for an omitted
// explicit source/target.
func PrefsRelayURLs() ([]*url.URL, error) {
	prefs, err := LoadPrefs()
	if err != nil {
		return nil, err
	}
	if len(prefs.Relays) == 0 {
		return nil, errors.New("no relays configured; pass the relay(s) explicitly, or run `ncli prefs relays add <url>`")
	}

	urls := make([]*url.URL, 0, len(prefs.Relays))
	for _, r := range prefs.Relays {
		u, _, err := resolveRelayURL(r)
		if err != nil {
			return nil, fmt.Errorf("invalid relay in prefs (%s): %w", r, err)
		}
		urls = append(urls, u)
	}
	return urls, nil
}

// TargetsFromPrefs builds a TargetsSpec purely from the prefs relay list,
// treating every entry as a remote relay -- the fallback find/dump/miner
// check use when they aren't given an explicit --targets file or --relays.
// Resolves each entry itself (rather than via PrefsRelayURLs) so a
// schemeless entry keeps its ws:// fallback candidate, not just its
// wss:// primary.
func TargetsFromPrefs() (*TargetsSpec, error) {
	prefs, err := LoadPrefs()
	if err != nil {
		return nil, err
	}
	if len(prefs.Relays) == 0 {
		return nil, errors.New("no relays configured; pass the relay(s) explicitly, or run `ncli prefs relays add <url>`")
	}

	spec := &TargetsSpec{}
	for _, r := range prefs.Relays {
		u, fallback, err := resolveRelayURL(r)
		if err != nil {
			return nil, fmt.Errorf("invalid relay in prefs (%s): %w", r, err)
		}
		spec.Relays = append(spec.Relays, &FlowSpec{Type: FlOW_REMOTE, Relay: r, relayURI: u, relayFallbackURI: fallback})
	}
	return spec, nil
}

// TargetsFromRelayList builds a TargetsSpec from --relays' comma-separated
// entries: each may be a ws(s):// relay URL or a local .db path, resolved
// the same way a --targets file's bare-string entries are (see
// flowSpecFromString).
func TargetsFromRelayList(entries []string) (*TargetsSpec, error) {
	if len(entries) == 0 {
		return nil, errors.New("no relays given")
	}

	spec := &TargetsSpec{}
	for _, e := range entries {
		fs, err := flowSpecFromString(e)
		if err != nil {
			return nil, fmt.Errorf("invalid relay %q: %w", e, err)
		}
		spec.Relays = append(spec.Relays, fs)
	}
	return spec, nil
}
