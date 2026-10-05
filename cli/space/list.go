package space

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip53"
	"github.com/spf13/cobra"
)

// defaultQueryTimeout bounds every space read command's wait for a
// response -- a slow relay shouldn't hang the command indefinitely.
const defaultQueryTimeout = 10 * time.Second

// spaceSummary is one row of "space list" / the detail "space show"
// builds on, in both commands' --json shape.
type spaceSummary struct {
	Address    string        `json:"address"`
	Identifier string        `json:"identifier"`
	Room       string        `json:"room"`
	Status     string        `json:"status"`
	Service    string        `json:"service"`
	Endpoint   string        `json:"endpoint,omitempty"`
	Summary    string        `json:"summary,omitempty"`
	Image      string        `json:"image,omitempty"`
	Hashtags   []string      `json:"hashtags,omitempty"`
	Hosts      []string      `json:"hosts,omitempty"`
	Live       bool          `json:"live"`
	Sessions   []liveSession `json:"sessions,omitempty"`
}

// liveSession is one kind:30313 session inside a space, live and not stale.
type liveSession struct {
	Identifier   string `json:"identifier"`
	Title        string `json:"title"`
	Participants *int   `json:"participants,omitempty"`
	Starts       uint64 `json:"starts,omitempty"`
}

func newListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the NIP-53 meeting spaces a relay knows about",
		Long: `Lists every open kind:30312 meeting space published to --relay, each row
showing whether it currently has a live kind:30313 session -- a space is
the persistent, durable thing worth seeing whether or not a call is
happening in it right now, so one with no live session is still shown,
not filtered out.

A live session claiming "live" whose event has not been refreshed within
--stale-after is treated as ended, per the spec's allowance that clients
may read an un-refreshed live event as abandoned.`,
		Example: `  ncli space list
  ncli space list --relay wss://relay.example
  ncli space list --all`,
		Args: common.NoArgs,
		RunE: runList,
	}

	cmd.Flags().Duration("timeout", defaultQueryTimeout, "Query timeout")
	cmd.Flags().Duration("stale-after", nip53.DefaultStaleWindow, "Treat a live session older than this as ended")
	cmd.Flags().Bool("all", false, "Also include closed/private spaces")

	return cmd
}

func runList(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	relayFlag, _ := cmd.Flags().GetString("relay")
	relayURL, err := resolveRelay(cmd, relayFlag)
	if err != nil {
		return err
	}

	targets, err := client.TargetsFromRelayList([]string{relayURL.String()})
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	filters := nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds: []int{nip53.KindMeetingSpace, nip53.KindMeetingRoomEvent},
	})
	timeout, _ := cmd.Flags().GetDuration("timeout")

	var events []*nip01.Event
	err = common.WithSpinner(cmd, fmt.Sprintf("Listing spaces on %s", relayURL.Host), func() error {
		var qErr error
		events, qErr = client.QueryTargets(ctx, targets, filters, timeout)
		return qErr
	})
	if err != nil {
		if errors.Is(err, client.ErrNoReachableTargets) {
			return common.NetworkError(cmd, relayURL.String(), err)
		}
		return common.RuntimeError(cmd, err)
	}

	includeAll, _ := cmd.Flags().GetBool("all")
	staleAfter, _ := cmd.Flags().GetDuration("stale-after")
	spaces := selectSpaces(events, time.Now(), staleAfter, includeAll)

	jsonMode, _ := cmd.Flags().GetBool("json")
	if jsonMode {
		common.PrintJSON(map[string]any{"spaces": spaces})
		return nil
	}

	if len(spaces) == 0 {
		fmt.Println("(no spaces found)")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SPACE\tSTATUS\tLIVE\tSERVICE")
	for _, s := range spaces {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Identifier, s.Status, liveLabel(s.Live), s.Service)
	}
	return tw.Flush()
}

// selectSpaces reduces raw kind:30312/30313 events to one summary per
// space, newest copy wins (both kinds are addressable/replaceable, so a
// stale copy from one relay beside a fresh one from another is normal).
// Unlike huddle's selectLiveSpaces, a space with no live session is kept,
// not dropped -- "live" is a property shown per row, not a filter on
// whether the space is worth listing at all.
func selectSpaces(events []*nip01.Event, now time.Time, staleAfter time.Duration, includeAll bool) []spaceSummary {
	type coordinate struct {
		pubkey string
		d      string
	}

	spaceEvents := map[coordinate]*nip53.MeetingSpace{}
	sessionEvents := map[coordinate]*nip53.MeetingRoomEvent{}
	sessionCreatedAt := map[coordinate]uint64{}
	newestSpace := map[coordinate]uint64{}

	for _, ev := range events {
		if ev == nil {
			continue
		}
		switch ev.Kind {
		case nip53.KindMeetingSpace:
			// A malformed event is skipped, not fatal: one bad publisher
			// must not hide every other space on the relay.
			parsed, err := nip53.ParseMeetingSpace(ev)
			if err != nil {
				continue
			}
			key := coordinate{ev.PubKey, parsed.Identifier}
			if at, seen := newestSpace[key]; seen && ev.CreatedAt <= at {
				continue
			}
			newestSpace[key] = ev.CreatedAt
			spaceEvents[key] = parsed
		case nip53.KindMeetingRoomEvent:
			parsed, err := nip53.ParseMeetingRoomEvent(ev)
			if err != nil {
				continue
			}
			key := coordinate{ev.PubKey, parsed.Identifier}
			if at, seen := sessionCreatedAt[key]; seen && ev.CreatedAt <= at {
				continue
			}
			sessionCreatedAt[key] = ev.CreatedAt
			sessionEvents[key] = parsed
		}
	}

	bySpaceAddress := map[string]*spaceSummary{}
	for key, parsed := range spaceEvents {
		if !includeAll && parsed.Status != nip53.SpaceStatusOpen {
			continue
		}
		address := spaceAddress(key.pubkey, parsed.Identifier)
		hosts := make([]string, 0, len(parsed.Hosts()))
		for _, h := range parsed.Hosts() {
			hosts = append(hosts, h.Pubkey)
		}
		bySpaceAddress[address] = &spaceSummary{
			Address:    address,
			Identifier: parsed.Identifier,
			Room:       parsed.Room,
			Status:     parsed.Status,
			Service:    parsed.Service,
			Endpoint:   parsed.Endpoint,
			Summary:    parsed.Summary,
			Image:      parsed.Image,
			Hashtags:   parsed.Hashtags,
			Hosts:      hosts,
		}
	}

	for key, session := range sessionEvents {
		parent, ok := bySpaceAddress[spaceAddress(session.SpacePubkey, session.SpaceIdentifier)]
		if !ok {
			// The session's space is excluded (closed/private and --all
			// wasn't passed), missing from this relay, or published by
			// someone else -- nothing to attach to.
			continue
		}
		if session.Status != nip53.StatusLive || nip53.IsStale(session.Status, sessionCreatedAt[key], now, staleAfter) {
			continue
		}
		parent.Live = true
		parent.Sessions = append(parent.Sessions, liveSession{
			Identifier:   session.Identifier,
			Title:        session.Title,
			Participants: session.CurrentParticipants,
			Starts:       session.Starts,
		})
	}

	spaces := make([]spaceSummary, 0, len(bySpaceAddress))
	for _, s := range bySpaceAddress {
		sort.Slice(s.Sessions, func(i, j int) bool { return s.Sessions[i].Identifier < s.Sessions[j].Identifier })
		spaces = append(spaces, *s)
	}
	sort.Slice(spaces, func(i, j int) bool { return spaces[i].Address < spaces[j].Address })
	return spaces
}

// spaceAddress builds a kind:30312 replaceable-event coordinate.
func spaceAddress(pubkey, identifier string) string {
	return fmt.Sprintf("%d:%s:%s", nip53.KindMeetingSpace, pubkey, identifier)
}

func liveLabel(live bool) string {
	if live {
		return "live"
	}
	return "-"
}

func titleOrDash(title string) string {
	if strings.TrimSpace(title) == "" {
		return "-"
	}
	return title
}

func peersOrDash(participants *int) string {
	if participants == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *participants)
}
