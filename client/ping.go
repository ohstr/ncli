package client

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/google/uuid"
	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip01"
	"github.com/rs/zerolog/log"
	"golang.org/x/term"
)

// PingResult is one relay's outcome from Ping.
type PingResult struct {
	Relay     string `json:"relay"`
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}

// PingReport is Ping's full result, and the shape printed for --json.
type PingReport struct {
	Results     []*PingResult `json:"results"`
	Checked     int           `json:"checked"`
	Reachable   int           `json:"reachable"`
	Unreachable int           `json:"unreachable"`
}

// AllReachable reports whether every relay Ping checked answered.
func (r *PingReport) AllReachable() bool {
	return r.Unreachable == 0
}

// PingOptions controls Ping's presentation.
type PingOptions struct {
	// JSON suppresses all narration (board or log lines) -- the caller is
	// expected to print the returned report itself, e.g. via
	// common.PrintJSON, so nothing else should touch stdout/stderr first.
	JSON bool
	// Quiet suppresses narration the same way JSON does, but leaves report
	// printing to the caller's normal text path rather than forcing JSON.
	Quiet bool
	// Timeout bounds how long a single relay's connect-and-subscribe gets
	// before it's counted unreachable and Ping moves on; 0 waits on ctx
	// alone.
	Timeout time.Duration
}

func relayWord(n int) string {
	if n == 1 {
		return "relay"
	}
	return "relays"
}

// Ping probes reachability of every remote relay in targets.Relays (local
// store paths have nothing to dial, so they're skipped) by connecting and
// issuing a Limit-1, match-everything subscription -- a bare TCP/WebSocket
// connect isn't enough to confirm the relay actually speaks the protocol.
// There's no caller-supplied filter: checkRelayConnectivity never reads the
// relay's response (see its own doc comment), so a filter's actual content
// has no effect on the result either way -- exposing one would just be
// surface area that looks configurable but silently isn't.
//
// Presentation depends on opts and the calling terminal: an interactive
// tui.ConnectivityBoard when stdout is a real terminal and neither JSON nor
// Quiet is set, plain log narration otherwise (unless JSON, which stays
// silent so the caller's stdout is clean for the returned report). Ping
// always returns a complete report regardless of presentation.
func Ping(ctx context.Context, targets *TargetsSpec, opts PingOptions) *PingReport {

	filters := nip01.NewSubscriptionFilterGroup()
	filters.Add(&nip01.SubscriptionFilter{Limit: 1})

	relays := make([]*FlowSpec, 0, len(targets.Relays))
	for _, t := range targets.Relays {
		if t.Type == FlOW_REMOTE {
			relays = append(relays, t)
		}
	}

	useTUI := !opts.JSON && !opts.Quiet && term.IsTerminal(int(os.Stdout.Fd()))

	var app *tui.App
	var logger connectivityLogger
	switch {
	case useTUI:
		app = tui.NewApp().Init()
		app.RegisterCallback(func() {}, func() {})
		board, l := tui.NewConnectivityBoard(app, ctx)
		app.Load(board)
		logger = l
	case !opts.JSON:
		logger = plainConnectivityLogger{}
	}

	report := &PingReport{Results: make([]*PingResult, 0, len(relays))}

	runChecks := func() {
		if logger != nil {
			logger.SetIndexWidth(len(strconv.Itoa(len(relays))))
			logger.Info(fmt.Sprintf("Checking connectivity for %d %s", len(relays), relayWord(len(relays))), tui.FlowAttr{})
		}

		history := make(map[string]bool)
		for i, r := range relays {
			select {
			case <-ctx.Done():
				return
			default:
			}

			attr := tui.FlowAttr{Index: i + 1, FlagColor: tcell.ColorPurple}
			result := &PingResult{Relay: r.Relay}

			if history[r.Relay] {
				result.Error = "duplicate relay"
				if logger != nil {
					logger.Error(fmt.Errorf("%s: duplicate relay", r.relayURI.Host), attr)
				}
				report.Results = append(report.Results, result)
				report.Checked++
				report.Unreachable++
				continue
			}
			history[r.Relay] = true

			if err := checkRelayConnectivityWithTimeout(ctx, opts.Timeout, r.relayURI, r.relayFallbackURI, filters); err != nil {
				result.Error = err.Error()
				if logger != nil {
					logger.Error(fmt.Errorf("%s: %w", r.relayURI.Host, err), attr)
				}
				report.Unreachable++
			} else {
				result.Reachable = true
				if logger != nil {
					logger.Success(r.relayURI.Host, attr)
				}
				report.Reachable++
			}
			report.Checked++
			report.Results = append(report.Results, result)
		}

		if logger != nil {
			logger.Info(fmt.Sprintf("%d of %d %s reachable", report.Reachable, report.Checked, relayWord(report.Checked)), tui.FlowAttr{})
		}
	}

	if app == nil {
		runChecks()
		return report
	}

	// Mirrors Client.run's TUI lifetime handling: the TUI takes exclusive
	// control of the terminal via tcell, so console writes from runChecks'
	// background goroutine would corrupt its rendering, and a panic while
	// it owns the terminal needs redirecting to the crash log rather than
	// printing into the middle of the alternate screen.
	common.SuspendConsole()
	defer common.ResumeConsole()
	if common.CrashLogPath != "" {
		if restore, err := common.RedirectStderrToCrashLog(common.CrashLogPath); err == nil {
			defer restore()
		}
	}

	go func() {
		<-ctx.Done()
		app.Stop()
	}()
	go runChecks()
	app.Run()

	return report
}

// connectivityLogger is satisfied by *tui.FlowLogger (structurally, without
// client/tui needing to know about it) and by plainConnectivityLogger --
// the headless fallback used when there's no TUI to render into.
type connectivityLogger interface {
	SetIndexWidth(int)
	Info(string, tui.FlowAttr)
	Error(error, tui.FlowAttr)
	Success(string, tui.FlowAttr)
}

type plainConnectivityLogger struct{}

func (plainConnectivityLogger) SetIndexWidth(int) {}
func (plainConnectivityLogger) Info(msg string, _ tui.FlowAttr) {
	log.Info().Msg(msg)
}
func (plainConnectivityLogger) Error(err error, _ tui.FlowAttr) {
	log.Error().Err(err).Msg("connectivity check failed")
}
func (plainConnectivityLogger) Success(host string, _ tui.FlowAttr) {
	log.Info().Str("relay", host).Msg("connectivity OK")
}

// checkRelayConnectivityWithTimeout is checkRelayConnectivity bounded by
// timeout (0 disables the bound, waiting on ctx alone) -- mirrors
// readEventsWithTimeout's pattern so one slow/unresponsive relay can't hang
// Ping past its own deadline.
func checkRelayConnectivityWithTimeout(ctx context.Context, timeout time.Duration, relayURL, fallbackURL *url.URL, filters *nip01.SubscriptionFilterGroup) error {
	if timeout <= 0 {
		return checkRelayConnectivity(ctx, relayURL, fallbackURL, filters)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return checkRelayConnectivity(ctx, relayURL, fallbackURL, filters)
}

// checkRelayConnectivity dials relayURL (falling back to fallbackURL, same
// as every other dial site) and writes a REQ for filters, but never reads
// the connection afterward -- success only means the WebSocket handshake
// and that write both went through, not that the relay sent back anything
// resembling a valid NIP-01 response. filters' content is consequently
// inert to the result; Ping always passes a fixed Limit-1 match-everything
// filter rather than exposing one to callers.
func checkRelayConnectivity(ctx context.Context, relayURL, fallbackURL *url.URL, filters *nip01.SubscriptionFilterGroup) error {
	conn, err := connectRelayWithFallback(ctx, relayURL, fallbackURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	conn.SubscribeWithID(uuid.NewString(), filters)

	return nil
}
