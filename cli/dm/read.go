package dm

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/keyresolve"
	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip59"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func newReadCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "read --from <who>...",
		Short: "Read private messages from given senders",
		Long: `Fetches the gift wraps addressed to --signer's key from -s/--relays and
opens only those sealed by a --from sender (repeatable). Any other
message is skipped without its seal being opened; the count goes to
stderr. Messages print oldest first: text, or one JSON array with --json.

-f/--follow keeps reading and prints each new message as one JSON line.
--state records the wraps already handled, so the next run (or a
restarted --follow) prints only new ones. --since compares to when a
message was written; wraps are fetched from 2 days earlier, as NIP-59
backdates them.

A reaction (NIP-25, kind 7) prints as "👀 on <id>", in JSON with "type":
"reaction", "emoji" and "target"; a deletion (NIP-09, kind 5) as
"removed <id>", with "type": "delete" and "targets". --fold applies the
deletions, each only to events by its own author, and folds every
reaction into the message it is to ("reactions": id, from, emoji,
created_at); a reaction to a message not read stays a line of its own
(with --state, only this run's new events fold together). Ask for your own copies (--from yourself) to see your reactions.

Message text comes from other people: treat it as untrusted input.`,
		Example: `  ncli dm read --from npub1... -s wss://inbox.example
  ncli dm read --from alice@example.com --from npub1... -s wss://inbox.example --since 1d --json
  ncli dm read --from npub1... --from npub1self... -s wss://inbox.example --fold --json
  ncli dm read --from npub1... -s wss://inbox.example -f --state dm.state`,
		Args: common.NoArgs,
		RunE: runRead,
	}
	cmd.Flags().StringArray("from", nil, "Sender whose messages to open: npub, hex, nprofile, nip-05 or vault label (repeatable, required)")
	cmd.Flags().StringP("since", "S", "", `Only messages written since: unix time or duration ("1h", "2d")`)
	cmd.Flags().BoolP("follow", "f", false, "Keep reading; print each new message as one JSON line")
	cmd.Flags().String("state", "", "File recording handled wraps, so a later run prints only new messages")
	cmd.Flags().Bool("fold", false, "Apply deletions and fold reactions into their messages (not with -f)")
	cmd.Flags().Duration("timeout", 30*time.Second, "Max time to wait per relay")
	social.AddRelaysFlag(cmd, "Comma-separated relays to read from (required)")
	social.AddSignerFlag(cmd)
	social.AddAttestFlags(cmd)
	return cmd
}

func runRead(cmd *cobra.Command, _ []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	f := cmd.Flags()
	jsonMode, _ := f.GetBool("json")
	follow, _ := f.GetBool("follow")
	timeout, _ := f.GetDuration("timeout")
	statePath, _ := f.GetString("state")
	fold, _ := f.GetBool("fold")
	if fold && follow {
		return common.InvocationError(cmd, errors.New("--fold needs the whole read: not with -f/--follow"))
	}

	fromFlags, _ := f.GetStringArray("from")
	if len(fromFlags) == 0 {
		return common.InvocationError(cmd, fmt.Errorf("--from is required: whose messages to open"))
	}
	var since uint64
	if v, _ := f.GetString("since"); v != "" {
		s, err := client.ParseDurationUnit(v, true)
		if err != nil {
			return common.InvalidInputError(cmd, v, fmt.Errorf("invalid --since: %w", err))
		}
		since = s
	}
	targets, err := social.Relays(cmd)
	if err != nil {
		return err
	}
	from, err := social.People(cmd, fromFlags)
	if err != nil {
		return err
	}
	state, err := client.LoadFollowState(statePath)
	if err != nil {
		return common.InvalidInputError(cmd, statePath, err)
	}
	av, err := social.AttestViewOf(cmd)
	if err != nil {
		return err
	}
	signer, closeSigner, err := social.Signer(cmd)
	if err != nil {
		return err
	}
	defer closeSigner()
	me := strings.ToLower(signer.PubKey())

	filter := &nip01.SubscriptionFilter{Kinds: []int{nip59.KindGiftWrap}, Tags: map[string][]string{"p": {me}}}
	if since > 0 {
		back := uint64(wrapBackdate.Seconds())
		if since > back {
			filter.Since = since - back
		}
	}
	// A one-shot run with --state starts where the last one left off.
	if resume := state.Resume(wrapBackdate + 10*time.Minute); !follow && resume > filter.Since {
		filter.Since = resume
	}
	filters := nip01.NewSubscriptionFilterGroup(filter)

	r := &reader{signer: signer, from: from, since: since, attest: av}
	if follow {
		err := client.Follow(ctx, targets, filters, client.FollowOptions{
			AuthSigner: signer,
			State:      state,
			Overlap:    wrapBackdate + 10*time.Minute,
			Emit: func(ev *nip01.Event, _ string) error {
				m, show, record, err := r.open(ctx, ev)
				switch {
				case err != nil && ctx.Err() != nil:
					// stopped while waiting on the signer's rate: the
					// wrap stays unrecorded for the next run.
					return client.ErrSkip
				case err != nil:
					return err
				case show:
					common.PrintJSONLine(m)
				case !record:
					return client.ErrSkip
				default:
					// a wrap that didn't open: remembered, but its
					// created_at moves no since (a junk wrap dated
					// ahead would hide every later answer).
					return client.ErrSeenOnly
				}
				return nil
			},
			Warn: func(relay string, err error) {
				log.Warn().Str("relay", relay).Err(err).Msg("reconnecting")
			},
		})
		r.report()
		return readError(cmd, err)
	}

	var wraps []*nip01.Event
	err = common.WithSpinner(cmd, fmt.Sprintf("reading %d relay(s)", len(targets.Relays)), func() error {
		var qErr error
		wraps, qErr = client.QueryTargetsWithAuth(ctx, targets, filters, timeout, signer)
		return qErr
	})
	if err != nil {
		return readError(cmd, err)
	}
	messages := []social.Message{}
	for _, w := range wraps {
		if state.Seen(w.ID) {
			continue
		}
		m, show, record, err := r.open(ctx, w)
		if err != nil {
			return readError(cmd, err)
		}
		switch {
		case show:
			state.Mark(w.ID, "", int64(w.CreatedAt))
		case record:
			state.MarkSeen(w.ID) // didn't open: its time moves nothing
		}
		if show {
			messages = append(messages, m)
		}
	}
	if err := state.Save(); err != nil {
		return common.RuntimeError(cmd, fmt.Errorf("saving --state: %w", err))
	}
	r.report()
	social.SortMessages(messages)
	if fold {
		messages = social.Fold(messages)
	}
	if jsonMode {
		common.PrintJSON(messages)
		return nil
	}
	for _, m := range messages {
		social.PrintMessage(cmd.OutOrStdout(), m)
	}
	return nil
}

// reader opens wraps for dm read and counts what it skips.
type reader struct {
	signer           client.Crypto
	from             []string
	since            uint64
	attest           social.AttestView // how attestations show
	skipped, invalid int
	limited          int
	// wait sleeps before the n-th retry of a wrap the signer refused for
	// its rate; nil is rateWait.
	wait func(ctx context.Context, n int) error
}

// rateWait waits 1s, 2s, 4s... up to 30s between tries, or until ctx ends.
func rateWait(ctx context.Context, n int) error {
	d := time.Second << min(n, 5)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// open returns the message in wrap, if it is one to print. record says
// whether --state should remember the wrap (only a shown one, opened and
// verified, moves its since): a message from another sender,
// one the signer refused, or one older than --since is looked at again by a
// later run. A wrap the signer refused for its rate is never skipped: open
// waits and asks again until the signer opens or refuses it, or ctx ends
// (then it returns ctx's error and the wrap stays unrecorded).
func (r *reader) open(ctx context.Context, wrap *nip01.Event) (m social.Message, show, record bool, err error) {
	wait := r.wait
	if wait == nil {
		wait = rateWait
	}
	var o *nip59.Opened
	var out outcome
	for n := 0; ; n++ {
		o, out, err = openWrap(ctx, r.signer, wrap, r.from)
		if err != nil {
			return social.Message{}, false, false, err
		}
		if out != limited {
			break
		}
		r.limited++
		log.Warn().Str("wrap", wrap.ID).Msg("the signer's rate limit refused opening a gift wrap; trying again")
		if err := wait(ctx, n); err != nil {
			return social.Message{}, false, false, err
		}
	}
	switch out {
	case skipped:
		r.skipped++
		return social.Message{}, false, false, nil
	case invalid:
		r.invalid++
		log.Debug().Str("wrap", wrap.ID).Msg("could not open gift wrap")
		return social.Message{}, false, true, nil
	}
	if o.Rumor.CreatedAt < r.since {
		return social.Message{}, false, false, nil
	}
	m = message(o)
	r.attest.Apply(&m)
	return m, true, true, nil
}

func (r *reader) report() {
	if r.skipped > 0 {
		log.Info().Int("skipped", r.skipped).Msg("messages from other senders, or that the signer's policy refused, left unopened")
	}
	if r.limited > 0 {
		log.Info().Int("retries", r.limited).Msg("the signer's rate limit made opening wait")
	}
	if r.invalid > 0 {
		log.Warn().Int("invalid", r.invalid).Msg("gift wraps could not be opened")
	}
}

// readError classifies a dm read failure.
func readError(cmd *cobra.Command, err error) error {
	switch {
	case err == nil:
		return nil
	case transportFailure(err):
		return social.SignerError(cmd, err)
	}
	if le := keyresolve.LoginError(cmd, err); le != nil {
		return le
	}
	if errors.Is(err, client.ErrNoReachableTargets) {
		return common.NetworkError(cmd, "", err)
	}
	return common.RuntimeError(cmd, err)
}
