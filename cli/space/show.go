package space

import (
	"errors"
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nip53"
	"github.com/spf13/cobra"
)

func newShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one space's full detail",
		Long: `Looks up kind:30312 events whose "d" tag is id on --relay. A bare id is
not globally unique -- more than one pubkey can use the same identifier --
so every match is shown, not just the first.`,
		Example: `  ncli space show standup`,
		Args:    common.ExactArgs(1),
		RunE:    runShow,
	}

	cmd.Flags().Duration("timeout", defaultQueryTimeout, "Query timeout")
	cmd.Flags().Duration("stale-after", nip53.DefaultStaleWindow, "Treat a live session older than this as ended")

	return cmd
}

func runShow(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	id := args[0]

	relayFlag, _ := cmd.Flags().GetString("relay")
	relayURL, err := resolveRelay(cmd, relayFlag)
	if err != nil {
		return err
	}

	targets, err := client.TargetsFromRelayList([]string{relayURL.String()})
	if err != nil {
		return common.RuntimeError(cmd, err)
	}

	// Sessions reference their space by pubkey+identifier, not by the
	// space's own event id, so sessions can't be narrowed by this same
	// "d" filter -- pull every session on the relay and let selectSpaces
	// discard the ones that don't attach to a matching space.
	filters := nip01.NewSubscriptionFilterGroup(
		&nip01.SubscriptionFilter{Kinds: []int{nip53.KindMeetingSpace}, Tags: map[string][]string{"d": {id}}},
		&nip01.SubscriptionFilter{Kinds: []int{nip53.KindMeetingRoomEvent}},
	)
	timeout, _ := cmd.Flags().GetDuration("timeout")

	var events []*nip01.Event
	err = common.WithSpinner(cmd, fmt.Sprintf("Querying %s for %s", relayURL.Host, id), func() error {
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

	staleAfter, _ := cmd.Flags().GetDuration("stale-after")
	spaces := selectSpaces(events, time.Now(), staleAfter, true)

	jsonMode, _ := cmd.Flags().GetBool("json")
	if jsonMode {
		if spaces == nil {
			spaces = []spaceSummary{}
		}
		common.PrintJSON(map[string]any{"spaces": spaces})
		return nil
	}

	if len(spaces) == 0 {
		fmt.Printf("(no space found with id %q)\n", id)
		return nil
	}

	for i, s := range spaces {
		if i > 0 {
			fmt.Println()
		}
		printSpaceDetail(s)
	}
	return nil
}

func printSpaceDetail(s spaceSummary) {
	fmt.Printf("address:    %s\n", s.Address)
	fmt.Printf("status:     %s\n", s.Status)
	fmt.Printf("live:       %s\n", liveLabel(s.Live))
	fmt.Printf("service:    %s\n", s.Service)
	if s.Endpoint != "" {
		fmt.Printf("endpoint:   %s\n", s.Endpoint)
	}
	fmt.Printf("summary:    %s\n", dashIfEmpty(s.Summary))
	if len(s.Hashtags) > 0 {
		fmt.Printf("hashtags:   %s\n", strings.Join(s.Hashtags, ", "))
	}
	if len(s.Hosts) > 0 {
		fmt.Printf("hosts:      %s\n", strings.Join(s.Hosts, ", "))
	}
	if len(s.Sessions) == 0 {
		fmt.Println("sessions:   (none live)")
		return
	}
	fmt.Println("sessions:")
	for _, session := range s.Sessions {
		fmt.Printf("  %s\t%s\t%s peers\n", session.Identifier, titleOrDash(session.Title), peersOrDash(session.Participants))
	}
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
