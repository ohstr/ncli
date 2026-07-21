package ncli

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip13"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var minerCmd = &cobra.Command{
	Use:   "miner",
	Short: "Mine and verify proof-of-work",
	Long:  `Mine NIP-13 proof-of-work into an unsigned event, or verify PoW on already-mined events.`,
	RunE:  common.RequireSubcommand,
}

var minerMineCmd = &cobra.Command{
	Use:   "mine",
	Short: "Mine proof-of-work into an unsigned event",
	Long: `Mine proof-of-work (NIP-13) for an event, writing the result to --out (or
back to --event in place, with --in-place). Mining runs across multiple CPU
cores by default (see --workers).

The event to mine comes from --event (a structured NIP-01 event file) or,
inline, from --content/--content-file plus --kind/--tag -- pick one, not a
mix. --content/--content-file mode fills in created_at (now) and kind
(default 1) for you and has no pubkey source other than --identity.

If --identity resolves to a private key (an nsec, or a vault label whose
password is available), the mined event is signed automatically before
being written -- no separate signing step needed. A pubkey-only identity
(npub/hex/nprofile/nip-05) mines but cannot sign; this is logged, not a
silent gap in the output.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cmd.ValidateRequiredFlags(); err != nil {
			return common.UsageError(cmd, err)
		}

		hasEvent := cmd.Flags().Changed("event")
		hasContent := cmd.Flags().Changed("content")
		hasContentFile := cmd.Flags().Changed("content-file")

		switch {
		case hasEvent && (hasContent || hasContentFile):
			return common.UsageError(cmd, fmt.Errorf("--event is mutually exclusive with --content/--content-file; a structured event file already declares kind/content/tags"))
		case hasContent && hasContentFile:
			return common.UsageError(cmd, fmt.Errorf("--content and --content-file are mutually exclusive"))
		case !hasEvent && !hasContent && !hasContentFile:
			return common.UsageError(cmd, fmt.Errorf("specify --event, or --content/--content-file to author a draft inline"))
		}
		if hasEvent && cmd.Flags().Changed("kind") {
			return common.UsageError(cmd, fmt.Errorf("--kind only applies to --content/--content-file mode; --event's file already declares kind"))
		}
		if hasEvent && cmd.Flags().Changed("tag") {
			return common.UsageError(cmd, fmt.Errorf("--tag only applies to --content/--content-file mode; --event's file already declares tags"))
		}

		if hasEvent {
			if _, err := validateArgFile(cmd, "event", true, ".json", ".jsonp", ".yaml", ".yml"); err != nil {
				return common.UsageError(cmd, err)
			}
		}
		if hasContentFile {
			if _, err := validateArgFile(cmd, "content-file", true, ".txt"); err != nil {
				return common.UsageError(cmd, err)
			}
		}

		out, _ := cmd.Flags().GetString("out")
		inPlace, _ := cmd.Flags().GetBool("in-place")
		switch {
		case out == "" && !inPlace:
			return common.UsageError(cmd, fmt.Errorf("exactly one of --out or --in-place is required"))
		case out != "" && inPlace:
			return common.UsageError(cmd, fmt.Errorf("--out and --in-place are mutually exclusive"))
		case inPlace && !hasEvent:
			return common.UsageError(cmd, fmt.Errorf("--in-place requires --event; --content/--content-file mode has no input file to overwrite"))
		}
		if out != "" {
			if _, err := validateArgFile(cmd, "out", false, ".json", ".jsonp", ".yaml", ".yml"); err != nil {
				return common.UsageError(cmd, err)
			}
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		jsonMode, _ := cmd.Flags().GetBool("json")

		var draft *nip01.Event
		var eventPath string // set only in --event mode; doubles as --in-place's target

		if cmd.Flags().Changed("event") {
			var err error
			eventPath, err = validateArgFile(cmd, "event", true, ".json", ".jsonp", ".yaml", ".yml")
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
			draft, err = client.LoadDraftEvent(eventPath)
			if err != nil {
				return common.InvalidInputError(cmd, eventPath, err)
			}
		} else {
			content, err := resolveMineContent(cmd)
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
			tagPairs, _ := cmd.Flags().GetStringArray("tag")
			tags, err := parseTagFlags(tagPairs)
			if err != nil {
				return common.InvalidInputError(cmd, "", err)
			}
			kind, _ := cmd.Flags().GetInt("kind")
			draft = nip01.NewUnsignedEvent(kind, "", content, tags...)
		}

		outPath := eventPath
		if out, _ := cmd.Flags().GetString("out"); out != "" {
			var err error
			outPath, err = validateArgFile(cmd, "out", false, ".json", ".jsonp", ".yaml", ".yml")
			if err != nil {
				return common.RuntimeError(cmd, err)
			}
		}

		difficulty, err := cmd.Flags().GetInt("difficulty")
		if err != nil {
			return common.RuntimeError(cmd, err)
		}

		workers, err := cmd.Flags().GetInt("workers")
		if err != nil {
			return common.RuntimeError(cmd, err)
		}

		opts := &client.MineOptions{Workers: workers}

		if identity, _ := cmd.Flags().GetString("identity"); identity != "" {
			resolved, err := client.ResolveIdentifier(identity)
			if err != nil {
				return keyresolve.ClassifyIdentifierError(cmd, identity, err)
			}
			opts.IdentityPubKeyHex = resolved.PubKeyHex

			privKeyHex, err := keyresolve.ResolveSigningKey(cmd, jsonMode, resolved)
			if err != nil {
				return err
			}
			opts.SignPrivKeyHex = privKeyHex
		}

		if draft.PubKey == "" && opts.IdentityPubKeyHex == "" {
			return common.UsageError(cmd, fmt.Errorf("--content/--content-file mode has no pubkey source other than --identity; pass --identity <vault-label|npub|hex|nsec|nprofile|nip-05>"))
		}

		// Progress is periodic log narration, not a prompt -- but it's still
		// interactive-only noise that would otherwise interleave with a
		// script/agent's clean stdout JSON result below, so --json disables
		// it the same way id.go's jsonMode disables every other interactive
		// affordance.
		if !jsonMode {
			if interval, _ := cmd.Flags().GetDuration("progress-interval"); interval > 0 {
				opts.ProgressInterval = interval
				opts.Progress = func(p nip13.Progress) {
					rate := float64(p.HashesTried) / p.Elapsed.Seconds()
					log.Info().Msgf("mining... %s hashes tried, %s elapsed, %s across %d worker(s)",
						withThousands(strconv.FormatUint(p.HashesTried, 10)),
						p.Elapsed.Round(100*time.Millisecond),
						humanRate(rate),
						p.Workers)
				}
			}
		}

		event, err := client.Mine(ctx, draft, outPath, difficulty, opts)
		if err != nil {
			return common.RuntimeError(cmd, err)
		}

		signed := event.Sig != ""
		if !signed && opts.IdentityPubKeyHex != "" {
			log.Warn().Msg("mined but not signed: no private key available for this identity")
		}

		var nonce string
		if tag := event.GetTag(nip13.POWTagName); len(tag) > 0 {
			nonce = tag[0]
		}

		if jsonMode {
			common.PrintJSON(map[string]any{
				"id":         event.ID,
				"nonce":      nonce,
				"difficulty": difficulty,
				"signed":     signed,
				"out":        outPath,
			})
			return nil
		}

		// The mined event itself is this command's result, not narration --
		// same stdout-only-holds-the-result convention as id/version/relay
		// stats's text mode, unlike client.Mine's own log.Info calls (mining
		// progress, duration), which stay on stderr.
		fmt.Println("id:        ", event.ID)
		fmt.Println("nonce:     ", nonce)
		fmt.Println("difficulty:", difficulty)
		fmt.Println("signed:    ", signed)
		fmt.Println("out:       ", outPath)
		return nil
	},
}

// resolveMineContent returns --content's literal value, or --content-file's
// file contents with exactly one trailing newline stripped (the file's own
// EOF convention, not part of the note) -- mine's --content/--content-file
// authoring path, as an alternative to a hand-written --event file.
func resolveMineContent(cmd *cobra.Command) (string, error) {
	if cmd.Flags().Changed("content-file") {
		path, err := validateArgFile(cmd, "content-file", true, ".txt")
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		content := strings.TrimSuffix(string(data), "\n")
		content = strings.TrimSuffix(content, "\r")
		return content, nil
	}
	content, _ := cmd.Flags().GetString("content")
	return content, nil
}

// parseTagFlags turns repeated --tag key=value flags into NIP-01's
// [][]string tag shape -- covers the common 2-element case (e.g. `--tag
// t=nostr`); anything needing more elements (relay hints, markers, multi-
// value e/p tags) stays on the --event file path.
func parseTagFlags(pairs []string) ([][]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	tags := make([][]string, 0, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("--tag %q must be in key=value form", p)
		}
		tags = append(tags, []string{k, v})
	}
	return tags, nil
}

var minerCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Verify proof-of-work",
	Long: `Verify proof-of-work (NIP-13) on already-mined events, sourced either from
a JSON file (--events, e.g. one produced by "ncli dump") or fetched live,
merged across every target. Live mode's targets and filters come from
--targets (a YAML file that may declare both), or from --relays plus
inline filter flags -- pick one, not a mix. Omitting both falls back to
the relays configured via "ncli prefs relays add". --identity further
narrows live mode to one identity's own events. Exits non-zero if any
checked event fails.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cmd.ValidateRequiredFlags(); err != nil {
			return common.UsageError(cmd, err)
		}

		fileMode := cmd.Flags().Changed("events")
		liveMode := cmd.Flags().Changed("targets") || cmd.Flags().Changed("relays") || cmd.Flags().Changed("identity") || inlineFilterFlagsChanged(cmd)

		switch {
		case fileMode && liveMode:
			return common.UsageError(cmd, fmt.Errorf("--events (file mode) cannot be combined with --targets/--relays/--identity/inline filter flags (live mode); use one or the other"))
		case !fileMode && !liveMode:
			return common.UsageError(cmd, fmt.Errorf("specify --events for file mode, or --targets/--relays/--identity/an inline filter flag for live mode"))
		}

		if err := queryMutualExclusionCheck(cmd); err != nil {
			return common.UsageError(cmd, err)
		}

		if fileMode {
			if _, err := validateArgFile(cmd, "events", true, ".json", ".jsonp"); err != nil {
				return common.UsageError(cmd, err)
			}
		}
		if cmd.Flags().Changed("targets") {
			if _, err := validateArgFile(cmd, "targets", true, ".yaml", ".yml"); err != nil {
				return common.UsageError(cmd, err)
			}
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		jsonMode, _ := cmd.Flags().GetBool("json")

		var report *client.POWCheckReport
		var err error

		if cmd.Flags().Changed("events") {
			eventsPath, ferr := validateArgFile(cmd, "events", true, ".json", ".jsonp")
			if ferr != nil {
				return common.RuntimeError(cmd, ferr)
			}
			report, err = client.CheckPOWFromFile(eventsPath)
		} else {
			targetsSpec, filtersSpec, qerr := resolveQuery(cmd)
			if qerr != nil {
				return common.RuntimeError(cmd, qerr)
			}

			if err := client.ResolveFilterAuthors(filtersSpec); err != nil {
				return common.NetworkError(cmd, "", err)
			}

			if identity, _ := cmd.Flags().GetString("identity"); identity != "" {
				resolved, ferr := client.ResolveIdentifier(identity)
				if ferr != nil {
					return keyresolve.ClassifyIdentifierError(cmd, identity, ferr)
				}
				filtersSpec, ferr = client.ApplyIdentityFilter(filtersSpec, resolved.PubKeyHex)
				if ferr != nil {
					return common.InvalidInputError(cmd, common.RedactSecretInput(identity), ferr)
				}
			}

			report, err = client.CheckPOWLive(ctx, targetsSpec, filtersSpec)
		}

		if err != nil {
			if errors.Is(err, client.ErrNoReachableTargets) {
				return common.NetworkError(cmd, "", err)
			}
			return common.RuntimeError(cmd, err)
		}

		if jsonMode {
			common.PrintJSON(report)
		} else {
			// The per-event verdicts and summary are this command's result,
			// not narration -- same stdout-only-holds-the-result convention
			// as jsonMode's report above, just human-readable instead of
			// JSON (see the equivalent fix in "miner mine" above).
			for _, r := range report.Results {
				if r.Valid {
					fmt.Println("id:        ", r.ID)
					fmt.Println("  nonce:     ", r.Nonce)
					fmt.Println("  difficulty:", r.Difficulty)
				} else {
					fmt.Println("id:   ", r.ID)
					fmt.Println("  error:", r.Error)
				}
			}
			fmt.Printf("checked %d, valid %d, invalid %d\n", report.Checked, report.Valid, report.Invalid)
		}

		if !report.AllValid() {
			return common.RuntimeError(cmd, fmt.Errorf("%d of %d events failed PoW verification", report.Invalid, report.Checked))
		}
		return nil
	},
}

func init() {
	RootCmd.AddCommand(minerCmd)
	minerCmd.AddCommand(minerMineCmd)
	minerCmd.AddCommand(minerCheckCmd)

	minerMineCmd.Flags().StringP("event", "e", "", "Path to a structured, unsigned event file")
	minerMineCmd.MarkFlagFilename("event", "yaml", "yml", "json")

	minerMineCmd.Flags().String("content", "", "Note content to mine, authored inline (alternative to --event)")
	minerMineCmd.Flags().String("content-file", "", "Path to a plain .txt file with the note content to mine (alternative to --event/--content)")
	minerMineCmd.MarkFlagFilename("content-file", "txt")
	minerMineCmd.Flags().Int("kind", 1, "Event kind (--content/--content-file mode only)")
	minerMineCmd.Flags().StringArray("tag", nil, "Tag as key=value, repeatable (--content/--content-file mode only, e.g. --tag t=nostr)")

	minerMineCmd.Flags().StringP("out", "o", "", "Output path for the mined event (.json/.jsonp/.yaml/.yml)")
	minerMineCmd.MarkFlagFilename("out", "yaml", "yml", "json")
	minerMineCmd.Flags().Bool("in-place", false, "Write the mined result back to --event")

	minerMineCmd.Flags().IntP("difficulty", "d", 2, "Proof-of-work difficulty, in leading zero bits")
	minerMineCmd.Flags().IntP("workers", "w", 0, "Parallel mining workers (0 = every available CPU core)")
	minerMineCmd.Flags().Duration("progress-interval", 5*time.Second, "How often to log mining progress (0 disables it)")
	minerMineCmd.Flags().String("identity", "", "Vault label/npub/hex/nsec/nprofile/nip-05 identity to fill the event's pubkey from -- also signs automatically if a private key is available (nsec, or a vault label)")

	minerCheckCmd.Flags().StringP("events", "e", "", "File mode: path to a JSON array of already-mined events")
	minerCheckCmd.MarkFlagFilename("events", "json", "jsonp")

	registerQueryFlags(minerCheckCmd, "Live mode: ")

	minerCheckCmd.Flags().String("identity", "", "Live mode: vault label/npub/hex/nsec/nprofile/nip-05 identity to restrict to")
}

// withThousands inserts thousands separators into a decimal digit string
// (as produced by strconv.Format*/fmt's %d/%.0f, an optional leading "-" and
// nothing after the digits), e.g. "1804600" -> "1,804,600" -- used to keep
// mining's progress narration (hashes tried, h/s) readable at the millions
// scale a modern multi-core mine reaches within a second.
func withThousands(s string) string {
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}

	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}

	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// humanRate scales a hashes/second figure to the largest unit (H/s, KH/s,
// MH/s, GH/s) that keeps the mantissa readable -- a modern multi-core
// miner's raw h/s reaches the millions within the first progress tick, and
// "6,547,305 h/s" is slower to read at a glance than "6.55 MH/s".
func humanRate(hz float64) string {
	switch {
	case hz >= 1e9:
		return fmt.Sprintf("%.2f GH/s", hz/1e9)
	case hz >= 1e6:
		return fmt.Sprintf("%.2f MH/s", hz/1e6)
	case hz >= 1e3:
		return fmt.Sprintf("%.2f KH/s", hz/1e3)
	default:
		return fmt.Sprintf("%.0f H/s", hz)
	}
}
