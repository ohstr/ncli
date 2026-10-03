package huddle

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

// liveSpace is one open NIP-53 meeting space (kind:30312) that has at least
// one live session running in it.
type liveSpace struct {
	// Address is the replaceable-event coordinate, 30312:<pubkey>:<d>. It is
	// the space's durable identity, and what a client hands back to address it
	// again -- unlike a huddle room id, which only exists while occupied.
	Address    string        `json:"address"`
	Identifier string        `json:"identifier"`
	Room       string        `json:"room"`
	Status     string        `json:"status"`
	Service    string        `json:"service"`
	Endpoint   string        `json:"endpoint,omitempty"`
	Sessions   []liveSession `json:"sessions"`
}

// liveSession is one kind:30313 session inside a space, live and not stale.
type liveSession struct {
	Identifier   string `json:"identifier"`
	Title        string `json:"title"`
	Participants *int   `json:"participants,omitempty"`
	Starts       uint64 `json:"starts,omitempty"`
}

func newSpacesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spaces",
		Short: "List the NIP-53 meeting spaces that are open with a live session",
		Long: `List kind:30312 meeting spaces whose status is open and that have a live
kind:30313 session, as published to the queried relays.

A space is not a huddle room. A space is a published, addressable event
(30312:<pubkey>:<d>) describing where a meeting lives and who hosts it; a
huddle room is the ephemeral transport the audio flows over, and "ncli huddle
list" is what lists those. A space's service/endpoint tag is what points at
the transport.

Only open spaces with a live session are reported. A session claiming live
whose event has not been updated within --stale-after is treated as ended, per
the spec's allowance that clients may consider an un-refreshed live event
abandoned -- so a host whose process died does not leave a meeting that looks
forever in progress.`,
		Example: `  ncli huddle spaces
  ncli huddle spaces -s wss://relay.example
  ncli huddle spaces -s wss://relay.example --json`,
		Args: common.NoArgs,
		RunE: runSpaces,
	}

	cmd.Flags().StringSliceP("relays", "s", nil, "Relays to query (falls back to the configured prefs relays)")
	cmd.Flags().Duration("timeout", 10*time.Second, "Per-relay query timeout")
	cmd.Flags().Duration("stale-after", nip53.DefaultStaleWindow, "Treat a live session older than this as ended")

	return cmd
}

func runSpaces(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	jsonMode, _ := cmd.Flags().GetBool("json")

	targets, err := spacesTargets(cmd)
	if err != nil {
		return err
	}

	// One subscription for both kinds: a space is useless here without its
	// sessions, so asking separately would cost a second round trip to answer
	// half the question.
	filters := nip01.NewSubscriptionFilterGroup(&nip01.SubscriptionFilter{
		Kinds: []int{nip53.KindMeetingSpace, nip53.KindMeetingRoomEvent},
	})

	timeout, _ := cmd.Flags().GetDuration("timeout")

	var events []*nip01.Event
	err = common.WithSpinner(cmd, "Listing open spaces", func() error {
		var qErr error
		// QueryTargets, not client.Find: Find stops at the first target with
		// any match, which would miss a session published only to a relay
		// later in the list.
		events, qErr = client.QueryTargets(ctx, targets, filters, timeout)
		return qErr
	})
	if err != nil {
		if errors.Is(err, client.ErrNoReachableTargets) {
			return common.NetworkError(cmd, "", err)
		}
		return common.RuntimeError(cmd, err)
	}

	staleAfter, _ := cmd.Flags().GetDuration("stale-after")
	spaces := selectLiveSpaces(events, time.Now(), staleAfter)

	if jsonMode {
		common.PrintJSON(map[string]any{"spaces": spaces})
		return nil
	}

	if len(spaces) == 0 {
		fmt.Println("(no open spaces with a live session)")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SPACE\tROOM\tSESSION\tPEERS\tSERVICE")
	for _, s := range spaces {
		for _, session := range s.Sessions {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				s.Identifier, s.Room, titleOrDash(session.Title), peersOrDash(session.Participants), s.Service)
		}
	}
	_ = tw.Flush()
	return nil
}

func spacesTargets(cmd *cobra.Command) (*client.TargetsSpec, error) {
	relays, _ := cmd.Flags().GetStringSlice("relays")
	if len(relays) > 0 {
		targets, err := client.TargetsFromRelayList(relays)
		if err != nil {
			return nil, common.InvalidInputError(cmd, strings.Join(relays, ","), err)
		}
		return targets, nil
	}
	targets, err := client.TargetsFromPrefs()
	if err != nil {
		return nil, common.NotFoundError(cmd, "", err)
	}
	return targets, nil
}

// selectLiveSpaces reduces raw events to the open spaces that have a live
// session, sorted by identifier.
//
// 30312 and 30313 are addressable replaceable events, so the same space can
// come back several times -- a stale copy from one relay beside a fresh one
// from another is normal. Newest (kind, pubkey, d) wins, which is why this
// compares CreatedAt rather than trusting arrival order.
func selectLiveSpaces(events []*nip01.Event, now time.Time, staleAfter time.Duration) []liveSpace {
	type coordinate struct {
		kind   int
		pubkey string
		d      string
	}

	spaceEvents := map[coordinate]*nip53.MeetingSpace{}
	sessionEvents := map[coordinate]*nip53.MeetingRoomEvent{}
	newest := map[coordinate]uint64{}

	for _, ev := range events {
		if ev == nil {
			continue
		}
		switch ev.Kind {
		case nip53.KindMeetingSpace:
			// A malformed event is skipped, not fatal: one bad publisher must
			// not hide every other space on the relay.
			space, err := nip53.ParseMeetingSpace(ev)
			if err != nil {
				continue
			}
			key := coordinate{ev.Kind, ev.PubKey, space.Identifier}
			if at, seen := newest[key]; seen && ev.CreatedAt <= at {
				continue
			}
			newest[key] = ev.CreatedAt
			spaceEvents[key] = space
		case nip53.KindMeetingRoomEvent:
			meeting, err := nip53.ParseMeetingRoomEvent(ev)
			if err != nil {
				continue
			}
			key := coordinate{ev.Kind, ev.PubKey, meeting.Identifier}
			if at, seen := newest[key]; seen && ev.CreatedAt <= at {
				continue
			}
			newest[key] = ev.CreatedAt
			sessionEvents[key] = meeting
		}
	}

	open := map[string]*liveSpace{}
	for key, space := range spaceEvents {
		if space.Status != nip53.SpaceStatusOpen {
			continue
		}
		address := spaceAddress(key.pubkey, space.Identifier)
		open[address] = &liveSpace{
			Address:    address,
			Identifier: space.Identifier,
			Room:       space.Room,
			Status:     space.Status,
			Service:    space.Service,
			Endpoint:   space.Endpoint,
		}
	}

	for key, meeting := range sessionEvents {
		// Stale means "claims live but nobody has refreshed it", which the
		// spec lets a client read as ended. Without this an abandoned session
		// would advertise a meeting that is not happening.
		if nip53.IsStale(meeting.Status, newest[key], now, staleAfter) {
			continue
		}
		if meeting.Status != nip53.StatusLive {
			continue
		}
		parent, ok := open[spaceAddress(meeting.SpacePubkey, meeting.SpaceIdentifier)]
		if !ok {
			// The session's space is closed, private, missing from these
			// relays, or published by someone else -- nothing to attach to.
			continue
		}
		parent.Sessions = append(parent.Sessions, liveSession{
			Identifier:   meeting.Identifier,
			Title:        meeting.Title,
			Participants: meeting.CurrentParticipants,
			Starts:       meeting.Starts,
		})
	}

	live := make([]liveSpace, 0, len(open))
	for _, space := range open {
		if len(space.Sessions) == 0 {
			continue
		}
		sort.Slice(space.Sessions, func(i, j int) bool {
			return space.Sessions[i].Identifier < space.Sessions[j].Identifier
		})
		live = append(live, *space)
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Address < live[j].Address })
	return live
}

// spaceAddress builds a kind:30312 replaceable-event coordinate. Both the
// space and the session side are built here rather than read from the raw `a`
// tag, so the two always agree on spelling.
func spaceAddress(pubkey, identifier string) string {
	return fmt.Sprintf("%d:%s:%s", nip53.KindMeetingSpace, pubkey, identifier)
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
