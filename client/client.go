// Package client implements ncli's Nostr client: streaming, inspecting,
// syncing, and recovering events between relays and local bbolt stores.
package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/fsnotify/fsnotify"
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
	relayclient "github.com/ohstr/nmilat/relay/client"
	"github.com/rs/zerolog/log"
	"golang.org/x/term"
	"sigs.k8s.io/yaml"
)

func loadSpecFromYaml(yamlPath string) (*RootSpec, error) {

	f, err := os.Open(yamlPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	yamlBytes, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	var rs *RootSpec
	if err := yaml.Unmarshal(yamlBytes, &rs); err != nil {
		return nil, err
	}

	return rs, nil
}

// LoadTargetsSpec reads a `kind: targets` YAML file, for `find`'s
// --targets flag.
func LoadTargetsSpec(yamlPath string) (*TargetsSpec, error) {
	rs, err := loadSpecFromYaml(yamlPath)
	if err != nil {
		return nil, err
	}

	spec, ok := rs.Spec.(*TargetsSpec)
	if !ok {
		return nil, errors.New("unknown spec")
	}
	return spec, nil
}

// DumpFromTargets fetches events matching filtersSpec from every target in
// targets -- merged and deduplicated by event ID across ALL of them (see
// mergeEventsFromTargets) -- and writes the result to outPath as JSON.
// This is `dump`'s implementation: targets comes from --targets, --relays,
// or (both omitted) the configured prefs relays.
func DumpFromTargets(ctx context.Context, targets *TargetsSpec, outPath string, filtersSpec []*FilterSpec, timeout time.Duration) error {
	if len(filtersSpec) == 0 {
		filtersSpec = []*FilterSpec{{}}
	}

	filters := nip01.NewSubscriptionFilterGroup()
	for _, f := range filtersSpec {
		filters.Add(&f.SubscriptionFilter)
	}

	merged, err := mergeEventsFromTargets(ctx, targets, filters, timeout)
	if err != nil {
		return err
	}

	if len(merged) == 0 {
		log.Info().Msgf("no events found")
		return nil
	}

	outBytes, err := json.MarshalIndent(merged, "", "    ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(outPath, outBytes, 0644); err != nil {
		return err
	}

	log.Info().Msgf("successfully saved %d events to %s", len(merged), outPath)

	return nil
}

// ErrNoReachableTargets is returned by Find/DumpFromTargets/CheckPOWLive
// when every target failed to connect or timed out -- as opposed to some
// target(s) responding with zero matching events, which is a legitimate
// (if empty) result, not a failure. Without this distinction, "every
// target was unreachable" and "genuinely no matching events" both surface
// identically as an empty result on stdout with exit 0 -- indistinguishable
// to a script/agent, and silently discarding a condition (e.g. a typo'd
// hostname, a relay that's down) worth surfacing as a retryable failure
// instead.
var ErrNoReachableTargets = errors.New("no target could be reached (every connection failed or timed out)")

// mergeEventsFromTargets fetches events matching filters from every target
// in targets (local store or remote relay), merging and deduplicating by
// event ID across ALL of them -- DumpFromRelays' merge semantics above,
// generalized from a []*url.URL to a *TargetsSpec so it also covers local
// store targets. Unlike Find (which stops at the first target with a
// match), this is for callers that want the full matching set regardless of
// which target(s) happen to hold it (e.g. CheckPOWLive auditing PoW
// compliance across several relays). An unreachable remote relay is logged
// and skipped rather than failing the whole fetch, matching Find/
// DumpFromRelays -- unless every target ends up skipped this way, in which
// case ErrNoReachableTargets is returned rather than silently reporting an
// empty result as if it were a genuine (if empty) match. timeout bounds how
// long any single remote target gets before it's treated the same way --
// logged and skipped -- so one slow or unresponsive relay can't eat the
// whole call; 0 waits indefinitely.
func mergeEventsFromTargets(ctx context.Context, targets *TargetsSpec, filters *nip01.SubscriptionFilterGroup, timeout time.Duration) ([]*nip01.Event, error) {
	seen := make(map[string]struct{})
	var merged []*nip01.Event
	reached := false

	for _, target := range targets.Relays {
		if target.killed {
			continue
		}

		var events []*nip01.Event
		var err error

		switch target.Type {
		case FlOW_LOCAL:
			if err := ensureLocalStoreDir(target); err != nil {
				return nil, fmt.Errorf("failed to create directory for local store: %w", err)
			}
			log.Info().Msgf("querying %s", target.Path)
			events, err = relayclient.ReadEventsFromStore(ctx, target.Path, filters)
			if err != nil {
				return nil, err
			}

		case FlOW_REMOTE:
			log.Info().Msgf("querying %s", target.relayURI.Host)
			events, err = readEventsWithFallback(ctx, timeout, target.relayURI, target.relayFallbackURI, filters)
			if err != nil {
				var connErr *relayclient.ConnectionError
				if errors.As(err, &connErr) {
					log.Error().Err(connErr).Msg("target unreachable, skipping")
					continue
				}
				if errors.Is(err, context.DeadlineExceeded) {
					log.Error().Msgf("%s: no response after %s, skipping", target.relayURI.Host, timeout)
					continue
				}
				return nil, err
			}
		}

		reached = true
		for _, ev := range events {
			if _, ok := seen[ev.ID]; ok {
				continue
			}
			seen[ev.ID] = struct{}{}
			merged = append(merged, ev)
		}
	}

	if !reached && len(targets.Relays) > 0 {
		return nil, ErrNoReachableTargets
	}

	return merged, nil
}

func Find(parent context.Context, idFilter *nip01.SubscriptionFilter, filtersSpec []*FilterSpec, targets *TargetsSpec, savePath string, timeout time.Duration) error {

	filters := nip01.NewSubscriptionFilterGroup()
	if idFilter != nil {
		filters.Add(idFilter)
	}
	for _, f := range filtersSpec {
		filters.Add(&f.SubscriptionFilter)
	}

	var events []*nip01.Event
	var err error
	reached := false

loop:
	for _, relay := range targets.Relays {
		if relay.killed {
			continue
		}

		switch relay.Type {
		case FlOW_LOCAL:
			if err := ensureLocalStoreDir(relay); err != nil {
				return fmt.Errorf("failed to create directory for local store: %w", err)
			}
			log.Info().Msgf("querying %s", relay.Path)
			events, err = relayclient.ReadEventsFromStore(parent, relay.Path, filters)
			if err != nil {
				return err
			}

		case FlOW_REMOTE:
			log.Info().Msgf("querying %s", relay.relayURI.Host)
			events, err = readEventsWithFallback(parent, timeout, relay.relayURI, relay.relayFallbackURI, filters)
			if err != nil {
				var connErr *relayclient.ConnectionError
				if errors.As(err, &connErr) {
					log.Error().Err(connErr).Msg("target unreachable, skipping")
					continue
				}
				if errors.Is(err, context.DeadlineExceeded) {
					log.Error().Msgf("%s: no response after %s, skipping", relay.relayURI.Host, timeout)
					continue
				}
				return err
			}

		}

		reached = true
		if len(events) > 0 {
			break loop
		}
	}

	if !reached && len(targets.Relays) > 0 {
		return ErrNoReachableTargets
	}

	if len(events) == 0 {
		// Narration only, on stderr -- stdout below still gets a valid,
		// parseable JSON value ([]) either way, never bare `null` and
		// never nothing at all, so a script/agent reading stdout doesn't
		// need a special case for an empty result.
		log.Info().Msg("no events found")
		events = []*nip01.Event{}
	}

	data, err := json.MarshalIndent(events, "", "    ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))

	// Matches DumpFromTargets: skip writing savePath when there's nothing
	// to save, rather than leaving a stale/empty file behind.
	if len(savePath) > 0 && len(events) > 0 {
		if err := os.WriteFile(savePath, data, 0644); err != nil {
			return err
		}
		log.Info().Msg("saved")
	}

	return nil
}

func Process(parent context.Context, specFile string, options *ClientOptions) error {

	ctx, cancel := context.WithCancel(parent)
	defer func() {
		cancel()
	}()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	c := NewClient(specFile, options)
	defer c.stop()

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				if event.Op&fsnotify.Write == fsnotify.Write {
					c.restart()
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				c.errors <- err

			}

		}
	}()

	if err = watcher.Add(specFile); err != nil {
		return err
	}

	if err := c.init(); err != nil {
		return err
	}

	completed := make(chan interface{})
	go func() {
		c.run(ctx, completed)
	}()

	select {
	case <-ctx.Done():
		// Don't return until c.run() actually finishes its graceful
		// shutdown (Stream.Close, including the recovery flush) —
		// otherwise the process could exit mid-flush.
		<-completed
	case <-completed:
	case err := <-c.errors:
		return err
	}

	return nil
}

type ClientOptions struct {
	Quiet *bool

	// StrictPow gates NIP-13 proof-of-work enforcement for stream/sync
	// workflows: nil or false (the default) accepts an event regardless of
	// a missing/insufficient PoW nonce; true rejects it -- see
	// Client.strictPow, Stream.strictPow, and SyncModule.strictPow.
	StrictPow *bool

	LogLevel tui.LogLevel
}

type Client struct {
	app *tui.App

	specPath string
	options  *ClientOptions

	errors chan error
	stopCh chan interface{}
	wg     sync.WaitGroup

	rs *RootSpec

	module Module
	raw    bool
}

func NewClient(specFile string, options *ClientOptions) *Client {
	c := &Client{
		errors:   make(chan error),
		stopCh:   make(chan interface{}),
		specPath: specFile,
		options:  options,
	}
	return c
}

func (c *Client) init() error {
	rs, err := loadSpecFromYaml(c.specPath)
	if err != nil {
		return fmt.Errorf("failed to process spec file %s: %w", c.specPath, err)
	}

	c.rs = rs

	if spec, ok := rs.Spec.(*StreamSpec); ok {
		c.raw = spec.Raw
	}

	if !c.raw && c.headless() {
		// Only StreamSpec's TUI usage (render()'s *StreamSpec case, guarded
		// by "!c.raw && c.app != nil") tolerates a nil c.app -- InspectSpec
		// and SyncSpec load a board (or, for Sync, even fetch the module's
		// logger) unconditionally from c.app further down in render(),
		// which would nil-pointer-panic instead of just degrading. Fail
		// fast here, before any goroutines start, rather than let that
		// happen.
		switch rs.Spec.(type) {
		case *InspectSpec, *SyncSpec:
			return fmt.Errorf("this workflow's kind requires an interactive terminal (TUI) and can't run headlessly yet; rerun in a terminal, or use a stream workflow (with raw: true) for unattended/agent use")
		}
	}

	if !c.raw && !c.headless() && c.app == nil {
		c.app = tui.NewApp().Init()
		if c.options != nil {
			c.app.Logger().Level = c.options.LogLevel
		}
	}

	return nil
}

// headless reports whether the TUI must be skipped: either the caller
// explicitly asked for it (--quiet), or stdout isn't a real terminal, which
// tcell (the TUI's rendering library) requires -- an agent or script
// driving apply without a pty would otherwise hang or error deep inside
// tcell's screen initialization instead of getting a clear, immediate
// answer.
func (c *Client) headless() bool {
	if c.shouldBeQuiet() {
		return true
	}
	return !term.IsTerminal(int(os.Stdout.Fd()))
}

func (c *Client) run(ctx context.Context, completed chan<- interface{}) {

	if !c.raw && c.app != nil {
		// The TUI takes exclusive control of the terminal via tcell, so any
		// concurrent write to the console (e.g. log.Info from stream.go or
		// recovery.go, which run in the background goroutine started
		// below) would corrupt its rendering. Suspend for the whole TUI
		// lifetime, not just around app.Run(), since c.render below already
		// starts logging before the TUI event loop begins.
		common.SuspendConsole()
		defer common.ResumeConsole()

		// A panic while the TUI owns the terminal would otherwise print
		// straight into the middle of its rendering (or vanish into the
		// alternate screen entirely); redirect it to the crash log instead,
		// for exactly as long as the TUI is running.
		if common.CrashLogPath != "" {
			if restore, err := common.RedirectStderrToCrashLog(common.CrashLogPath); err == nil {
				defer restore()
			}
		}

		c.app.RegisterCallback(func() {
			if err := c.init(); err != nil {
				c.app.Error(fmt.Sprintf("Settings have changed. %s", err.Error()))
			} else {
				c.stopCh <- true
				c.wg.Wait()
				c.render(ctx)
			}
		}, func() {
			c.save()
		})
	}

	go c.render(ctx)

	if !c.raw && c.app != nil {
		// The TUI only quits via c.app.Stop() (normally triggered by its own
		// Ctrl+C key handling). Wire external cancellation (e.g. a SIGTERM,
		// via signal.NotifyContext up in cli/ncli/apply.go) to the same
		// path, so the graceful shutdown below (which runs Stream.Close and
		// the recovery flush) is actually reached instead of the process
		// exiting mid-run.
		go func() {
			<-ctx.Done()
			c.app.Stop()
		}()
		c.app.Run()
	} else {
		<-ctx.Done()
	}

	close(c.stopCh)
	c.wg.Wait()
	close(completed)
}

func (c *Client) save() error {

	if c.module == nil {
		return errors.New("undefined module")
	}

	rs := &RootSpec{
		Kind: c.rs.Kind,
		Spec: c.module.Spec(),
	}
	bytes, err := yaml.Marshal(rs)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	dir := filepath.Dir(c.specPath)
	ext := filepath.Ext(c.specPath)
	base := filepath.Base(c.specPath)
	filename := base[:len(base)-len(ext)]

	newFilename := fmt.Sprintf("%s-%d%s", filename, time.Now().Unix(), ext)
	newSpecPath := filepath.Join(dir, newFilename)

	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	if err := os.WriteFile(newSpecPath, bytes, 0644); err != nil {
		return fmt.Errorf("failed to write file %s: %w", newSpecPath, err)
	}

	c.app.Alert(fmt.Sprintf("file saved to %s", newSpecPath))

	return nil
}

func (c *Client) restart() {
	if c.app == nil || c.raw {
		return
	}
	c.app.Reload()
}

func (c *Client) render(parent context.Context) {

	c.wg.Add(1)
	defer c.wg.Done()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	switch spec := c.rs.Spec.(type) {

	case *StreamSpec:

		s, err := NewStream(spec, c.strictPow(spec.StrictPow))
		if err != nil {
			c.errors <- err
			return
		}

		if c.raw {
			s.sc.logger.Raw = true
		}

		c.module = s
		defer s.Close()

		up, down := s.Sync(ctx)

		if !c.raw && c.app != nil {
			streamBoard := tui.NewStreamBoard(c.app, ctx, &up, &down, s.sc.logger)
			c.app.Load(streamBoard)
		}

	case *InspectSpec:

		var events *tui.EventTable
		if !c.raw && c.app != nil {
			sessionWriter := NewEventSessionWriter(c.specPath)
			events = tui.NewEventTable(c.app, func(event *nip01.Event) error {
				_, err := sessionWriter.Save(event)
				return err
			})
		}

		i, err := NewInspector(ctx, spec, events)

		if err != nil {
			c.errors <- err
			return
		}
		c.module = i
		defer i.Close()

		if !c.raw && c.app != nil {
			board := tui.NewInspectBoard(c.app, ctx, &i.metrics, i.sc.logger, events)
			c.app.Load(board)
		}

	case *SyncSpec:

		sm, err := NewSyncModule(spec, c.app.Logger(), c.strictPow(spec.StrictPow))
		if err != nil {
			c.errors <- err
			return
		}
		c.module = sm
		defer sm.Close()

		logger, err := sm.Run(ctx)
		if err != nil {
			c.errors <- err
			return
		}

		board := tui.NewSyncBoard(c.app, ctx, logger)
		c.app.Load(board)

	default:
		c.errors <- errors.New("unsupported spec")
	}

	<-c.stopCh
}

func (c *Client) stop() {
	if c.app != nil {
		c.app.Stop()
	}
}

func (c *Client) shouldBeQuiet() bool {
	if c.options.Quiet != nil {
		return *c.options.Quiet
	}
	return false
}

// strictPow resolves the effective --strict-pow setting for the current
// run: the CLI flag, when passed explicitly, overrides whatever the spec
// file itself declares (specDefault, i.e. StreamSpec.StrictPow /
// SyncSpec.StrictPow); otherwise the spec's own value is used as-is.
func (c *Client) strictPow(specDefault bool) bool {
	if c.options.StrictPow != nil {
		return *c.options.StrictPow
	}
	return specDefault
}

type Module interface {
	Spec() Spec
}

// connectRelayWithFallback dials primary; if that fails to even connect
// (not a later read/write error once connected) and fallback is non-nil --
// meaning the relay input had no explicit ws(s):// scheme, see
// resolveRelayURL -- it retries once against fallback before giving up.
// cfg may be nil (relayclient.NewConnection fills in its own defaults).
func connectRelayWithFallback(ctx context.Context, primary, fallback *url.URL, cfg *relayclient.ConnectionConfig) (*relayclient.Connection, error) {
	conn, err := relayclient.NewConnection(ctx, primary, cfg)
	if err == nil || fallback == nil {
		return conn, err
	}
	var connErr *relayclient.ConnectionError
	if !errors.As(err, &connErr) {
		return conn, err
	}
	return relayclient.NewConnection(ctx, fallback, cfg)
}

// readEventsWithFallback is connectRelayWithFallback's counterpart for the
// one-shot ReadEventsFromRelay helper used by find/dump's target loop. If
// timeout > 0, primary and (if tried) fallback each get their own budget --
// a slow primary times out on its own deadline rather than one shared with
// fallback, and doesn't fall back on a timeout at all (only on a dial/
// connection failure -- see connectRelayWithFallback), matching
// readEventsWithTimeout's ctx.DeadlineExceeded, which isn't a
// *relayclient.ConnectionError.
func readEventsWithFallback(ctx context.Context, timeout time.Duration, primary, fallback *url.URL, filters *nip01.SubscriptionFilterGroup) ([]*nip01.Event, error) {
	events, err := readEventsWithTimeout(ctx, timeout, primary, filters)
	if err == nil || fallback == nil {
		return events, err
	}
	var connErr *relayclient.ConnectionError
	if !errors.As(err, &connErr) {
		return events, err
	}
	return readEventsWithTimeout(ctx, timeout, fallback, filters)
}

// readEventsWithTimeout bounds a single ReadEventsFromRelay call to timeout
// (0 disables the bound, waiting on ctx alone) -- so a relay that accepts a
// subscription and then never sends EOSE or an error can't hang the caller
// past this deadline.
func readEventsWithTimeout(ctx context.Context, timeout time.Duration, relayURL *url.URL, filters *nip01.SubscriptionFilterGroup) ([]*nip01.Event, error) {
	if timeout <= 0 {
		return relayclient.ReadEventsFromRelay(ctx, relayURL, filters)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return relayclient.ReadEventsFromRelay(ctx, relayURL, filters)
}

func GetPublicKey(privKey string) (string, error) {
	b, err := hex.DecodeString(privKey)
	if err != nil {
		return "", err
	}

	_, pk := btcec.PrivKeyFromBytes(b)
	return hex.EncodeToString(schnorr.SerializePubKey(pk)), nil
}
