package huddle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/ncli/client/tui"
	"github.com/ohstr/ncli/huddleaudio"
	"github.com/ohstr/ncli/huddleclient"
	"github.com/ohstr/nmilat/huddle/wire"
	"github.com/ohstr/nmilat/huddle/wsaudio"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"
)

// NewHuddleCommand builds the `ncli huddle` command tree.
func NewHuddleCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "huddle",
		Short: "Join a relay's voice rooms",
		Long: `Join a voice room hosted by a relay running "ncli relay" with huddles enabled.

Shows the live roster and who is speaking, drawn from the level telemetry every
audio frame already carries. It never captures a microphone, so joining puts no
audio into the room.

Hearing the call needs a build with audio output: the release binaries are built
without cgo and the only maintained output library needs it on Linux, so
playback is compiled in only under "-tags huddleaudio". Without it the roster
still works and the status line says "watching only".`,
		Example: `  ncli huddle join standup --relay wss://relay.example
  ncli huddle join standup --relay ws://localhost:7777 --identity satoshi`,
		RunE: common.RequireSubcommand,
	}

	cmd.PersistentFlags().String("identity", "", "Identity to authenticate with -- vault label, nsec, npub, hex, nprofile, or nip-05")

	cmd.AddCommand(newJoinCommand())
	cmd.AddCommand(newListCommand())
	cmd.AddCommand(newSpacesCommand())

	return cmd
}

func newJoinCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "join <room>",
		Short: "Join a huddle and watch who is talking",
		Long: `Authenticate to the relay over NIP-42, join <room>, and open the roster view.

Ctrl+C (or q) asks before leaving, so a stray keystroke does not drop the
call.`,
		Args: common.ExactArgs(1),
		RunE: runJoin,
	}

	cmd.Flags().String("relay", "", "Relay hosting the huddle (falls back to the first configured prefs relay)")

	return cmd
}

func runJoin(cmd *cobra.Command, args []string) error {
	room := args[0]

	// Checked before anything else: the roster view is the whole command, so
	// without a terminal there is nothing to fall back to. Refusing here beats
	// authenticating, joining a real room, and only then finding there is
	// nowhere to draw it.
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return common.UnsupportedError(cmd, "huddle join",
			errors.New("the roster view needs a terminal, and stdout is not one"))
	}

	identityFlag, _ := cmd.Flags().GetString("identity")
	privKeyHex, err := resolveIdentity(cmd, identityFlag)
	if err != nil {
		return err
	}

	relayFlag, _ := cmd.Flags().GetString("relay")
	relayURL, err := resolveRelay(cmd, relayFlag)
	if err != nil {
		return err
	}

	endpoint, err := Endpoint(relayURL, room)
	if err != nil {
		return common.InvalidInputError(cmd, room, err)
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Dial before the TUI takes the terminal: a refused or unreachable relay
	// should print one plain error line, not flash a board for an instant and
	// then vanish with nowhere to show why.
	var hc *huddleclient.Client
	dialErr := common.WithSpinner(cmd, fmt.Sprintf("Joining %s", room), func() error {
		var err error
		hc, err = huddleclient.Dial(ctx, huddleclient.Config{
			Endpoint: endpoint,
			RelayURL: relayURL.String(),
			PrivKey:  privKeyHex,
		})
		return err
	})
	if dialErr != nil {
		// A relay with huddles switched off never mounts the endpoint, so the
		// upgrade 404s instead of being refused with a code. That is the most
		// common failure by far, and "bad handshake" explains none of it.
		var notUpgraded *huddleclient.DialError
		if errors.As(dialErr, &notUpgraded) && notUpgraded.Status == http.StatusNotFound {
			return common.RuntimeError(cmd, fmt.Errorf(
				"%s has no huddle endpoint: the relay is not running with huddles enabled", relayURL.Host))
		}

		var refused *huddleclient.RefusedError
		if errors.As(dialErr, &refused) {
			if hint := refusalHint(refused); hint != "" {
				return common.RuntimeError(cmd, fmt.Errorf("%s refused the join: %s", relayURL.Host, hint))
			}
			return common.RuntimeError(cmd, fmt.Errorf("%s refused the join: %w", relayURL.Host, refused))
		}
		return common.NetworkError(cmd, endpoint, dialErr)
	}
	defer func() { _ = hc.Close() }()

	return runBoard(cmd, ctx, hc, room)
}

// runBoard owns the TUI for as long as it is on screen. The board is cheap to
// build (no blocking calls -- the dial already happened), so unlike
// cli/bunker's it is constructed up front rather than behind a splash screen.
func runBoard(cmd *cobra.Command, ctx context.Context, hc Client, room string) error {
	app := tui.NewApp().Init()
	app.RegisterCallback(func() {}, func() {})

	// Console logging writes to the same terminal tview is drawing on, so it has
	// to stop for as long as the board owns the screen.
	common.SuspendConsole()
	defer common.ResumeConsole()
	if common.CrashLogPath != "" {
		if restore, err := common.RedirectStderrToCrashLog(common.CrashLogPath); err == nil {
			defer restore()
		}
	}

	board := NewBoard(app, hc, room)

	// Playback is best-effort. A build without the huddleaudio tag, or a machine
	// with no usable device, still gets the roster view -- which is the whole
	// command minus the sound, not a failure. The status line says which it is.
	if player, err := huddleaudio.NewPlayer(); err == nil {
		board.PlayAudio(player)
	} else if !errors.Is(err, huddleaudio.ErrPlaybackUnavailable) {
		// A real device failure is worth a line, unlike the expected
		// "this build has no audio output".
		log.Warn().Err(err).Msg("continuing without audio playback")
	}

	app.Load(board)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go board.Run(runCtx)

	// Bounded by appDone: left unbounded, this goroutine outlives app.Run and
	// calls Stop on a torn-down (or never-started) screen, which panics inside
	// tcell rather than returning an error.
	appDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			app.Stop()
		case <-appDone:
		}
	}()

	err := app.Run()
	close(appDone)
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	// Reported after the TUI has given the terminal back, so it is a visible
	// line rather than something painted under the board and lost.
	if err := hc.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return common.NetworkError(cmd, room, err)
	}
	return nil
}

// resolveIdentity resolves the key that signs the NIP-42 auth event, mirroring
// what cli/bunker's ResolveSignerKey does for the bunker: the --identity flag,
// then the "huddle.identity" config key, then the vault's sole entry when there
// is exactly one. A pubkey-only identity is rejected -- authenticating means
// signing.
func resolveIdentity(cmd *cobra.Command, identityFlag string) (string, error) {
	identity := identityFlag
	if identity == "" {
		identity = viper.GetString("huddle.identity")
	}

	if identity == "" {
		entries, err := client.LoadVaultEntries()
		if err != nil {
			return "", common.RuntimeError(cmd, err)
		}
		switch len(entries) {
		case 0:
			return "", common.InvocationError(cmd, errors.New("--identity is required (or set NCLI_HUDDLE_IDENTITY/huddle.identity): no vault identity to fall back to"))
		case 1:
			identity = entries[0].Label
		default:
			return "", common.InvocationError(cmd, fmt.Errorf("--identity is required (or set NCLI_HUDDLE_IDENTITY/huddle.identity): the vault has %d saved identities, none chosen by default", len(entries)))
		}
	}

	resolved, err := client.ResolveIdentifier(identity)
	if err != nil {
		return "", keyresolve.ClassifyIdentifierError(cmd, identity, err)
	}

	privKeyHex, err := keyresolve.ResolveSigningKey(cmd, false, resolved)
	if err != nil {
		return "", err
	}
	if privKeyHex == "" {
		return "", common.AuthError(cmd, fmt.Errorf("identity %q has no private key available", common.RedactSecretInput(identity)))
	}
	return privKeyHex, nil
}

// resolveRelay honors --relay, falling back to the first configured prefs relay
// -- a huddle lives on one relay, so unlike the repeatable --relay every
// publish/subscribe command takes, there is nothing to fan out to here.
func resolveRelay(cmd *cobra.Command, relayFlag string) (*url.URL, error) {
	if relayFlag != "" {
		u, _, err := client.ResolveRelayURL(relayFlag)
		if err != nil {
			return nil, common.InvalidInputError(cmd, relayFlag, err)
		}
		return u, nil
	}

	urls, err := client.PrefsRelayURLs()
	if err != nil {
		return nil, common.UsageError(cmd, err)
	}
	if len(urls) == 0 {
		return nil, common.InvocationError(cmd, errors.New("--relay is required: no relays are configured"))
	}
	return urls[0], nil
}

// Endpoint builds the huddle audio URL for room on the relay at base. Exported
// so a test can build the same URL the command does rather than reimplement the
// path shape and prove nothing.
func Endpoint(base *url.URL, room string) (string, error) {
	room = strings.TrimSpace(room)
	if room == "" {
		return "", errors.New("room id is empty")
	}
	// The room id becomes a path segment, so a character that would change the
	// path's shape is rejected rather than escaped. Escaping it would leave the
	// client and the relay disagreeing about which room was asked for: the relay
	// matches on the id it receives.
	if strings.ContainsAny(room, "/?#%") {
		return "", fmt.Errorf("room id %q may not contain /, ?, # or %%", room)
	}

	u := *base
	u.Path = strings.TrimSuffix(u.Path, "/") + "/huddle/" + room + "/audio"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// refusalHint turns a relay's refusal code into something worth reading. An
// empty return means the code is unrecognized, so the caller shows the relay's
// own message instead of inventing an explanation for it.
func refusalHint(err *huddleclient.RefusedError) string {
	switch err.Code {
	case wsaudio.CodeAudioUnavailable:
		return "this relay does not have huddles enabled"
	case wsaudio.CodeRoomFull:
		return "the room is at capacity"
	case wsaudio.CodeRoomEnded:
		return "the call has already ended"
	case wsaudio.CodeRoomUnavailable:
		return "the relay could not open the room"
	case wsaudio.CodeUpgradeRequired:
		if err.CurrentVersion != nil {
			return fmt.Sprintf("the room is running protocol v%d, this build speaks v%d",
				*err.CurrentVersion, wire.CurrentProtocolVersion)
		}
		return "the room is running a different protocol version"
	case wsaudio.CodeJoinRejected, wsaudio.CodeAuthFailed:
		return "the relay refused this identity"
	}
	return ""
}
