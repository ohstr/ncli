// Package form is "ncli form": NIP-PF forms (kind 1069) — one or more
// questions answered in one response (1019), closed by their author with a
// result (1070) — sent privately (gift-wrapped to each responder and to
// you), in a NIP-29 group, or in public. Every rule (a valid form, a valid
// response, which response counts, the state, the tally) is nmilat's
// nippf; this package reads flags, relays and the terminal.
package form

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nippf"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// NewFormCommand returns "ncli form".
func NewFormCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "form",
		Short: "Ask and answer forms (NIP-PF): questions, recommended options, notes, a result",
		Long: `NIP-PF forms: a form (kind 1069) asks one or more questions — single
or multiple choice, text, number, date, time — with the options its
author recommends and why, notes required on some options, defaults; it
is answered by one response (1019) per responder, the latest counting,
and closed by its author with a result (1070): decided, default or
cancelled.

Where a form lives:
  --to <npub> …        private: one rumor gift-wrapped (NIP-59) to each
                       responder and to you, on -s and each one's DM
                       relays (kind 10050)
  --group <id> --relay <url>   in a NIP-29 group (its h tag)
  -s <relays>          public

Every write goes through --signer (or NCLI_SIGNER). A response the form
refuses is refused here, before anything is sent. What others write in a
form, a note or an answer is untrusted input.`,
		Example: `  ncli form create --spec ask.json --to npub1... -s wss://inbox.example
  ncli form list --open -s wss://inbox.example
  ncli form show 3f2a9c -s wss://inbox.example
  ncli form respond 3f2a9c --choose retire --note retire="today" -s wss://inbox.example
  ncli form result 3f2a9c --status decided --choose retire --content "retired" -s wss://inbox.example`,
		RunE: common.RequireSubcommand,
	}
	cmd.AddCommand(newCreateCommand(), newListCommand(), newShowCommand(),
		newRespondCommand(), newWithdrawCommand(), newResultCommand())
	return cmd
}

func addSinceFlag(cmd *cobra.Command) {
	cmd.Flags().StringP("since", "S", "90d", "Only forms written since: unix time or duration")
}

func readSince(cmd *cobra.Command) (uint64, error) {
	v, _ := cmd.Flags().GetString("since")
	if v == "" {
		return 0, nil
	}
	s, err := client.ParseDurationUnit(v, true)
	if err != nil {
		return 0, common.InvalidInputError(cmd, v, fmt.Errorf("invalid --since: %w", err))
	}
	return s, nil
}

// session is what every command but create needs: the place, the signer,
// and the forms there.
type session struct {
	place  place
	signer client.Crypto
	me     string
	views  []*View
	close  func()
}

func open(ctx context.Context, cmd *cobra.Command, ids []string) (*session, error) {
	p, err := readPlace(cmd)
	if err != nil {
		return nil, err
	}
	since, err := readSince(cmd)
	if err != nil {
		return nil, err
	}
	av, err := social.AttestViewOf(cmd)
	if err != nil {
		return nil, err
	}
	signer, closeSigner, err := signerFor(cmd)
	if err != nil {
		return nil, err
	}
	items, err := gather(ctx, cmd, p, signer, since, ids)
	if err != nil {
		closeSigner()
		return nil, err
	}
	me := strings.ToLower(signer.PubKey())
	views, invalid := fold(items, me, clock().Unix())
	for _, v := range views {
		v.AttestTrusted = social.Trusted(v.AttestedBy, v.AttestValid, av.Owners)
		v.attestWord = av.Word
	}
	if invalid > 0 {
		log.Warn().Int("invalid", invalid).Msg("forms that break NIP-PF's rules skipped")
	}
	return &session{place: p, signer: signer, me: me, views: views, close: closeSigner}, nil
}

// fullID: the id arg names in full (for reading a public form not naming
// me), or nothing for a prefix.
func fullID(arg string) []string {
	a := strings.ToLower(strings.TrimSpace(arg))
	if len(a) == 64 && isHex(a) {
		return []string{a}
	}
	return nil
}

func signalContext(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
}

// ---- create

func newCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create [--spec <file|->] (--to <who>… | --group <id> --relay <url> | -s <relays>)",
		Short: "Create a form from a JSON spec, or by answering prompts",
		Long: `Builds a form, checks it against NIP-PF's rules, and sends it.

--spec reads it as JSON (a file, or - for stdin):
  {"title": "…", "context": "…", "e": ["<event id>"], "a": ["<kind:pubkey:d>"],
   "responders": ["npub…"], "observers": ["npub…"], "quorum": 1,
   "priority": "high", "endsAt": <unix>, "expiration": <unix>, "results": "all",
   "questions": [{"text": "…", "type": "singlechoice", "why": "…",
     "options": [{"id": "a", "label": "…", "rec": true},
                 {"id": "other", "label": "Other (say what)", "noteRequired": true}],
     "optional": false, "default": "a", "min": "…", "max": "…"}]}
Types: singlechoice, multiplechoice, text, number, date (YYYY-MM-DD),
time (unix seconds). Without --spec it asks for each part on the
terminal.

--to sends it privately: each --to key not already a responder or
observer becomes a responder, and the form is gift-wrapped to every one
of them and to you. --group/--relay posts it in a group; else it is
public on -s (its alt tag filled). --print shows the form and the event,
and sends nothing.`,
		Example: `  ncli form create --spec ask.json --to npub1... -s wss://inbox.example
  ncli form create --spec ask.json --group ops --relay wss://groups.example
  ncli form create --spec ask.json -s wss://relay.example --print`,
		Args: common.NoArgs,
		RunE: runCreate,
	}
	cmd.Flags().String("spec", "", "The form as JSON (a file, or - for stdin); without it, prompts")
	cmd.Flags().StringArray("to", nil, "Send privately to this key (repeatable): npub, hex, nprofile, nip-05 or vault label")
	cmd.Flags().Bool("print", false, "Show the form and its event; send nothing")
	addPlaceFlags(cmd)
	return cmd
}

func runCreate(cmd *cobra.Command, _ []string) error {
	ctx, cancel := signalContext(cmd)
	defer cancel()
	to, _ := cmd.Flags().GetStringArray("to")
	p, err := readPlace(cmd)
	if err != nil {
		return err
	}
	if len(to) > 0 && p.group != "" {
		return common.InvocationError(cmd, errors.New("--to (private) or --group, not both"))
	}
	var spec *Spec
	if path, _ := cmd.Flags().GetString("spec"); path != "" {
		r := cmd.InOrStdin()
		if path != "-" {
			fh, err := os.Open(path)
			if err != nil {
				return common.InvalidInputError(cmd, path, err)
			}
			defer func() { _ = fh.Close() }()
			r = fh
		}
		if spec, err = ReadSpec(r); err != nil {
			return common.InvalidInputError(cmd, path, err)
		}
	} else {
		if spec, err = promptSpec(cmd.ErrOrStderr(), bufio.NewReader(cmd.InOrStdin())); err != nil {
			return common.InvalidInputError(cmd, "", err)
		}
	}
	resolve := func(v string) (string, error) {
		ks, err := social.People(cmd, []string{v})
		if err != nil {
			return "", err
		}
		return ks[0], nil
	}
	f, err := spec.Form(resolve)
	if err != nil {
		return common.InvalidInputError(cmd, "", err)
	}
	toKeys, err := social.People(cmd, to)
	if err != nil {
		return err
	}
	f.Private = len(toKeys) > 0
	f.Group = p.group
	AddResponders(f, toKeys)

	signer, closeSigner, err := signerFor(cmd)
	if err != nil {
		return err
	}
	defer closeSigner()
	me := strings.ToLower(signer.PubKey())
	ev, err := f.Event(me, clock().Unix())
	if err != nil {
		return common.InvalidInputError(cmd, "", err)
	}
	if pr, _ := cmd.Flags().GetBool("print"); pr {
		back, err := nippf.ParseForm(ev, f.Private)
		if err != nil {
			return common.InvalidInputError(cmd, "", err)
		}
		if jsonMode(cmd) {
			printJSON(cmd, map[string]any{"form": back, "event": ev})
			return nil
		}
		writeView(cmd.OutOrStdout(), &View{Form: back, State: nippf.StateOpen, Where: whereOf(back), Tally: back.Tally(nil)})
		printJSON(cmd, ev)
		return nil
	}
	d, err := deliver(ctx, cmd, p, signer, ev, f.Private, f.Group, f.FormRecipients(), nil)
	if err != nil {
		return err
	}
	return printDelivery(cmd, d, "form")
}

func whereOf(f *nippf.Form) string {
	switch {
	case f.Private:
		return "private"
	case f.Group != "":
		return "group"
	}
	return "public"
}

// ---- list, show

func newListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list (--group <id> --relay <url> | -s <relays>)",
		Short: "List the forms that name you, yours, or a group's",
		Long: `Lists forms with their state (open, ended, decided, default, cancelled,
expired), how many responders answered, and whether you still have to.

On -s: the private forms sent to you or by you, and the public forms
that name you or are yours. In a group: every form there.
--mine: yours only; --asked: only those you may answer; --open: open
ones only; --state picks one state.`,
		Example: `  ncli form list --open -s wss://inbox.example
  ncli form list --group ops --relay wss://groups.example --asked --json`,
		Args: common.NoArgs,
		RunE: runList,
	}
	cmd.Flags().Bool("mine", false, "Only forms you created")
	cmd.Flags().Bool("asked", false, "Only forms you may answer")
	cmd.Flags().Bool("open", false, "Only open forms")
	cmd.Flags().String("state", "", "Only forms in this state: open, ended, decided, default, cancelled, expired")
	addSinceFlag(cmd)
	addPlaceFlags(cmd)
	return cmd
}

func runList(cmd *cobra.Command, _ []string) error {
	ctx, cancel := signalContext(cmd)
	defer cancel()
	f := cmd.Flags()
	state, _ := f.GetString("state")
	if o, _ := f.GetBool("open"); o {
		if state != "" && state != string(nippf.StateOpen) {
			return common.InvocationError(cmd, errors.New("--open or --state, not both"))
		}
		state = string(nippf.StateOpen)
	}
	switch nippf.State(state) {
	case "", nippf.StateOpen, nippf.StateEnded, nippf.StateDecided, nippf.StateDefault, nippf.StateCancelled, nippf.StateExpired:
	default:
		return common.InvalidInputError(cmd, state, errors.New("--state: open, ended, decided, default, cancelled or expired"))
	}
	mine, _ := f.GetBool("mine")
	asked, _ := f.GetBool("asked")
	s, err := open(ctx, cmd, nil)
	if err != nil {
		return err
	}
	defer s.close()
	out := []*View{}
	for _, v := range s.views {
		switch {
		case s.place.group == "" && v.Role == "":
		case mine && v.Role != "author":
		case asked && (v.Role == "author" || !v.Form.Allowed(s.me) || v.Form.ReadOnly):
		case state != "" && string(v.State) != state:
		default:
			out = append(out, v)
		}
	}
	if jsonMode(cmd) {
		printJSON(cmd, out)
		return nil
	}
	w := cmd.OutOrStdout()
	if len(out) == 0 {
		_, _ = fmt.Fprintln(w, "no form")
		return nil
	}
	for _, v := range out {
		_, _ = fmt.Fprintln(w, listLine(v))
	}
	return nil
}

func newShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <form id> (--group <id> --relay <url> | -s <relays>)",
		Short: "Show a form: its questions, the recommended options and why, the tally, notes, who hasn't answered",
		Long: `Shows one form: its context, each question with its index (what
--answer names), its options (recommended ones marked, with the
author's why; notes required), defaults and bounds; then the tally per
option with the notes, the answers, who hasn't answered, your response
and the result. The id may be its first 6 characters or more.`,
		Example: `  ncli form show 3f2a9c -s wss://inbox.example
  ncli form show 3f2a9c --group ops --relay wss://groups.example --json`,
		Args: common.ExactArgs(1),
		RunE: runShow,
	}
	addSinceFlag(cmd)
	addPlaceFlags(cmd)
	return cmd
}

func runShow(cmd *cobra.Command, args []string) error {
	ctx, cancel := signalContext(cmd)
	defer cancel()
	s, err := open(ctx, cmd, fullID(args[0]))
	if err != nil {
		return err
	}
	defer s.close()
	v, err := pick(cmd, s.views, args[0])
	if err != nil {
		return err
	}
	if jsonMode(cmd) {
		printJSON(cmd, v)
		return nil
	}
	writeView(cmd.OutOrStdout(), v)
	return nil
}

// ---- respond, withdraw, result

func addOutcomeFlags(cmd *cobra.Command) {
	cmd.Flags().StringArray("choose", nil, "Choose this option (by id; repeatable, or comma-separated)")
	cmd.Flags().StringArray("note", nil, "<option>=<text>: a note on a chosen option (repeatable)")
	cmd.Flags().StringArray("answer", nil, "<index>=<value>: the answer to a text, number, date or time question (repeatable)")
	cmd.Flags().StringArray("answer-note", nil, "<index>=<text>: a note on an answer (repeatable)")
	cmd.Flags().Bool("print", false, "Show the event; send nothing")
}

// outcomeFlags reads --choose/--note/--answer/--answer-note; given: any was.
func outcomeFlags(cmd *cobra.Command) (cs []nippf.Choice, as []nippf.Answer, given bool, err error) {
	f := cmd.Flags()
	choose, _ := f.GetStringArray("choose")
	notes, _ := f.GetStringArray("note")
	answers, _ := f.GetStringArray("answer")
	answerNotes, _ := f.GetStringArray("answer-note")
	given = len(choose)+len(notes)+len(answers)+len(answerNotes) > 0
	for _, c := range choose {
		for _, id := range strings.Split(c, ",") {
			if id = strings.TrimSpace(id); id != "" {
				cs = append(cs, nippf.Choice{Option: id})
			}
		}
	}
	for _, n := range notes {
		id, text, ok := strings.Cut(n, "=")
		id = strings.TrimSpace(id)
		if !ok || id == "" {
			return nil, nil, given, common.InvalidInputError(cmd, n, errors.New("--note <option>=<text>"))
		}
		i := slices.IndexFunc(cs, func(c nippf.Choice) bool { return c.Option == id })
		if i < 0 {
			return nil, nil, given, common.InvalidInputError(cmd, n, fmt.Errorf("--note names %q, which --choose doesn't", id))
		}
		cs[i].Note = text
	}
	for _, a := range answers {
		idx, value, ok := strings.Cut(a, "=")
		i, err := strconv.Atoi(strings.TrimSpace(idx))
		if !ok || err != nil || i < 0 {
			return nil, nil, given, common.InvalidInputError(cmd, a, errors.New("--answer <question index>=<value>"))
		}
		as = append(as, nippf.Answer{Question: i, Value: value})
	}
	for _, n := range answerNotes {
		idx, text, ok := strings.Cut(n, "=")
		i, err := strconv.Atoi(strings.TrimSpace(idx))
		if !ok || err != nil {
			return nil, nil, given, common.InvalidInputError(cmd, n, errors.New("--answer-note <question index>=<text>"))
		}
		j := slices.IndexFunc(as, func(a nippf.Answer) bool { return a.Question == i })
		if j < 0 {
			return nil, nil, given, common.InvalidInputError(cmd, n, fmt.Errorf("--answer-note names question %d, which --answer doesn't", i))
		}
		as[j].Note = text
	}
	return cs, as, given, nil
}

func newRespondCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "respond <form id> [--choose <option>…] [--answer <index>=<value>…] (--group <id> --relay <url> | -s <relays>)",
		Short: "Answer a form: every question in one response",
		Long: `Sends your response to a form: one event answering every question you
answer, checked against the form before anything is sent — each
single-choice question one option, a multiple-choice question its
options, a note on every option that requires one, each value valid
and within its bounds, every question that isn't optional answered.
Your latest response counts; withdraw takes it back.

With no --choose or --answer it asks on the terminal, question by
question: the options with the recommended one marked and the author's
why, a note where one is required (and offered elsewhere), optional
questions skippable; then it shows the response and asks to send it.

A private form's response is gift-wrapped to its author (and to every
recipient when the form says results: all) and to you; a group form's
goes to the group; a public form's to -s and the form's relay hints.`,
		Example: `  ncli form respond 3f2a9c --choose retire --note retire="signing today" -s wss://inbox.example
  ncli form respond 3f2a9c --choose revert --answer 1=512 --answer-note 1="the cache needs it" --group ops --relay wss://groups.example
  ncli form respond 3f2a9c -s wss://inbox.example   # asks on the terminal`,
		Args: common.ExactArgs(1),
		RunE: runRespond,
	}
	addOutcomeFlags(cmd)
	addSinceFlag(cmd)
	addPlaceFlags(cmd)
	return cmd
}

// answerable: v may be answered by me now, or why not.
func answerable(cmd *cobra.Command, v *View, me string) error {
	switch {
	case v.Form.ReadOnly:
		return common.UnsupportedError(cmd, v.Form.ID, nippf.ErrReadOnly)
	case !v.Form.Allowed(me):
		return common.AuthError(cmd, nippf.ErrNotResponder)
	case v.State != nippf.StateOpen:
		return common.ConflictError(cmd, v.Form.ID, fmt.Errorf("the form is %s: a response now counts for nothing", v.State))
	}
	return nil
}

func runRespond(cmd *cobra.Command, args []string) error {
	ctx, cancel := signalContext(cmd)
	defer cancel()
	cs, as, given, err := outcomeFlags(cmd)
	if err != nil {
		return err
	}
	s, err := open(ctx, cmd, fullID(args[0]))
	if err != nil {
		return err
	}
	defer s.close()
	v, err := pick(cmd, s.views, args[0])
	if err != nil {
		return err
	}
	if err := answerable(cmd, v, s.me); err != nil {
		return err
	}
	f := v.Form
	interactive := !given
	if interactive {
		in := bufio.NewReader(cmd.InOrStdin())
		if cs, as, err = promptResponse(cmd.ErrOrStderr(), in, f, v.Labels); err != nil {
			return common.InvalidInputError(cmd, "", err)
		}
		ev, err := f.NewResponse(s.me, replyTime(v), cs, as)
		if err != nil {
			return common.InvalidInputError(cmd, f.ID, err)
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "\nyour response: %s\n", describe(cs, as))
		if !confirm(cmd.ErrOrStderr(), in, "send it? [Y/n] ") {
			return common.RuntimeError(cmd, errors.New("not sent"))
		}
		return send(ctx, cmd, s, v, ev, f.ResponseRecipients(), "response")
	}
	ev, err := f.NewResponse(s.me, replyTime(v), cs, as)
	if err != nil {
		return common.InvalidInputError(cmd, f.ID, err)
	}
	return send(ctx, cmd, s, v, ev, f.ResponseRecipients(), "response")
}

// replyTime: now, or just after my latest response to v's form when that
// is as late (a response in the same second could lose the tie).
func replyTime(v *View) int64 {
	t := clock().Unix()
	if t <= v.last {
		t = v.last + 1
	}
	return t
}

// send delivers ev the way v's form lives, or prints it with --print.
func send(ctx context.Context, cmd *cobra.Command, s *session, v *View, ev *nip01.Event, to []string, what string) error {
	if pr, _ := cmd.Flags().GetBool("print"); pr {
		printJSON(cmd, ev)
		return nil
	}
	p := s.place
	if v.Form.Group != "" && p.group == "" {
		return common.InvocationError(cmd, fmt.Errorf("the form is in group %s: answer with --group %s --relay <its relay>", v.Form.Group, v.Form.Group))
	}
	d, err := deliver(ctx, cmd, p, s.signer, ev, v.Form.Private, v.Form.Group, to, v.Form.Relays)
	if err != nil {
		return err
	}
	d.Form = v.Form.ID
	return printDelivery(cmd, d, what)
}

func newWithdrawCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "withdraw <form id> (--group <id> --relay <url> | -s <relays>)",
		Short: "Take back your response to a form",
		Long: `Sends a response with no answer: your earlier response no longer
counts, and nothing takes its place (respond again to answer anew).`,
		Example: `  ncli form withdraw 3f2a9c -s wss://inbox.example`,
		Args:    common.ExactArgs(1),
		RunE:    runWithdraw,
	}
	cmd.Flags().Bool("print", false, "Show the event; send nothing")
	addSinceFlag(cmd)
	addPlaceFlags(cmd)
	return cmd
}

func runWithdraw(cmd *cobra.Command, args []string) error {
	ctx, cancel := signalContext(cmd)
	defer cancel()
	s, err := open(ctx, cmd, fullID(args[0]))
	if err != nil {
		return err
	}
	defer s.close()
	v, err := pick(cmd, s.views, args[0])
	if err != nil {
		return err
	}
	if err := answerable(cmd, v, s.me); err != nil {
		return err
	}
	ev, err := v.Form.NewWithdrawal(s.me, replyTime(v))
	if err != nil {
		return common.InvalidInputError(cmd, v.Form.ID, err)
	}
	return send(ctx, cmd, s, v, ev, v.Form.ResponseRecipients(), "withdrawal")
}

func newResultCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "result <form id> --status decided|default|cancelled [--choose …] [--answer …] [--content <text>]",
		Short: "Close your form with its result",
		Long: `Closes a form you created; the earliest result counts and ends the
form for everyone.

  decided    the outcome you took (--choose, --answer, as a response
             would; notes optional)
  default    the form's defaults apply (they are filled in for you)
  cancelled  no outcome

--content says what you did. Checked against the form before anything
is sent. A private form's result is gift-wrapped to every recipient.`,
		Example: `  ncli form result 3f2a9c --status decided --choose retire --content "retired; record signed" -s wss://inbox.example
  ncli form result 3f2a9c --status default -s wss://inbox.example
  ncli form result 3f2a9c --status cancelled --content "asked wrong; see the new form" -s wss://inbox.example`,
		Args: common.ExactArgs(1),
		RunE: runResult,
	}
	cmd.Flags().String("status", "", "decided, default or cancelled (required)")
	cmd.Flags().String("content", "", "What you did")
	addOutcomeFlags(cmd)
	addSinceFlag(cmd)
	addPlaceFlags(cmd)
	return cmd
}

func runResult(cmd *cobra.Command, args []string) error {
	ctx, cancel := signalContext(cmd)
	defer cancel()
	status, _ := cmd.Flags().GetString("status")
	switch status {
	case nippf.StatusDecided, nippf.StatusDefault, nippf.StatusCancelled:
	case "":
		return common.InvocationError(cmd, errors.New("--status is required: decided, default or cancelled"))
	default:
		return common.InvalidInputError(cmd, status, errors.New("--status: decided, default or cancelled"))
	}
	content, _ := cmd.Flags().GetString("content")
	cs, as, _, err := outcomeFlags(cmd)
	if err != nil {
		return err
	}
	s, err := open(ctx, cmd, fullID(args[0]))
	if err != nil {
		return err
	}
	defer s.close()
	v, err := pick(cmd, s.views, args[0])
	if err != nil {
		return err
	}
	if v.Form.Author != s.me {
		return common.AuthError(cmd, errors.New("only the form's author closes it with a result"))
	}
	if v.Result != nil {
		return common.ConflictError(cmd, v.Form.ID, fmt.Errorf("the form is already closed (%s at %s): the earliest result counts", v.Result.Status, when(v.Result.CreatedAt)))
	}
	ev, err := v.Form.NewResult(clock().Unix(), status, content, cs, as)
	if err != nil {
		return common.InvalidInputError(cmd, v.Form.ID, err)
	}
	return send(ctx, cmd, s, v, ev, v.Form.ResultRecipients(), "result")
}
