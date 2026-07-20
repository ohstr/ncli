package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"sigs.k8s.io/yaml"
)

type Spec interface{}

type RootSpec struct {
	Kind string `json:"kind"`
	Spec Spec   `json:"spec"`
}

func (rs *RootSpec) UnmarshalJSON(data []byte) error {

	temp := struct {
		Kind string          `json:"kind"`
		Spec json.RawMessage `json:"spec"`
	}{}

	if err := yaml.UnmarshalStrict(data, &temp); err != nil {
		return err
	}

	switch strings.ToLower(temp.Kind) {
	case "stream":
		rs.Spec = &StreamSpec{}

	case "inspect":
		rs.Spec = &InspectSpec{}

	case "targets":
		rs.Spec = &TargetsSpec{}

	case "sync":
		rs.Spec = &SyncSpec{}

	default:
		return fmt.Errorf("unexpected kind `%s`", temp.Kind)
	}

	rs.Kind = temp.Kind
	if err := json.Unmarshal(temp.Spec, &rs.Spec); err != nil {
		return err
	}

	if rs.Spec == nil {
		return errors.New("undefined spec")
	}

	return nil
}

type FlowType string
type EnsurePolicy string

const (
	FlOW_LOCAL   FlowType     = "local"
	FlOW_REMOTE  FlowType     = "remote"
	FLOW_FILE    FlowType     = "file"
	EnsureCreate EnsurePolicy = "create"
	EnsureExists EnsurePolicy = "exists"
)

type FlowSpec struct {
	Type    FlowType     `json:"type,omitempty"`
	Path    string       `json:"path,omitempty"`
	Relay   string       `json:"relay,omitempty"`
	Ensure  EnsurePolicy `json:"ensure,omitempty"`
	Trusted bool         `json:"trusted,omitempty"`

	// WriteConcurrency overrides how many concurrent workers a FlOW_LOCAL
	// destination's LocalSubscription.Write runs (see localWriteConcurrency
	// in client/stream.go). Zero/unset means "use the default" -- applied at
	// NewLocalSubscription rather than here, since the bare-string shorthand
	// branch of UnmarshalJSON below never goes through a default-filling
	// step of its own.
	WriteConcurrency int `json:"writeConcurrency,omitempty" yaml:"writeConcurrency,omitempty"`

	relayURI *url.URL
	// relayFallbackURI is set only when Relay had no explicit ws(s)://
	// scheme: the ws:// counterpart of relayURI (which defaults such input
	// to wss://), tried by connectRelayWithFallback/readEventsWithFallback
	// if wss:// fails to connect. An explicit scheme means the caller said
	// exactly what they wanted, so this stays nil -- no silent fallback.
	relayFallbackURI *url.URL
	killed           bool
}

// ensureLocalStoreDir creates the parent directory of a local flow's store
// path when its ensure policy is "create". bolt.Open (via
// relay.NewEventStore) creates the db file itself but not missing parent
// directories, so callers opening a FlOW_LOCAL store must call this first.
func ensureLocalStoreDir(spec *FlowSpec) error {
	if spec.Ensure != EnsureCreate {
		return nil
	}
	return os.MkdirAll(filepath.Dir(spec.Path), 0755)
}

// flowSpecFromString resolves a bare-string flow entry (as used in a
// targets/from/to list) to a FlowSpec. An explicit ws(s):// URL is always a
// relay. A bare string with no scheme is ambiguous between a relay host and
// a local store path -- an existing local file wins (preserving the
// pre-existing path shorthand); otherwise it's treated as a relay host,
// resolved via resolveRelayURL (wss:// primary, ws:// fallback). Shared by
// FlowSpec.UnmarshalJSON and TargetsFromRelayList (--relays' comma-list).
func flowSpecFromString(str string) (*FlowSpec, error) {
	if strings.Contains(str, "://") {
		if u, _, err := resolveRelayURL(str); err == nil {
			return &FlowSpec{Type: FlOW_REMOTE, Relay: str, relayURI: u}, nil
		}
		return nil, fmt.Errorf("invalid input: %s", str)
	}
	if path, err := fileExists(str); err == nil {
		return &FlowSpec{Type: FlOW_LOCAL, Path: path}, nil
	}
	if u, fallback, err := resolveRelayURL(str); err == nil {
		return &FlowSpec{Type: FlOW_REMOTE, Relay: str, relayURI: u, relayFallbackURI: fallback}, nil
	}
	return nil, fmt.Errorf("invalid input: %s", str)
}

func (ds *FlowSpec) UnmarshalJSON(data []byte) error {

	type Alias FlowSpec
	var flowSpec Alias

	var str string

	if err := json.Unmarshal(data, &str); err == nil {
		fs, err := flowSpecFromString(str)
		if err != nil {
			return err
		}
		*ds = FlowSpec(*fs)
		return nil
	}

	if err := json.Unmarshal(data, &flowSpec); err != nil {
		return err
	}

	if flowSpec.Type == "" {
		if flowSpec.Relay != "" {
			flowSpec.Type = FlOW_REMOTE
		} else if flowSpec.Path != "" {
			flowSpec.Type = FlOW_LOCAL
		}
	}

	switch flowSpec.Type {
	case FlOW_REMOTE:
		if u, fallback, err := resolveRelayURL(flowSpec.Relay); err != nil {
			return err
		} else {
			flowSpec.relayURI = u
			flowSpec.relayFallbackURI = fallback
		}

	case FlOW_LOCAL:

		ensureNormalized := EnsurePolicy(strings.ToLower(strings.ReplaceAll(string(flowSpec.Ensure), " ", "")))

		if len(ensureNormalized) == 0 {
			ensureNormalized = EnsureExists
		} else if !slices.Contains([]EnsurePolicy{EnsureCreate, EnsureExists}, ensureNormalized) {
			return fmt.Errorf("unknown ensure policy: %s", flowSpec.Ensure)
		}
		flowSpec.Ensure = ensureNormalized

		if path, err := fileExists(flowSpec.Path); err != nil && ensureNormalized == EnsureExists {
			return err
		} else {
			flowSpec.Path = path
		}

	case FLOW_FILE:
		if path, err := fileExists(flowSpec.Path); err != nil {
			return err
		} else {
			flowSpec.Path = path
		}

	default:
		return fmt.Errorf("invalid entry: %v", string(data))
	}

	*ds = FlowSpec(flowSpec)

	return nil
}

type StreamSpec struct {
	From     []*FlowSpec   `json:"from" yaml:"from"`
	To       []*FlowSpec   `json:"to" yaml:"to"`
	Filters  []*FilterSpec `json:"filters" yaml:"filters"`
	Timeouts *TimeoutSpec  `json:"timeouts,omitempty" yaml:"timeouts,omitempty"`

	filters *nip01.SubscriptionFilterGroup

	Recovery *RecoverySpec `json:"recovery,omitempty" yaml:"recovery,omitempty"`
	Raw      bool          `json:"raw,omitempty" yaml:"raw,omitempty"`

	// StrictPow enforces NIP-13 proof-of-work on untrusted events: false
	// (the default) accepts an event regardless of a missing/insufficient
	// nonce tag; true rejects it. The `apply --strict-pow` CLI flag
	// overrides this field for the current run when passed explicitly --
	// see Client.strictPow.
	StrictPow bool `json:"strictPow,omitempty" yaml:"strictPow,omitempty"`
}

// RecoverySpec tunes the always-on recovery subsystem (see NewStream).
// Every field is optional: recovery runs with sensible defaults even when
// this whole block is omitted from the YAML.
type RecoverySpec struct {
	// StorePath overrides where the recovery bbolt store lives. If empty, a
	// stable path derived from this stream's destinations is used, under the
	// OS temp directory.
	StorePath     string `json:"store_path,omitempty" yaml:"store_path,omitempty"`
	MaxRetries    int    `json:"max_retries,omitempty" yaml:"max_retries,omitempty"`
	RetryInterval string `json:"retry_interval,omitempty" yaml:"retry_interval,omitempty"`
}

type TimeoutSpec struct {
	Handshake *string `json:"handshake,omitempty"`
	Ping      *string `json:"ping,omitempty"`
	Pong      *string `json:"pong,omitempty"`
	Write     *string `json:"write,omitempty"`
}

func (ss *StreamSpec) UnmarshalJSON(data []byte) error {

	type Alias StreamSpec
	var temp Alias
	if err := yaml.UnmarshalStrict(data, &temp); err != nil {
		return err
	}

	if len(temp.From) == 0 {
		return errors.New("undefined `from` flow")
	} else if len(temp.To) == 0 {
		return errors.New("undefined `to` flow")
	}

	if len(temp.Filters) == 0 {
		temp.Filters = append(temp.Filters, &FilterSpec{})
	}

	*ss = StreamSpec(temp)
	ss.filters = nip01.NewSubscriptionFilterGroup()

	for _, f := range ss.Filters {
		if f == nil {
			return errors.New("invalid filters: got nil")
		}
		ss.filters.Add(&f.SubscriptionFilter)
	}

	for _, flow := range append(ss.From, ss.To...) {
		if flow == nil {
			return errors.New("invalid flows: got nil")
		}
	}

	return nil
}

type FilterSpec struct {
	nip01.SubscriptionFilter
}

func NewFilterSpec(f *nip01.SubscriptionFilter) *FilterSpec {
	return &FilterSpec{
		SubscriptionFilter: *f,
	}
}

func (fs *FilterSpec) UnmarshalJSON(data []byte) error {

	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}

	var since, until uint64
	var err error

	parseTs := func(v interface{}, past bool) (uint64, error) {
		switch val := v.(type) {
		case float64:
			return uint64(val), nil
		case int:
			return uint64(val), nil
		case int64:
			return uint64(val), nil
		case string:
			return ParseDurationUnit(val, past)
		default:
			return 0, fmt.Errorf("invalid type for timestamp: %T", v)
		}
	}

	if v, ok := raw["since"]; ok {
		if since, err = parseTs(v, true); err != nil {
			return err
		}
	}

	if v, ok := raw["until"]; ok {
		if until, err = parseTs(v, false); err != nil {
			return err
		}
	}

	// Remove fields that might cause type mismatch
	delete(raw, "since")
	delete(raw, "until")

	// Marshal back to JSON (or YAML) to unmarshal into the struct without those fields
	cleanData, err := json.Marshal(raw)
	if err != nil {
		return err
	}

	type Alias FilterSpec
	var subFilter Alias
	if err := json.Unmarshal(cleanData, &subFilter); err != nil {
		return err
	}

	*fs = FilterSpec(subFilter)
	fs.SubscriptionFilter.Since = since
	fs.SubscriptionFilter.Until = until

	return nil
}

// /////

type InspectSpec struct {
	Targets []*FlowSpec   `json:"targets,omitempty"`
	Filters []*FilterSpec `json:"filters,omitempty"`

	filters *nip01.SubscriptionFilterGroup
}

func (is *InspectSpec) UnmarshalJSON(data []byte) error {

	type Alias InspectSpec
	var temp Alias
	if err := yaml.UnmarshalStrict(data, &temp); err != nil {
		return err
	}

	if len(temp.Filters) == 0 {
		temp.Filters = append(temp.Filters, &FilterSpec{})
	}
	*is = InspectSpec(temp)

	is.filters = nip01.NewSubscriptionFilterGroup()
	for _, f := range is.Filters {
		if f == nil {
			return errors.New("invalid filters: got nil")
		}
		is.filters.Add(&f.SubscriptionFilter)
	}

	for _, target := range is.Targets {
		if target == nil {
			return errors.New("invalid targets: got nil")
		}
	}

	return nil
}

// /////

// TargetsSpec is the `kind: targets` file loaded by find/dump/miner
// check's --targets flag: any mix of remote relays and local event-store
// paths, plus an optional Filters list -- letting one file declare both
// "where to look" and "what to match" instead of splitting them across two
// flags. Filters follows the same OR-across-filters convention as
// StreamSpec/InspectSpec/SyncSpec; omitting it entirely means "match
// everything" rather than "match nothing".
type TargetsSpec struct {
	Relays  []*FlowSpec   `json:"relays,omitempty"`
	Filters []*FilterSpec `json:"filters,omitempty"`
}

func (ts *TargetsSpec) UnmarshalJSON(data []byte) error {

	type Alias TargetsSpec
	var temp Alias
	if err := yaml.UnmarshalStrict(data, &temp); err != nil {
		return err
	}

	*ts = TargetsSpec(temp)

	for _, relay := range ts.Relays {
		if relay == nil {
			return errors.New("invalid targets: got nil")
		}
	}

	for _, f := range ts.Filters {
		if f == nil {
			return errors.New("invalid filters: got nil")
		}
	}

	return nil
}

// /////

const (
	SyncDirectionBoth = "both"
	SyncDirectionUp   = "up"
	SyncDirectionDown = "down"
)

type SyncSpec struct {
	From               *FlowSpec     `json:"from" yaml:"from"`
	To                 *FlowSpec     `json:"to" yaml:"to"`
	Direction          string        `json:"direction,omitempty" yaml:"direction,omitempty"`
	Filters            []*FilterSpec `json:"filters,omitempty" yaml:"filters,omitempty"`
	Timeouts           *TimeoutSpec  `json:"timeouts,omitempty" yaml:"timeouts,omitempty"`
	MaxReconcileRounds int           `json:"maxReconcileRounds,omitempty" yaml:"maxReconcileRounds,omitempty"`
	PullBatchSize      int           `json:"pullBatchSize,omitempty" yaml:"pullBatchSize,omitempty"`

	// StrictPow enforces NIP-13 proof-of-work on pulled events: false (the
	// default) accepts one regardless of a missing/insufficient nonce tag;
	// true drops it instead of inserting it locally. The `apply
	// --strict-pow` CLI flag overrides this field for the current run when
	// passed explicitly -- see Client.strictPow.
	StrictPow bool `json:"strictPow,omitempty" yaml:"strictPow,omitempty"`

	filters *nip01.SubscriptionFilterGroup

	local  *FlowSpec
	remote *FlowSpec
}

func (ss *SyncSpec) GetLocal() *FlowSpec {
	return ss.local
}

func (ss *SyncSpec) GetRemote() *FlowSpec {
	return ss.remote
}

func (ss *SyncSpec) UnmarshalJSON(data []byte) error {

	type Alias SyncSpec
	var temp Alias
	if err := yaml.UnmarshalStrict(data, &temp); err != nil {
		return err
	}

	if temp.From == nil {
		return errors.New("undefined `from` flow")
	}
	if temp.To == nil {
		return errors.New("undefined `to` flow")
	}

	for _, f := range []*FlowSpec{temp.From, temp.To} {
		if f.Type == FlOW_LOCAL {
			if temp.local != nil {
				return errors.New("multiple local stores defined (only one allowed for sync)")
			}
			temp.local = f
		} else if f.Type == FlOW_REMOTE {
			if temp.remote != nil {
				return errors.New("multiple remote relays defined (only one allowed for sync)")
			}
			temp.remote = f
		}
	}

	if temp.local == nil {
		return errors.New("undefined local store in flows")
	}
	if temp.remote == nil {
		return errors.New("undefined remote relay in flows")
	}

	if temp.Direction == "" {
		temp.Direction = SyncDirectionBoth
	}
	if !slices.Contains([]string{SyncDirectionBoth, SyncDirectionUp, SyncDirectionDown}, temp.Direction) {
		return fmt.Errorf("invalid direction: %s (must be both, up, or down)", temp.Direction)
	}

	*ss = SyncSpec(temp)

	if ss.MaxReconcileRounds <= 0 {
		ss.MaxReconcileRounds = 20
	}
	if ss.PullBatchSize <= 0 {
		ss.PullBatchSize = 100
	}

	if len(ss.Filters) == 0 {
		ss.Filters = append(ss.Filters, &FilterSpec{})
	}

	ss.filters = nip01.NewSubscriptionFilterGroup()
	for _, f := range ss.Filters {
		if f == nil {
			return errors.New("invalid filters: got nil")
		}
		ss.filters.Add(&f.SubscriptionFilter)
	}

	return nil
}

//////

// resolveRelayURL parses a relay input into its primary connection URL and,
// when raw has no explicit ws(s):// scheme, a ws:// fallback candidate --
// wss:// is tried first (see connectRelayWithFallback/
// readEventsWithFallback), falling back to ws:// only if that fails to
// connect. An explicit scheme is taken at face value with no fallback:
// writing "ws://" or "wss://" already says exactly what's wanted.
func resolveRelayURL(raw string) (primary *url.URL, fallback *url.URL, err error) {
	if !strings.Contains(raw, "://") {
		if !looksLikeRelayHost(raw) {
			return nil, nil, fmt.Errorf("invalid relay URL %s", raw)
		}
		u, err := url.Parse("wss://" + raw)
		if err != nil || u.Host == "" {
			return nil, nil, fmt.Errorf("invalid relay URL %s", raw)
		}
		f, err := url.Parse("ws://" + raw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid relay URL %s", raw)
		}
		return u, f, nil
	}

	uri, err := url.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid relay URL %s: %w", raw, err)
	} else if !slices.Contains([]string{"ws", "wss"}, uri.Scheme) {
		return nil, nil, fmt.Errorf("invalid relay URL %s, unsupported scheme", raw)
	} else if uri.Host == "" {
		return nil, nil, fmt.Errorf("invalid relay URL %s, empty host", raw)
	}
	return uri, nil, nil
}

// looksLikeRelayHost is resolveRelayURL's gate on schemeless input: any
// bare string technically parses as a syntactically "valid" single-label
// URL host, which would otherwise swallow plain typos ("not-a-relay-url")
// and local store paths ("../notes.db") as relay candidates instead of
// letting them fail with a clear error / fall through to the file-path
// check in flowSpecFromString. A path separator rules out a host outright;
// otherwise this requires a dot (domain-like), "localhost", or a bare IP --
// the same shape every schemeless relay input in practice actually has.
func looksLikeRelayHost(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, `/\`) {
		return false
	}
	host := raw
	if h, _, err := net.SplitHostPort(raw); err == nil {
		host = h
	}
	return host == "localhost" || strings.Contains(host, ".") || net.ParseIP(host) != nil
}

const (
	HoursPerDay   = 24
	HoursPerWeek  = 168 // 24 hours * 7 days
	DaysPerMonth  = 30  // Simplification
	HoursPerMonth = HoursPerDay * DaysPerMonth
)

// ParseDurationUnit parses a filter's since/until value: either an absolute
// unix timestamp (a bare integer string) or a relative duration ("1h", "1w
// 2d", ...) resolved against time.Now(), looking backward if past is true
// (since's default direction) or forward otherwise (until's default
// direction) -- see parseDuration for the supported unit suffixes.
func ParseDurationUnit(input string, past bool) (uint64, error) {

	if in := strings.ReplaceAll(input, " ", ""); len(in) == 0 {
		return 0, nil
	}
	if unit, err := strconv.ParseUint(input, 10, 64); err == nil {
		return unit, nil
	} else if duration, err := parseDuration(input); err == nil {
		if past {
			return uint64(time.Now().Add(-duration).Unix()), nil
		} else {
			return uint64(time.Now().Add(duration).Unix()), nil
		}
	} else {
		return 0, fmt.Errorf("invalid value: %s", input)
	}
}

// ParseDuration parses a duration string built from the same unit suffixes
// as ParseDurationUnit (h, mo, m, s, d, w -- e.g. "24h", "7d", "2w", "1mo"),
// returning the plain time.Duration rather than resolving it against
// time.Now(). Use this wherever a config value needs a duration, not a
// timestamp.
func ParseDuration(durationStr string) (time.Duration, error) {
	return parseDuration(durationStr)
}

func parseDuration(durationStr string) (time.Duration, error) {
	durationStr = strings.ReplaceAll(durationStr, " ", "") // Remove all whitespace
	var totalDuration time.Duration
	var lastIndex = 0
	sign := 1.0 // Positive by default

	// Handle global sign
	if strings.HasPrefix(durationStr, "-") {
		sign = -1.0
		durationStr = durationStr[1:] // Remove the '-' for easier processing
	} else if strings.HasPrefix(durationStr, "+") {
		durationStr = durationStr[1:] // Remove the '+' for easier processing
	}

	for i, char := range durationStr {
		if (char < '0' || char > '9') && char != '.' && lastIndex <= i {

			numberStr := durationStr[lastIndex:i]
			number, err := strconv.ParseFloat(numberStr, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid number: %s", numberStr)
			}

			unitStart := i
			for i < len(durationStr) && (durationStr[i] < '0' || durationStr[i] > '9') && durationStr[i] != '.' {
				i++
			}

			unit := durationStr[unitStart:i]
			unitDuration, err := parseUnitDuration(number, unit)
			if err != nil {
				return 0, err
			}

			totalDuration += unitDuration
			lastIndex = i

		}
	}

	if lastIndex < len(durationStr) {
		return 0, fmt.Errorf("unprocessed data at the end: %s", durationStr[lastIndex:])
	}

	return time.Duration(float64(totalDuration) * sign), nil
}

func parseUnitDuration(number float64, unit string) (time.Duration, error) {
	var unitDuration time.Duration

	switch unit {
	case "h":
		unitDuration = time.Duration(number * float64(time.Hour))
	case "mo":
		unitDuration = time.Duration(number * float64(HoursPerMonth) * float64(time.Hour))
	case "m":
		unitDuration = time.Duration(number * float64(time.Minute))
	case "s":
		unitDuration = time.Duration(number * float64(time.Second))
	case "d":
		unitDuration = time.Duration(number * float64(HoursPerDay) * float64(time.Hour))
	case "w":
		unitDuration = time.Duration(number * float64(HoursPerWeek) * float64(time.Hour))
	default:
		return 0, fmt.Errorf("invalid or unsupported time unit: %s", unit)
	}

	return unitDuration, nil
}

func fileExists(input string) (string, error) {
	if path, err := filepath.Abs(input); err != nil {
		return "", fmt.Errorf("failed to get absolute path %s: %w", path, err)
	} else if stat, err := os.Stat(path); err != nil || stat.IsDir() {
		return path, fmt.Errorf("failed to validate db path %s: %w", path, err)
	} else {
		return path, nil
	}
}
