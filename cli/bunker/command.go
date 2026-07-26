package bunker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/nmilat/nip46"
	"github.com/ohstr/nmilat/utils"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/natefinch/lumberjack.v2"
)

// NewBunkerCommand builds the `ncli bunker` command tree: the bare command
// (ensure a daemon is running, attach the TUI to it), "attach" (reattach
// only, never spawns), "status"/"stop"/"sessions"/"connect" (scriptable,
// one-shot, `--json`-aware per ncli convention), and a hidden re-exec
// target ("__daemon") that actually runs the daemon loop.
func NewBunkerCommand() *cobra.Command {
	var identity string
	var relayFlags []string

	cmd := &cobra.Command{
		Use:   "bunker",
		Short: "Run ncli as a NIP-46 remote signer",
		Long: `Run ncli as a NIP-46 "bunker": listen on one or more relays for signing
requests from other Nostr clients, approve or reject them from a live
TUI, and remember per-app decisions so you aren't re-prompted every time.

On Linux/macOS this starts (or reattaches to) a background daemon that
keeps running after the TUI is closed with "b" or "q" -- reattach any
time with "ncli bunker attach". On Windows the TUI runs directly with no
background support.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireInteractive(cmd); err != nil {
				return err
			}

			// Already running (from an earlier "ncli bunker") -- just
			// attach, ignoring --identity/--relay for this invocation
			// (the same "attach to whatever's already there" behavior
			// tmux gives a bare `tmux` with a session already up).
			if existing, err := DialIPC(SocketPath(), 500*time.Millisecond); err == nil {
				return runTUI(cmd, existing, nil)
			}

			bunkerClient, cancel, err := ensureDaemonRunning(cmd, identity, relayFlags)
			if err != nil {
				return err
			}
			return runTUI(cmd, bunkerClient, cancel)
		},
	}

	cmd.Flags().StringVar(&identity, "identity", "", "Identity to sign with -- vault label, nsec, npub, hex, nprofile, or nip-05 (also settable via NCLI_BUNKER_IDENTITY or bunker.identity)")
	cmd.Flags().StringArrayVar(&relayFlags, "relay", nil, "Relay to listen on (repeatable; falls back to configured prefs relays)")

	cmd.AddCommand(newAttachCommand())
	cmd.AddCommand(newStatusCommand())
	cmd.AddCommand(newStopCommand())
	cmd.AddCommand(newSessionsCommand())
	cmd.AddCommand(newHistoryCommand())
	cmd.AddCommand(newConnectCommand())
	cmd.AddCommand(newHiddenDaemonCommand())

	return cmd
}

func requireInteractive(cmd *cobra.Command) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	if jsonMode || !interactive {
		return common.UsageError(cmd, errors.New("this needs an interactive terminal (TUI); use `ncli bunker status`/`sessions`/`connect` for scripted access to an already-running daemon"))
	}
	return nil
}

func newAttachCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "attach",
		Short: "Reattach the TUI to an already-running background bunker daemon",
		Long: `Reconnect the interactive TUI to a bunker daemon already started with
"ncli bunker" and left running in the background. Never starts one
itself -- fails if none is running (use "ncli bunker" for that).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireInteractive(cmd); err != nil {
				return err
			}

			bunkerClient, err := DialIPC(SocketPath(), 2*time.Second)
			if err != nil {
				if runtime.GOOS == "windows" {
					return common.UsageError(cmd, errors.New("background/attach is not supported on windows; run `ncli bunker` directly instead"))
				}
				return common.NotFoundError(cmd, "", errors.New("no bunker daemon is running; start one with `ncli bunker`"))
			}
			defer bunkerClient.Close()

			return runTUI(cmd, bunkerClient, nil)
		},
	}
}

func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether a bunker daemon is running",
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode, _ := cmd.Flags().GetBool("json")

			bunkerClient, err := DialIPC(SocketPath(), 2*time.Second)
			if err != nil {
				if jsonMode {
					common.PrintJSON(map[string]any{"running": false})
					return nil
				}
				fmt.Println("not running")
				return nil
			}
			defer bunkerClient.Close()

			st, err := bunkerClient.Status()
			if err != nil {
				return common.RuntimeError(cmd, err)
			}

			if jsonMode {
				common.PrintJSON(map[string]any{
					"running":        true,
					"identity_pub":   st.IdentityPub,
					"identity_name":  st.IdentityName,
					"identity_nip05": st.IdentityNip05,
					"vault_label":    st.VaultLabel,
					"relays":         st.Relays,
					"relay_statuses": st.RelayStatuses,
					"pending_count":  st.PendingCount,
					"session_count":  st.SessionCount,
				})
				return nil
			}

			fmt.Println("running")
			printIdentityAndRelays(st)
			fmt.Println("pending: ", st.PendingCount)
			fmt.Println("sessions:", st.SessionCount)
			return nil
		},
	}
}

// printIdentityAndRelays writes st's identity and per-relay connection
// state in the shared plain-text shape `status` and `stop` both use --
// npub (fullNpub, not the raw hex: id.go's own "npub:" precedent, and
// simply more useful to read/copy than 64 hex characters), then
// name/nip05/vault only when actually resolved, then relays.
//
// Deliberately does NOT also print st.Relays (the bare configured URL
// list) alongside the per-relay status below: Daemon.RelayStatuses
// already derives one entry per configured relay (same URLs, same
// order -- see daemon.go), so printing both just repeats every URL
// twice for no added information, which is exactly what this replaced.
func printIdentityAndRelays(st StatusInfo) {
	fmt.Println("npub:    ", fullNpub(st.IdentityPub))
	if st.IdentityName != "" {
		fmt.Println("name:    ", st.IdentityName)
	}
	if st.IdentityNip05 != "" {
		fmt.Println("nip05:   ", st.IdentityNip05)
	}
	if st.VaultLabel != "" {
		fmt.Println("vault:   ", st.VaultLabel)
	}
	if len(st.RelayStatuses) == 0 {
		fmt.Println("relays:   none configured")
		return
	}
	fmt.Println("relays:")
	for _, rs := range st.RelayStatuses {
		state := "down"
		if rs.Connected {
			state = "connected"
		}
		fmt.Printf("  %-40s %s\n", rs.URL, state)
	}
}

func newStopCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the running bunker daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode, _ := cmd.Flags().GetBool("json")

			bunkerClient, err := DialIPC(SocketPath(), 2*time.Second)
			if err != nil {
				return common.NotFoundError(cmd, "", errors.New("no bunker daemon is running"))
			}
			defer bunkerClient.Close()

			// Fetched before Stop(), not after: Stop() tears down the very
			// daemon Status() would otherwise need to answer, so there's no
			// "identity of what I just stopped" left to ask for once it's
			// gone. statusErr is deliberately non-fatal -- a signer that's
			// somehow unreachable for Status() but still answers Stop() (or
			// vice versa) should still stop; the confirmation just falls
			// back to a bare "stopped" rather than blocking the whole
			// command on a display nicety.
			st, statusErr := bunkerClient.Status()

			if err := bunkerClient.Stop(); err != nil {
				return common.RuntimeError(cmd, err)
			}

			if jsonMode {
				result := map[string]any{"stopped": true}
				if statusErr == nil {
					result["identity_pub"] = st.IdentityPub
					result["identity_name"] = st.IdentityName
					result["identity_nip05"] = st.IdentityNip05
					result["vault_label"] = st.VaultLabel
				}
				common.PrintJSON(result)
				return nil
			}

			fmt.Println("stopped")
			if statusErr == nil {
				printIdentityAndRelays(st)
			}
			return nil
		},
	}
}

func newSessionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Manage remembered per-app permissions",
		RunE:  common.RequireSubcommand,
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List every app with a remembered permission",
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode, _ := cmd.Flags().GetBool("json")

			bunkerClient, err := DialIPC(SocketPath(), 2*time.Second)
			if err != nil {
				return common.NotFoundError(cmd, "", errors.New("no bunker daemon is running"))
			}
			defer bunkerClient.Close()

			sessions, err := bunkerClient.ListSessions()
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
			if jsonMode {
				common.PrintJSON(sessions)
				return nil
			}
			if len(sessions) == 0 {
				fmt.Println("(no remembered sessions)")
				return nil
			}
			for _, s := range sessions {
				fmt.Printf("%s  %s  %s\n", s.Pubkey, labelFor(s), summarizeGrants(s.Grants))
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "revoke <pubkey>",
		Short: "Revoke every remembered permission for one app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode, _ := cmd.Flags().GetBool("json")

			bunkerClient, err := DialIPC(SocketPath(), 2*time.Second)
			if err != nil {
				return common.NotFoundError(cmd, "", errors.New("no bunker daemon is running"))
			}
			defer bunkerClient.Close()

			revoked, err := bunkerClient.Revoke(args[0])
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
			if !revoked {
				return common.NotFoundError(cmd, args[0], fmt.Errorf("no remembered session for %q", args[0]))
			}
			if jsonMode {
				common.PrintJSON(map[string]bool{"revoked": true})
				return nil
			}
			fmt.Println("revoked")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "rename <pubkey> <name>",
		Short: "Set (or clear, with \"\") a trusted app's display name",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode, _ := cmd.Flags().GetBool("json")

			bunkerClient, err := DialIPC(SocketPath(), 2*time.Second)
			if err != nil {
				return common.NotFoundError(cmd, "", errors.New("no bunker daemon is running"))
			}
			defer bunkerClient.Close()

			updated, err := bunkerClient.SetName(args[0], args[1])
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
			if !updated {
				return common.NotFoundError(cmd, args[0], fmt.Errorf("no remembered session for %q", args[0]))
			}
			if jsonMode {
				common.PrintJSON(map[string]bool{"updated": true})
				return nil
			}
			fmt.Println("updated")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "grants <pubkey>",
		Short: "List one trusted app's remembered permissions individually",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode, _ := cmd.Flags().GetBool("json")

			bunkerClient, err := DialIPC(SocketPath(), 2*time.Second)
			if err != nil {
				return common.NotFoundError(cmd, "", errors.New("no bunker daemon is running"))
			}
			defer bunkerClient.Close()

			sessions, err := bunkerClient.ListSessions()
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
			var found *Session
			for i := range sessions {
				if sessions[i].Pubkey == args[0] {
					found = &sessions[i]
					break
				}
			}
			if found == nil {
				return common.NotFoundError(cmd, args[0], fmt.Errorf("no remembered session for %q", args[0]))
			}
			if jsonMode {
				common.PrintJSON(found.Grants)
				return nil
			}
			if len(found.Grants) == 0 {
				fmt.Println("(no remembered grants)")
				return nil
			}
			for _, g := range found.Grants {
				statusText, _ := grantStatusLabel(g)
				fmt.Printf("%-9s %-45s %s\n", statusText, grantScopeLabel(g), grantDurationLabel(g))
			}
			return nil
		},
	})

	revokeGrantCmd := &cobra.Command{
		Use:   "revoke-grant <pubkey>",
		Short: "Revoke one remembered permission for an app, leaving the rest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode, _ := cmd.Flags().GetBool("json")
			method, _ := cmd.Flags().GetString("method")

			// kind stays nil ("--kind not given") unless the flag was
			// actually set -- 0 is itself a meaningful kind (profile
			// metadata), so its zero value can't double as "unset."
			var kind *int
			if cmd.Flags().Changed("kind") {
				k, _ := cmd.Flags().GetInt("kind")
				kind = &k
			}

			bunkerClient, err := DialIPC(SocketPath(), 2*time.Second)
			if err != nil {
				return common.NotFoundError(cmd, "", errors.New("no bunker daemon is running"))
			}
			defer bunkerClient.Close()

			revoked, err := bunkerClient.RevokeGrant(args[0], method, kind)
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
			if !revoked {
				return common.NotFoundError(cmd, args[0], fmt.Errorf("no matching grant (method=%q) for %q", method, args[0]))
			}
			if jsonMode {
				common.PrintJSON(map[string]bool{"revoked": true})
				return nil
			}
			fmt.Println("revoked")
			return nil
		},
	}
	revokeGrantCmd.Flags().String("method", "", "NIP-46 method the grant covers (e.g. sign_event, ping, connect)")
	revokeGrantCmd.MarkFlagRequired("method")
	revokeGrantCmd.Flags().Int("kind", 0, "Event kind, for a sign_event grant (omit for the any-kind grant)")
	cmd.AddCommand(revokeGrantCmd)

	return cmd
}

// newHistoryCommand is a flat command (no subcommands, unlike sessions'
// list/revoke pair) since History is read-only end to end -- there's no
// "undo a past decision" action to give it a group for.
func newHistoryCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "history",
		Short: "List recently resolved requests (approved/rejected/expired)",
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode, _ := cmd.Flags().GetBool("json")

			bunkerClient, err := DialIPC(SocketPath(), 2*time.Second)
			if err != nil {
				return common.NotFoundError(cmd, "", errors.New("no bunker daemon is running"))
			}
			defer bunkerClient.Close()

			history, err := bunkerClient.History()
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
			if jsonMode {
				common.PrintJSON(history)
				return nil
			}
			if len(history) == 0 {
				fmt.Println("(no resolved requests yet)")
				return nil
			}

			// Best-effort: a session lookup failure shouldn't block
			// printing history, just fall back to labelFor's own
			// shortHex(pubkey) below (byPub stays empty either way).
			sessions, _ := bunkerClient.ListSessions()
			byPub := make(map[string]Session, len(sessions))
			for _, s := range sessions {
				byPub[s.Pubkey] = s
			}

			for _, h := range history {
				kindCol := "-"
				if h.Method == nip46.MethodSignEvent {
					kindCol = strconv.Itoa(h.Kind)
				}
				// historyStatus's own text return is already plain (no
				// tview color-tag markup) -- built that way from the start
				// for exactly this reuse, not just board.go's own coloring.
				status, _ := historyStatus(h)
				label := shortHex(h.ClientKey)
				if s, ok := byPub[h.ClientKey]; ok {
					label = labelFor(s)
				}
				fmt.Printf("%s  %s  %-14s  %-4s  %s\n",
					h.ResolvedAt.Local().Format("15:04:05"), label, h.Method, kindCol, status)
			}
			return nil
		},
	}
}

func newConnectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect [nostrconnect-uri]",
		Short: "Start a pairing with a running bunker daemon",
		Long: `With no argument, generates and prints a fresh bunker:// URI for another
Nostr app to connect to. Given a nostrconnect:// URI, initiates that
pairing instead, blocking until the client confirms or it times out.

--grants <file> pre-authorizes the app that completes this pairing with a
declared set of permissions (see examples/bunker/ for the YAML shape),
instead of prompting interactively on first use. "ncli bunker sessions
grants <pubkey>" shows what actually landed once paired.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode, _ := cmd.Flags().GetBool("json")

			var spec *GrantSpec
			if grantsPath, _ := cmd.Flags().GetString("grants"); grantsPath != "" {
				var err error
				spec, err = LoadGrantSpec(grantsPath)
				if err != nil {
					return common.InvalidInputError(cmd, grantsPath, err)
				}
			}

			bunkerClient, err := DialIPC(SocketPath(), 2*time.Second)
			if err != nil {
				return common.NotFoundError(cmd, "", errors.New("no bunker daemon is running; start one with `ncli bunker`"))
			}
			defer bunkerClient.Close()

			uri := ""
			if len(args) > 0 {
				uri = args[0]
			}
			result, err := bunkerClient.Connect(uri, spec)
			if err != nil {
				return common.InvalidInputError(cmd, "", err)
			}
			if jsonMode {
				common.PrintJSON(map[string]string{"result": result})
				return nil
			}
			fmt.Println(result)
			return nil
		},
	}
	cmd.Flags().String("grants", "", `Path to a "kind: bunker" YAML spec declaring grants to apply automatically once this pairing completes (see examples/bunker/)`)
	cmd.MarkFlagFilename("grants", "yaml", "yml")
	return cmd
}

func newHiddenDaemonCommand() *cobra.Command {
	var relayFlags []string

	cmd := &cobra.Command{
		Use:    daemonHiddenArg,
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			privKeyHex, vaultLabel, err := ReadIdentityKeyFromFD3()
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
			return runDaemonProcess(cmd, privKeyHex, vaultLabel, relayFlags)
		},
	}
	cmd.Flags().StringArrayVar(&relayFlags, "relay", nil, "")
	cmd.Flags().MarkHidden("relay")
	return cmd
}

// runDaemonProcess is the detached child's entire lifetime: build the
// Daemon core, bind the control socket, and run both until SIGINT/SIGTERM
// (the same signal.NotifyContext house pattern every other ncli command
// uses for graceful shutdown -- dump.go/ping.go/miner.go/apply.go).
func runDaemonProcess(cmd *cobra.Command, privKeyHex, vaultLabel string, relays []string) error {
	pubKeyHex, err := utils.GetPublicKey(privKeyHex)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	store, err := LoadStore(SessionsPath())
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	queue := NewQueue(0, 0)

	eventLog, initialHistory, err := LoadEventLog(EventLogPath())
	if err != nil {
		return common.RuntimeError(cmd, err)
	}
	defer eventLog.Close()

	logPath := filepath.Join(common.AppConfigDir(), "bunker", "daemon.log")
	logWriter := &lumberjack.Logger{Filename: logPath, MaxSize: 20, MaxBackups: 3, MaxAge: 28, Compress: true}
	defer logWriter.Close()

	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	daemon := NewDaemon(DaemonConfig{
		IdentityPriv:   privKeyHex,
		IdentityPub:    pubKeyHex,
		VaultLabel:     vaultLabel,
		Relays:         relays,
		Store:          store,
		Queue:          queue,
		EventLog:       eventLog,
		InitialHistory: initialHistory,
		OnLog: func(format string, args ...any) {
			fmt.Fprintf(logWriter, "%s "+format+"\n", append([]any{time.Now().Format(time.RFC3339)}, args...)...)
		},
	})

	listener, err := Listen(SocketPath())
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	local := newLocalClient(daemon, time.Now(), cancel)
	server := NewServer(listener, local)
	go server.Serve(ctx)

	return daemon.Run(ctx)
}

// ensureDaemonRunning resolves the signer identity (prompting for a vault
// password if needed -- safe here since requireInteractive already ruled
// out --json/non-interactive callers before this is ever reached), then
// either spawns a detached background daemon and dials it (Unix) or, if
// spawnDaemon reports ErrBackgroundUnsupported (Windows), runs the daemon
// core directly in this same process instead.
func ensureDaemonRunning(cmd *cobra.Command, identityFlag string, relayFlags []string) (BunkerClient, context.CancelFunc, error) {
	privKeyHex, pubKeyHex, vaultLabel, err := ResolveSignerKey(cmd, false, identityFlag)
	if err != nil {
		return nil, nil, err
	}

	relays, err := resolveRelays(cmd, relayFlags)
	if err != nil {
		return nil, nil, err
	}

	logPath := filepath.Join(common.AppConfigDir(), "bunker", "daemon.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return nil, nil, common.RuntimeError(cmd, err)
	}

	err = spawnDaemon(privKeyHex, vaultLabel, relays, logPath, SocketPath(), 10*time.Second)
	if errors.Is(err, ErrBackgroundUnsupported) {
		return runInProcess(pubKeyHex, privKeyHex, vaultLabel, relays)
	}
	if err != nil {
		return nil, nil, common.RuntimeError(cmd, err)
	}

	bunkerClient, err := DialIPC(SocketPath(), 5*time.Second)
	if err != nil {
		return nil, nil, common.RuntimeError(cmd, err)
	}
	return bunkerClient, nil, nil
}

// runInProcess is the Windows fallback: the daemon core runs as a
// goroutine in this same process rather than a detached child, so there is
// no separate process to attach to later -- cancel stops it, and runTUI
// treats a non-nil cancel as "closing the TUI is the only way to stop
// this daemon."
func runInProcess(pubKeyHex, privKeyHex, vaultLabel string, relays []string) (BunkerClient, context.CancelFunc, error) {
	store, err := LoadStore(SessionsPath())
	if err != nil {
		return nil, nil, err
	}
	queue := NewQueue(0, 0)

	// Not explicitly Closed anywhere here: unlike runDaemonProcess (which
	// blocks in daemon.Run for the process's whole lifetime, giving a
	// natural defer point), this returns immediately with the daemon
	// still running in its own goroutine, so process exit is what
	// eventually closes the underlying file descriptor -- harmless, since
	// AppendAdded/AppendResolved/AppendSigned already fsync per write, so
	// nothing is buffered waiting on a clean Close to become durable.
	eventLog, initialHistory, err := LoadEventLog(EventLogPath())
	if err != nil {
		return nil, nil, err
	}

	daemon := NewDaemon(DaemonConfig{
		IdentityPriv:   privKeyHex,
		IdentityPub:    pubKeyHex,
		VaultLabel:     vaultLabel,
		Relays:         relays,
		Store:          store,
		Queue:          queue,
		EventLog:       eventLog,
		InitialHistory: initialHistory,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go daemon.Run(ctx)

	return newLocalClient(daemon, time.Now(), cancel), cancel, nil
}

// resolveRelays honors explicit --relay flags (validated/normalized the
// same bare-host-friendly way every other ncli relay input is, via
// client.ResolveRelayURL), falling back to the configured prefs relay list
// when none are given -- the same fallback dump/find/miner check already
// use.
func resolveRelays(cmd *cobra.Command, relayFlags []string) ([]string, error) {
	if len(relayFlags) == 0 {
		urls, err := client.PrefsRelayURLs()
		if err != nil {
			return nil, common.UsageError(cmd, err)
		}
		resolved := make([]string, 0, len(urls))
		for _, u := range urls {
			resolved = append(resolved, u.String())
		}
		return resolved, nil
	}

	resolved := make([]string, 0, len(relayFlags))
	for _, r := range relayFlags {
		u, _, err := client.ResolveRelayURL(r)
		if err != nil {
			return nil, common.InvalidInputError(cmd, r, err)
		}
		resolved = append(resolved, u.String())
	}
	return resolved, nil
}

// runTUI drives the interactive board's whole lifetime: the tview.App
// bootstrap (matching client.Client.run's own sequence exactly --
// SuspendConsole/RedirectStderrToCrashLog for as long as the TUI owns the
// terminal, ctx cancellation wired to app.Stop()), then, once the TUI
// exits, stops an in-process (Windows) daemon via cancel -- a spawned
// background daemon (cancel == nil) is completely unaffected by this
// process exiting, by design.
func runTUI(cmd *cobra.Command, bunkerClient BunkerClient, cancel context.CancelFunc) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app := tui.NewApp().Init()

	common.SuspendConsole()
	defer common.ResumeConsole()
	if common.CrashLogPath != "" {
		if restore, err := common.RedirectStderrToCrashLog(common.CrashLogPath); err == nil {
			defer restore()
		}
	}

	canDetach := cancel == nil
	board := NewBunkerBoard(app, ctx, bunkerClient, app.Logger(), canDetach)
	app.Load(board)

	go func() {
		<-ctx.Done()
		app.Stop()
	}()

	if err := app.Run(); err != nil {
		return common.RuntimeError(cmd, err)
	}

	// tview's own Run() has already restored the normal terminal (and
	// exited the alternate screen) by the time it returns, whether that's
	// from a plain user quit or app.Stop() -- so this prints straight to
	// the real terminal, not into a screen buffer about to be discarded.
	// Distinguishing the two matters here specifically because
	// board.DaemonLost() is otherwise invisible: every panel's own
	// Update() silently keeps the TUI's last good snapshot on screen
	// rather than erroring loudly, so without this the daemon dying out
	// from under an attached TUI (`ncli bunker stop` from elsewhere, or a
	// crash) would just leave that stale screen frozen forever with
	// nothing ever explaining why nothing's moving -- see
	// IdentityBar.Update's own doc comment for why that failure is
	// permanent rather than a transient hiccup worth staying quiet about.
	if board.DaemonLost() {
		fmt.Println("bunker daemon is no longer running (stopped or crashed) -- exiting")
	}

	if cancel != nil {
		cancel()
	}
	return nil
}
