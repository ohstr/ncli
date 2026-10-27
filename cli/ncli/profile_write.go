package ncli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/signal"
	"sort"
	"strings"
	"sync"
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
vault label. Refuses unless every relay answers, so a list is never
rebuilt from a stale copy, and never removes the last person unless
--allow-empty.`,
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
				if !add && len(nip51.Values(next, "p")) == 0 && !allowEmpty {
					return nil, nil, nil, common.UsageError(cmd, errors.New("this would leave no one on the list; pass --allow-empty to mean it"))
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
	for _, list := range []*[]string{&read, &write, &both, &dm} {
		if *list, err = normalize(*list); err != nil {
			return err
		}
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
	me := strings.ToLower(signer.PubKey())

	var changes []listChange
	var reports []*client.PublishReport
	replace := func(kind int, next *nip01.Event) error {
		c, err := replaceOne(ctx, cmd, targets, signer, me, kind, func(old *nip01.Event) (*nip01.Event, []string, []string, error) {
			added, removed := tagDiff(old, next)
			if len(added) == 0 && len(removed) == 0 {
				return nil, nil, nil, errNoChange
			}
			return next, added, removed, nil
		})
		if err != nil {
			return err
		}
		changes = append(changes, *c)
		if c.Report != nil {
			reports = append(reports, c.Report)
		}
		return nil
	}
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
		if err := replace(nip65.KindRelayListMetadata, nip65.NewRelayList(me, entries)); err != nil {
			return err
		}
	}
	if len(dm) > 0 {
		if err := replace(nip17.KindDMRelays, nip17.NewDMRelayList(dm)); err != nil {
			return err
		}
	}
	if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
		common.PrintJSON(changes)
	} else {
		for _, c := range changes {
			printChange(cmd, c)
		}
	}
	for _, r := range reports {
		if err := social.ReportError(cmd, r); err != nil {
			return err
		}
	}
	return nil
}

// tagDiff lists the tags (joined) in next but not old, and in old but not
// next.
func tagDiff(old, next *nip01.Event) (added, removed []string) {
	key := func(t []string) string { return strings.Join(t, " ") }
	have := map[string]bool{}
	if old != nil {
		for _, t := range old.Tags {
			have[key(t)] = true
		}
	}
	want := map[string]bool{}
	for _, t := range next.Tags {
		want[key(t)] = true
		if !have[key(t)] {
			added = append(added, key(t))
		}
	}
	if old != nil {
		for _, t := range old.Tags {
			if !want[key(t)] {
				removed = append(removed, key(t))
			}
		}
	}
	return added, removed
}

// writeReplaceable reads the signer's current event of kind, lets change
// build its replacement, publishes it and prints the change.
func writeReplaceable(cmd *cobra.Command, kind int, change func(old *nip01.Event) (next *nip01.Event, added, removed []string, err error)) error {
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

	c, err := replaceOne(ctx, cmd, targets, signer, strings.ToLower(signer.PubKey()), kind, change)
	if err != nil {
		return err
	}
	if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
		common.PrintJSON(c)
	} else {
		printChange(cmd, *c)
	}
	if c.Report == nil {
		return nil
	}
	return social.ReportError(cmd, c.Report)
}

// replaceOne reads me's current event of kind from every target, lets
// change build its replacement and publishes it to the same targets. It
// refuses unless every target answered (a relay that didn't may hold a
// newer version than the others), and without --create when none had one.
// A change of nothing publishes nothing.
func replaceOne(ctx context.Context, cmd *cobra.Command, targets *client.TargetsSpec, signer client.Crypto, me string, kind int, change func(old *nip01.Event) (*nip01.Event, []string, []string, error)) (*listChange, error) {
	old, err := currentReplaceable(ctx, cmd, targets, kind, me, signer)
	if err != nil {
		return nil, err
	}
	if create, _ := cmd.Flags().GetBool("create"); old == nil && !create {
		return nil, common.NotFoundError(cmd, "", fmt.Errorf("no relay has your current kind %d, so nothing was published (it may just not be on these relays); --create starts a new one", kind))
	}
	next, added, removed, err := change(old)
	if errors.Is(err, errNoChange) {
		return &listChange{Kind: kind, Added: []string{}, Removed: []string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if added == nil {
		added = []string{}
	}
	if removed == nil {
		removed = []string{}
	}
	next.PubKey, next.ID, next.Sig = "", "", ""
	next.CreatedAt = uint64(time.Now().Unix())
	if old != nil && next.CreatedAt <= old.CreatedAt {
		// A replacement must be newer than what it replaces.
		next.CreatedAt = old.CreatedAt + 1
	}
	if err := signer.Sign(ctx, next); err != nil {
		return nil, social.SignerError(cmd, err)
	}
	report, err := social.Publish(ctx, cmd, targets, []*nip01.Event{next}, signer)
	if err != nil {
		return nil, err
	}
	return &listChange{ID: next.ID, Kind: kind, Added: added, Removed: removed, Created: old == nil, Event: next, Report: report}, nil
}

func printChange(cmd *cobra.Command, c listChange) {
	if c.ID == "" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "kind %d: nothing to change\n", c.Kind)
		return
	}
	for _, a := range c.Added {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "+ %s\n", displayItem(a))
	}
	for _, r := range c.Removed {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "- %s\n", displayItem(r))
	}
	social.PrintReportLines(cmd, c.Report)
}

func displayItem(v string) string {
	if len(v) == 64 {
		return social.Npub(v)
	}
	return v
}

// currentReplaceable is author's newest valid event of kind, nil if no
// target has one. Every target must answer: one that doesn't may hold the
// newest version, and merging onto an older one would lose what it adds.
func currentReplaceable(ctx context.Context, cmd *cobra.Command, targets *client.TargetsSpec, kind int, author string, signer client.Signer) (*nip01.Event, error) {
	timeout, _ := cmd.Flags().GetDuration("timeout")
	filters := nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{Kinds: []int{kind}, Authors: []string{author}})
	reads := make([]client.TargetRead, len(targets.Relays))
	_ = common.WithSpinner(cmd, fmt.Sprintf("reading your current kind %d", kind), func() error {
		var wg sync.WaitGroup
		for i, t := range targets.Relays {
			wg.Add(1)
			go func(i int, t *client.FlowSpec) {
				defer wg.Done()
				reads[i] = client.ReadTarget(ctx, t, filters, timeout, signer)
			}(i, t)
		}
		wg.Wait()
		return nil
	})
	var failed []string
	var newest *nip01.Event
	for _, r := range reads {
		if r.Err != nil || r.Restricted {
			failed = append(failed, r.Target)
			continue
		}
		for _, ev := range r.Events {
			if ev.Kind != kind || !strings.EqualFold(ev.PubKey, author) || ev.Verify(nip01.WithoutPowCheck()) != nil {
				continue
			}
			if newest == nil || ev.CreatedAt > newest.CreatedAt || (ev.CreatedAt == newest.CreatedAt && ev.ID < newest.ID) {
				newest = ev
			}
		}
	}
	if len(failed) > 0 {
		return nil, common.NetworkError(cmd, strings.Join(failed, ","), fmt.Errorf("couldn't read your current kind %d from %s, so nothing was published; retry, or pass -s with only the relays you can reach", kind, strings.Join(failed, ", ")))
	}
	return newest, nil
}
