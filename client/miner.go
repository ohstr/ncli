package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip13"
	"github.com/rs/zerolog/log"
	"sigs.k8s.io/yaml"
)

// MineOptions configures client.Mine's search strategy and identity
// handling. A nil *MineOptions behaves like a zero-value one: Workers
// resolves to runtime.NumCPU() either way, since this CLI-facing layer
// defaults to parallel mining, unlike nip13.Mine's own conservative
// (single-threaded) default for other direct callers.
type MineOptions struct {
	// Workers is how many goroutines search for a valid nonce in parallel.
	// <=0 resolves to runtime.NumCPU().
	Workers int

	// Progress, if set, is called roughly every ProgressInterval (default
	// 5s if <=0) with mining progress aggregated across all workers.
	Progress         func(nip13.Progress)
	ProgressInterval time.Duration

	// IdentityPubKeyHex, if non-empty, fills the event's pubkey field
	// before mining if it was empty, or errors if the event already
	// declares a different pubkey.
	IdentityPubKeyHex string

	// SignPrivKeyHex, if non-empty, signs the event immediately after
	// mining succeeds and before it's written to outPath. This must be the
	// private key for the same identity that supplied IdentityPubKeyHex
	// (the CLI layer guarantees this: both come from resolving one
	// --identity value) -- nip01.Event.Sign recomputes both PubKey and ID
	// from the signing key and the event's current (already nonce-tagged)
	// fields, so as long as the pubkey matches what was mined, the
	// recomputed ID equals the mined one and no re-mining is needed.
	SignPrivKeyHex string
}

func (o *MineOptions) workers() int {
	if o == nil || o.Workers <= 0 {
		return runtime.NumCPU()
	}
	return o.Workers
}

// LoadDraftEvent reads eventPath (.json/.jsonp/.yaml/.yml) as an unsigned
// event -- mine's traditional structured `-e` input.
func LoadDraftEvent(eventPath string) (*nip01.Event, error) {
	bytes, err := os.ReadFile(eventPath)
	if err != nil {
		return nil, err
	}

	var event *nip01.Event
	if err := yaml.UnmarshalStrict(bytes, &event); err != nil {
		return nil, err
	}
	return event, nil
}

// Mine mines draft at difficulty (in place), optionally signs it (see
// MineOptions.SignPrivKeyHex), writes the result to outPath, and returns the
// mined event. outPath's own extension (.json/.jsonp/.yaml/.yml) governs the
// output format. Pass an outPath equal to the path draft was originally
// loaded from to overwrite that file in place -- Mine never does this
// implicitly; that choice belongs to the caller (the CLI's
// --in-place flag).
func Mine(ctx context.Context, event *nip01.Event, outPath string, difficulty int, opts *MineOptions) (*nip01.Event, error) {

	startTime := time.Now()
	defer func() {
		log.Info().Msgf("duration: %s", time.Since(startTime))
	}()

	var err error

	if opts != nil && opts.IdentityPubKeyHex != "" {
		switch {
		case event.PubKey == "":
			event.PubKey = opts.IdentityPubKeyHex
		case !strings.EqualFold(event.PubKey, opts.IdentityPubKeyHex):
			return nil, fmt.Errorf("event pubkey %q conflicts with --identity's resolved pubkey %q", event.PubKey, opts.IdentityPubKeyHex)
		}
	}

	mineOpts := []nip13.MineOption{nip13.WithWorkers(opts.workers())}
	if opts != nil && opts.Progress != nil {
		mineOpts = append(mineOpts, nip13.WithProgress(opts.ProgressInterval, opts.Progress))
	}

	if err := event.Mine(ctx, difficulty, mineOpts...); err != nil {
		return nil, err
	}

	if opts != nil && opts.SignPrivKeyHex != "" {
		if err := event.Sign(opts.SignPrivKeyHex); err != nil {
			return nil, fmt.Errorf("failed to sign mined event: %w", err)
		}
	}

	var data []byte

	switch strings.ToLower(filepath.Ext(outPath)) {
	case ".json", ".jsonp":
		data, err = json.MarshalIndent(event, "", "    ")
	case ".yaml", ".yml":
		data, err = yaml.Marshal(event)
	default:
		return nil, fmt.Errorf("unsupported output extension %q (must be .json, .jsonp, .yaml, or .yml)", filepath.Ext(outPath))
	}
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return nil, err
	}

	return event, nil
}

// POWCheckResult is one event's NIP-13 proof-of-work verification outcome.
type POWCheckResult struct {
	ID         string `json:"id"`
	Valid      bool   `json:"valid"`
	Difficulty int    `json:"difficulty,omitempty"`
	Nonce      string `json:"nonce,omitempty"`
	Error      string `json:"error,omitempty"`
}

// POWCheckReport summarizes CheckPOWFromFile/CheckPOWLive's verification of
// a batch of events.
type POWCheckReport struct {
	Checked int              `json:"checked"`
	Valid   int              `json:"valid"`
	Invalid int              `json:"invalid"`
	Results []POWCheckResult `json:"results"`
}

// AllValid reports whether every checked event passed PoW verification.
func (r *POWCheckReport) AllValid() bool {
	return r.Invalid == 0
}

func verifyEvents(events []*nip01.Event) *POWCheckReport {
	report := &POWCheckReport{Checked: len(events), Results: make([]POWCheckResult, 0, len(events))}

	for _, event := range events {
		fields := nip13.Fields{ID: event.ID, PubKey: event.PubKey, CreatedAt: event.CreatedAt, Kind: event.Kind, Tags: event.Tags, Content: event.Content}
		nonce, difficulty, err := nip13.ValidatePow(fields)

		result := POWCheckResult{ID: event.ID}
		if err != nil {
			result.Error = err.Error()
			report.Invalid++
		} else {
			result.Valid = true
			result.Nonce = nonce
			result.Difficulty = difficulty
			report.Valid++
		}
		report.Results = append(report.Results, result)
	}

	return report
}

// LoadEvents reads eventsPath as either a JSON array of events (e.g. one
// produced by `ncli dump`) or a single event object (e.g. `ncli miner
// mine`'s own `-o` output) -- so a freshly mined/signed single event can be
// fed straight into `miner check`/`ncli publish` without first wrapping it
// in a one-element array by hand.
func LoadEvents(eventsPath string) ([]*nip01.Event, error) {
	bytes, err := os.ReadFile(eventsPath)
	if err != nil {
		return nil, err
	}

	var events []*nip01.Event
	if err := yaml.UnmarshalStrict(bytes, &events); err == nil {
		return events, nil
	}

	var event *nip01.Event
	if err := yaml.UnmarshalStrict(bytes, &event); err != nil {
		return nil, fmt.Errorf("%s is neither a JSON array of events nor a single event object: %w", eventsPath, err)
	}
	return []*nip01.Event{event}, nil
}

// CheckPOWFromFile verifies PoW on every event in eventsPath -- see
// LoadEvents for the accepted shapes.
func CheckPOWFromFile(eventsPath string) (*POWCheckReport, error) {
	events, err := LoadEvents(eventsPath)
	if err != nil {
		return nil, err
	}

	return verifyEvents(events), nil
}

// CheckPOWLive fetches events matching filtersSpec from every target in
// targets -- merged and deduplicated by event ID across ALL targets (see
// mergeEventsFromTargets), not stopping at the first target with a match --
// and verifies PoW on the merged result.
func CheckPOWLive(ctx context.Context, targets *TargetsSpec, filtersSpec []*FilterSpec) (*POWCheckReport, error) {
	// A SubscriptionFilterGroup with zero filters matches nothing (each
	// filter is OR'd in; with none to OR, nothing ever matches) -- unlike a
	// single empty filter, which matches everything. "no filters at all"
	// (--targets with no --filters/inline flags/--identity) should mean
	// "check everything", so ensure at least one (possibly empty) filter is
	// always present, matching StreamSpec/InspectSpec/SyncSpec's own
	// UnmarshalJSON convention (client/spec.go).
	if len(filtersSpec) == 0 {
		filtersSpec = []*FilterSpec{{}}
	}

	filters := nip01.NewSubscriptionFilterGroup()
	for _, f := range filtersSpec {
		filters.Add(&f.SubscriptionFilter)
	}

	events, err := mergeEventsFromTargets(ctx, targets, filters, 0)
	if err != nil {
		return nil, err
	}

	return verifyEvents(events), nil
}

// ApplyIdentityFilter ANDs pubkeyHex into every filter in filtersSpec as an
// Authors constraint: a filter with no Authors yet gets pubkeyHex as its
// only author; a filter that already declares Authors not including
// pubkeyHex is a contradiction (it could never match anything under the
// added constraint) and is reported as an error rather than silently
// dropped or ignored. If filtersSpec is empty, a single
// {Authors:[pubkeyHex]} filter is returned.
func ApplyIdentityFilter(filtersSpec []*FilterSpec, pubkeyHex string) ([]*FilterSpec, error) {
	if len(filtersSpec) == 0 {
		return []*FilterSpec{NewFilterSpec(&nip01.SubscriptionFilter{Authors: []string{pubkeyHex}})}, nil
	}

	for _, f := range filtersSpec {
		if len(f.Authors) == 0 {
			f.Authors = []string{pubkeyHex}
			continue
		}

		found := false
		for _, a := range f.Authors {
			if strings.EqualFold(a, pubkeyHex) {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("--identity's pubkey %q is not among this filter's existing authors %v (the filter could never match)", pubkeyHex, f.Authors)
		}
		f.Authors = []string{pubkeyHex}
	}

	return filtersSpec, nil
}
