package ncli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/cli/social"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip02"
	"github.com/ohstr/nmilat/nip17"
	"github.com/ohstr/nmilat/nip51"
	"github.com/ohstr/nmilat/nip65"
	"github.com/spf13/cobra"
)

// listChange is what a profile write prints with --json.
type listChange struct {
	ID      string                `json:"id"`
	Kind    int                   `json:"kind"`
	Added   []string              `json:"added"`
	Removed []string              `json:"removed"`
	Created bool                  `json:"created"`
	Event   *nip01.Event          `json:"event"`
	Report  *client.PublishReport `json:"report"`
}

// errNoChange is a write that would change nothing: nothing is published.
var errNoChange = errors.New("nothing to change")

func addProfileWriteFlags(cmd *cobra.Command) {
	cmd.Flags().StringSliceP("relays", "s", nil, "Comma-separated relays to read the current version from and publish to (defaults to `ncli prefs relays`)")
	cmd.Flags().Duration("timeout", 10*time.Second, "Per-relay query timeout")
	cmd.Flags().Bool("create", false, "Start a new one when no relay has a current version")
	social.AddSignerFlag(cmd)
}

func addProfileWriteCommands(profile *cobra.Command) {
	set := &cobra.Command{
		Use:   "set",
		Short: "Publish your profile, changing only the fields given",
		Long: `Reads your current profile (kind 0) from the relays, changes only the
fields given -- every other field is kept as it is -- and publishes it.
An empty value removes a field. Refuses if no relay answers, so a
profile is never rebuilt from nothing; --create starts a new one when no
relay has one.`,
		Example: `  ncli profile set --name agent --about "I answer DMs" --signer bunker+unix:///run/signer/agent.sock
  ncli profile set --field bot=true --picture "" --signer me`,
		Args: common.NoArgs,
		RunE: runProfileSet,
	}
	for _, f := range []struct{ name, key, help string }{
		{"name", "name", "Name"},
		{"display-name", "display_name", "Display name"},
		{"about", "about", "About text"},
		{"picture", "picture", "Picture URL"},
		{"banner", "banner", "Banner URL"},
		{"website", "website", "Website URL"},
		{"nip05", "nip05", "NIP-05 address"},
		{"lud16", "lud16", "Lightning address"},
	} {
		set.Flags().String(f.name, "", f.help)
	}
	set.Flags().StringArray("field", nil, `Any other field, as key=value (repeatable; a value that is JSON is kept as JSON)`)
	addProfileWriteFlags(set)

	relays := &cobra.Command{
		Use:   "relays",
		Short: "Publish your relay lists",
		RunE:  common.RequireSubcommand,
	}
	relaysSet := &cobra.Command{
		Use:   "set",
		Short: "Replace your outbox/inbox list (NIP-65) and DM relays (10050)",
		Long: `Publishes the relays given, replacing the list each flag names:
--read, --write and --both build your NIP-65 list (kind 10002); --dm
builds your DM relay list (kind 10050), where others send you private
messages. A list whose flags aren't given is left alone.`,
		Example: `  ncli profile relays set --both wss://relay.example --read wss://inbox.example --dm wss://inbox.example --signer me`,
		Args:    common.NoArgs,
		RunE:    runRelaysSet,
	}
	relaysSet.Flags().StringSlice("read", nil, "Relays you read from (inbox)")
	relaysSet.Flags().StringSlice("write", nil, "Relays you write to (outbox)")
	relaysSet.Flags().StringSlice("both", nil, "Relays you read from and write to")
	relaysSet.Flags().StringSlice("dm", nil, "Relays to receive private messages on")
	addProfileWriteFlags(relaysSet)
	relays.AddCommand(relaysSet)

	follow := listCommand("follow", "Follow people (adds to your kind 3 list)", nip02.KindContacts, true)
	unfollow := listCommand("unfollow", "Stop following people", nip02.KindContacts, false)
	mute := listCommand("mute", "Mute people (adds to your kind 10000 list)", nip51.KindMuteList, true)
	unmute := listCommand("unmute", "Unmute people", nip51.KindMuteList, false)

	profile.AddCommand(set, relays, follow, unfollow, mute, unmute)
}

func listCommand(name, short string, kind int, add bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   name + " <who...>",
		Short: short,
		Long: `Reads your current list from the relays, changes only the people given
-- every other entry, petname, relay hint and private (encrypted) item is
kept -- and publishes it. <who> is an npub, hex, nprofile, nip-05 or
vault label. Refuses if no relay answers, and never publishes an empty
list unless --allow-empty, so a list is never lost to an unreachable
relay.`,
		Example: fmt.Sprintf(`  ncli profile %s npub1... alice@example.com --signer me`, name),
		Args:    common.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			people, err := social.People(cmd, args)
			if err != nil {
				return err
			}
			allowEmpty, _ := cmd.Flags().GetBool("allow-empty")
			return writeReplaceable(cmd, kind, func(old *nip01.Event) (*nip01.Event, []string, []string, error) {
				var next *nip01.Event
				var changed []string
				switch {
				case add && kind == nip02.KindContacts:
					next, changed = nip02.Follow(old, people...)
				case add:
					tags := make([][]string, len(people))
					for i, p := range people {
						tags[i] = []string{"p", p}
					}
					next, changed = nip51.Add(old, kind, tags...)
				default:
					next, changed = nip51.Remove(old, kind, "p", people...)
				}
				if len(changed) == 0 {
					return nil, nil, nil, errNoChange
				}
				if !add && len(nip51.Values(next, "p")) == 0 && len(next.Tags) == 0 && next.Content == "" && !allowEmpty {
					return nil, nil, nil, common.UsageError(cmd, errors.New("this would publish an empty list; pass --allow-empty to mean it"))
				}
				if add {
					return next, changed, []string{}, nil
				}
				return next, []string{}, changed, nil
			})
		},
	}
	if !add {
		cmd.Flags().Bool("allow-empty", false, "Publish even if the list ends up empty")
	}
	addProfileWriteFlags(cmd)
	return cmd
}

func runProfileSet(cmd *cobra.Command, _ []string) error {
	// A nil value removes the field.
	changes := map[string]json.RawMessage{}
	setString := func(key, v string) {
		if v == "" {
			changes[key] = nil
			return
		}
		raw, _ := json.Marshal(v)
		changes[key] = raw
	}
	for _, f := range []struct{ name, key string }{
		{"name", "name"}, {"display-name", "display_name"}, {"about", "about"}, {"picture", "picture"},
		{"banner", "banner"}, {"website", "website"}, {"nip05", "nip05"}, {"lud16", "lud16"},
	} {
		if cmd.Flags().Changed(f.name) {
			v, _ := cmd.Flags().GetString(f.name)
			setString(f.key, v)
		}
	}
	fields, _ := cmd.Flags().GetStringArray("field")
	for _, kv := range fields {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return common.InvalidInputError(cmd, kv, errors.New(`--field must be key=value`))
		}
		// --field bot=true keeps a JSON value as JSON; other text is a string.
		if t := strings.TrimSpace(v); t != "" && json.Valid([]byte(t)) && !strings.HasPrefix(t, `"`) {
			changes[k] = json.RawMessage(t)
			continue
		}
		setString(k, v)
	}
	if len(changes) == 0 {
		return common.InvocationOrHelp(cmd, nil, errors.New("give at least one field to change"))
	}
	return writeReplaceable(cmd, 0, func(old *nip01.Event) (*nip01.Event, []string, []string, error) {
		meta := map[string]json.RawMessage{}
		next := nip01.NewEvent(0, "")
		next.Tags = [][]string{}
		if old != nil {
			if old.Content != "" {
				if err := json.Unmarshal([]byte(old.Content), &meta); err != nil {
					return nil, nil, nil, common.InvalidInputError(cmd, old.ID, fmt.Errorf("your current profile is not a JSON object, so it can't be merged: %w", err))
				}
			}
			next.Tags = old.Tags
		}
		added, removed := []string{}, []string{}
		for k, raw := range changes {
			if raw == nil {
				if _, ok := meta[k]; ok {
					delete(meta, k)
					removed = append(removed, k)
				}
				continue
			}
			if string(meta[k]) != string(raw) {
				meta[k] = raw
				added = append(added, k)
			}
		}
		if len(added) == 0 && len(removed) == 0 {
			return nil, nil, nil, errNoChange
		}
		sort.Strings(added)
		sort.Strings(removed)
		content, err := json.Marshal(meta)
		if err != nil {
			return nil, nil, nil, common.RuntimeError(cmd, err)
		}
		next.Content = string(content)
		return next, added, removed, nil
	})
}

func runRelaysSet(cmd *cobra.Command, _ []string) error {
	read, _ := cmd.Flags().GetStringSlice("read")
	write, _ := cmd.Flags().GetStringSlice("write")
	both, _ := cmd.Flags().GetStringSlice("both")
	dm, _ := cmd.Flags().GetStringSlice("dm")
	if len(read)+len(write)+len(both)+len(dm) == 0 {
		return common.InvocationOrHelp(cmd, nil, errors.New("give --read, --write, --both or --dm"))
	}
	normalize := func(list []string) ([]string, error) {
		var out []string
		for _, r := range list {
			u, _, err := client.ResolveRelayURL(r)
			if err != nil {
				return nil, common.InvalidInputError(cmd, r, err)
			}
			out = append(out, u.String())
		}
		return out, nil
	}
	var err error
	if read, err = normalize(read); err != nil {
		return err
	}
	if write, err = normalize(write); err != nil {
		return err
	}
	if both, err = normalize(both); err != nil {
		return err
	}
	if dm, err = normalize(dm); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	targets, err := profileTargets(cmd)
	if err != nil {
		return err
	}
	signer, closeSigner, err := social.Signer(cmd)
	if err != nil {
		return err
	}
	defer closeSigner()

	var events []*nip01.Event
	if len(read)+len(write)+len(both) > 0 {
		var entries []nip65.RelayEntry
		for _, r := range both {
			entries = append(entries, nip65.RelayEntry{URL: r, Read: true, Write: true})
		}
		for _, r := range read {
			entries = append(entries, nip65.RelayEntry{URL: r, Read: true})
		}
		for _, r := range write {
			entries = append(entries, nip65.RelayEntry{URL: r, Write: true})
		}
		events = append(events, nip65.NewRelayList(signer.PubKey(), entries))
	}
	if len(dm) > 0 {
		events = append(events, nip17.NewDMRelayList(dm))
	}
	now := uint64(time.Now().Unix())
	for _, ev := range events {
		ev.CreatedAt = now
		if err := signer.Sign(ctx, ev); err != nil {
			return social.SignerError(cmd, err)
		}
	}
	report, err := social.Publish(ctx, cmd, targets, events, signer)
	if err != nil {
		return err
	}
	if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
		common.PrintJSON(report)
	} else {
		social.PrintReportLines(cmd, report)
	}
	return social.ReportError(cmd, report)
}

// writeReplaceable reads the signer's newest event of kind, lets change
// build its replacement, and publishes it to the same relays. It refuses
// to publish when no relay answered, or (without --create) when none had
// a current version.
func writeReplaceable(cmd *cobra.Command, kind int, change func(old *nip01.Event) (next *nip01.Event, added, removed []string, err error)) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	jsonMode, _ := cmd.Flags().GetBool("json")
	targets, err := profileTargets(cmd)
	if err != nil {
		return err
	}
	signer, closeSigner, err := social.Signer(cmd)
	if err != nil {
		return err
	}
	defer closeSigner()
	me := strings.ToLower(signer.PubKey())

	old, err := currentReplaceable(ctx, cmd, targets, kind, me, signer)
	if err != nil {
		return err
	}
	if create, _ := cmd.Flags().GetBool("create"); old == nil && !create {
		return common.NotFoundError(cmd, "", fmt.Errorf("no relay has your current kind %d, so nothing was published (it may just not be on these relays); --create starts a new one", kind))
	}

	next, added, removed, err := change(old)
	if errors.Is(err, errNoChange) {
		if jsonMode {
			common.PrintJSON(listChange{Kind: kind, Added: []string{}, Removed: []string{}})
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "nothing to change")
		}
		return nil
	}
	if err != nil {
		return err
	}
	next.CreatedAt = uint64(time.Now().Unix())
	if old != nil && next.CreatedAt <= old.CreatedAt {
		// A replacement must be newer than what it replaces.
		next.CreatedAt = old.CreatedAt + 1
	}
	if err := signer.Sign(ctx, next); err != nil {
		return social.SignerError(cmd, err)
	}
	report, err := social.Publish(ctx, cmd, targets, []*nip01.Event{next}, signer)
	if err != nil {
		return err
	}
	out := listChange{ID: next.ID, Kind: kind, Added: added, Removed: removed, Created: old == nil, Event: next, Report: report}
	if jsonMode {
		common.PrintJSON(out)
	} else {
		for _, a := range added {
			fmt.Fprintf(cmd.OutOrStdout(), "+ %s\n", displayItem(a))
		}
		for _, r := range removed {
			fmt.Fprintf(cmd.OutOrStdout(), "- %s\n", displayItem(r))
		}
		social.PrintReportLines(cmd, report)
	}
	return social.ReportError(cmd, report)
}

func displayItem(v string) string {
	if len(v) == 64 {
		return social.Npub(v)
	}
	return v
}

// currentReplaceable is author's newest valid event of kind on targets, nil
// if none has one. Every target failing is a network error.
func currentReplaceable(ctx context.Context, cmd *cobra.Command, targets *client.TargetsSpec, kind int, author string, signer client.Signer) (*nip01.Event, error) {
	timeout, _ := cmd.Flags().GetDuration("timeout")
	filters := nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{Kinds: []int{kind}, Authors: []string{author}})
	var events []*nip01.Event
	err := common.WithSpinner(cmd, fmt.Sprintf("reading your current kind %d", kind), func() error {
		var qErr error
		events, qErr = client.QueryTargetsWithAuth(ctx, targets, filters, timeout, signer)
		return qErr
	})
	if err != nil {
		if errors.Is(err, client.ErrNoReachableTargets) {
			return nil, common.NetworkError(cmd, "", fmt.Errorf("no relay answered, so nothing was published: %w", err))
		}
		return nil, queryError(cmd, err)
	}
	var newest *nip01.Event
	for _, ev := range events {
		if ev.Kind != kind || !strings.EqualFold(ev.PubKey, author) || ev.Verify(nip01.WithoutPowCheck()) != nil {
			continue
		}
		if newest == nil || ev.CreatedAt > newest.CreatedAt || (ev.CreatedAt == newest.CreatedAt && ev.ID < newest.ID) {
			newest = ev
		}
	}
	return newest, nil
}
